package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func testApp(t *testing.T, ollama http.Handler) (*Server, *gin.Engine, *httptest.Server, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ollamaServer := httptest.NewServer(ollama)
	dbPath := filepath.Join(t.TempDir(), "spirit-test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Close()
		ollamaServer.Close()
	})
	server := &Server{db: db, ollama: ollamaServer.URL, client: ollamaServer.Client()}
	if err := server.init(); err != nil {
		t.Fatal(err)
	}
	return server, server.router(), ollamaServer, dbPath
}

func performJSON(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestChatPersistsAndCanBeRetrievedAfterRestart(t *testing.T) {
	ollama := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model    string          `json:"model"`
			Messages []ollamaMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Ollama request: %v", err)
		}
		if request.Model != "gemma2:latest" {
			t.Errorf("model = %q, want gemma2:latest", request.Model)
		}
		if len(request.Messages) < 2 || request.Messages[0].Role != "system" {
			t.Errorf("request did not include Ghost system prompt and user message: %#v", request.Messages)
		}
		if !strings.Contains(request.Messages[0].Content, "digital spirit") {
			t.Errorf("system prompt missing Ghost persona: %q", request.Messages[0].Content)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"A reply from the machine."}}`))
	})
	server, router, _, dbPath := testApp(t, ollama)

	created := performJSON(t, router, http.MethodPost, "/api/chat", `{"message":"Build a tiny clock","model":"gemma2:latest"}`)
	if created.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", created.Code, created.Body)
	}
	var result struct {
		ConversationID int64  `json:"conversation_id"`
		Reply          string `json:"reply"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ConversationID < 1 || result.Reply != "A reply from the machine." {
		t.Fatalf("unexpected chat response: %+v", result)
	}

	listed := performJSON(t, router, http.MethodGet, "/api/chats?q=clock", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "Build a tiny clock") {
		t.Fatalf("search did not return created conversation: %d %s", listed.Code, listed.Body)
	}
	detail := performJSON(t, router, http.MethodGet, "/api/chats/"+intString(result.ConversationID), "")
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "A reply from the machine.") {
		t.Fatalf("conversation detail did not include persisted messages: %d %s", detail.Code, detail.Body)
	}

	if err := server.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	restarted := &Server{db: db, ollama: "unused", client: http.DefaultClient}
	if err := restarted.init(); err != nil {
		t.Fatal(err)
	}
	restartedRouter := restarted.router()
	restartedDetail := performJSON(t, restartedRouter, http.MethodGet, "/api/chats/"+intString(result.ConversationID), "")
	if restartedDetail.Code != http.StatusOK || !strings.Contains(restartedDetail.Body.String(), "A reply from the machine.") {
		t.Fatalf("conversation did not survive backend restart: %d %s", restartedDetail.Code, restartedDetail.Body)
	}
	_ = db.Close()
}

func TestConversationRenameDeleteAndValidation(t *testing.T) {
	_, router, _, _ := testApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	created := performJSON(t, router, http.MethodPost, "/api/chats", `{"title":"Untitled"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body)
	}
	var chat Chat
	if err := json.Unmarshal(created.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	renamed := performJSON(t, router, http.MethodPatch, "/api/chats/"+intString(chat.ID), `{"title":"A clearer name"}`)
	if renamed.Code != http.StatusOK || !strings.Contains(renamed.Body.String(), "A clearer name") {
		t.Fatalf("rename failed: %d %s", renamed.Code, renamed.Body)
	}
	if invalid := performJSON(t, router, http.MethodPatch, "/api/chats/"+intString(chat.ID), `{"title":"  "}`); invalid.Code != http.StatusBadRequest {
		t.Fatalf("blank title status = %d, want 400", invalid.Code)
	}
	if deleted := performJSON(t, router, http.MethodDelete, "/api/chats/"+intString(chat.ID), ""); deleted.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", deleted.Code, deleted.Body)
	}
	if missing := performJSON(t, router, http.MethodGet, "/api/chats/"+intString(chat.ID), ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing chat status = %d, want 404", missing.Code)
	}
}

func TestSoulLedgerCRUDAndNaturalInstruction(t *testing.T) {
	ollama := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":{"content":"I used the saved preference."}}`))
			return
		}
		http.NotFound(w, r)
	})
	_, router, _, _ := testApp(t, ollama)
	added := performJSON(t, router, http.MethodPost, "/api/memories", `{"content":"Prefers concise responses"}`)
	if added.Code != http.StatusCreated {
		t.Fatalf("add memory status = %d, body = %s", added.Code, added.Body)
	}
	duplicate := performJSON(t, router, http.MethodPost, "/api/memories", `{"content":"prefers concise responses"}`)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate memory status = %d, want 200", duplicate.Code)
	}
	var memory Memory
	if err := json.Unmarshal(added.Body.Bytes(), &memory); err != nil {
		t.Fatal(err)
	}
	edited := performJSON(t, router, http.MethodPatch, "/api/memories/"+intString(memory.ID), `{"content":"Prefers short, clear answers"}`)
	if edited.Code != http.StatusOK || !strings.Contains(edited.Body.String(), "short, clear answers") {
		t.Fatalf("edit memory failed: %d %s", edited.Code, edited.Body)
	}
	chat := performJSON(t, router, http.MethodPost, "/api/chat", `{"message":"Remember that I prefer concise answers."}`)
	if chat.Code != http.StatusOK || !strings.Contains(chat.Body.String(), `"memory_saved":true`) {
		t.Fatalf("natural memory instruction was not confirmed: %d %s", chat.Code, chat.Body)
	}
	list := performJSON(t, router, http.MethodGet, "/api/memories", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "prefer concise answers") {
		t.Fatalf("memory list missing naturally saved memory: %d %s", list.Code, list.Body)
	}
	deleted := performJSON(t, router, http.MethodDelete, "/api/memories/"+intString(memory.ID), "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete memory failed: %d %s", deleted.Code, deleted.Body)
	}
	cleared := performJSON(t, router, http.MethodDelete, "/api/memories", "")
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear memory failed: %d %s", cleared.Code, cleared.Body)
	}
}

func TestModelDiscoveryHealthAndPull(t *testing.T) {
	ollama := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"zeta:latest"},{"name":"gemma2:latest"}]}`))
		case "/api/pull":
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			http.NotFound(w, r)
		}
	})
	_, router, _, _ := testApp(t, ollama)
	models := performJSON(t, router, http.MethodGet, "/api/models", "")
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), "gemma2:latest") || !strings.Contains(models.Body.String(), `"connected":true`) {
		t.Fatalf("model discovery failed: %d %s", models.Code, models.Body)
	}
	health := performJSON(t, router, http.MethodGet, "/api/health", "")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"ollama_connected":true`) {
		t.Fatalf("health failed: %d %s", health.Code, health.Body)
	}
	pulled := performJSON(t, router, http.MethodPost, "/api/pull", `{"model":"gemma2:latest"}`)
	if pulled.Code != http.StatusOK || !strings.Contains(pulled.Body.String(), "Model downloaded") {
		t.Fatalf("model pull failed: %d %s", pulled.Code, pulled.Body)
	}
}

func TestOllamaUnavailableIsNotReportedAsEmptySuccess(t *testing.T) {
	_, router, _, _ := testApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	models := performJSON(t, router, http.MethodGet, "/api/models", "")
	if models.Code != http.StatusServiceUnavailable || !strings.Contains(models.Body.String(), `"connected":false`) {
		t.Fatalf("unavailable Ollama should be explicit: %d %s", models.Code, models.Body)
	}
	health := performJSON(t, router, http.MethodGet, "/api/health", "")
	if !strings.Contains(health.Body.String(), `"ollama_connected":false`) {
		t.Fatalf("health should report Ollama disconnected: %s", health.Body)
	}
	offlineServer, offlineRouter, stoppedOllama, _ := testApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	stoppedOllama.Close()
	offlineServer.ollama = stoppedOllama.URL
	offlineModels := performJSON(t, offlineRouter, http.MethodGet, "/api/models", "")
	if offlineModels.Code != http.StatusServiceUnavailable || !strings.Contains(offlineModels.Body.String(), `"connected":false`) {
		t.Fatalf("offline Ollama should be explicit: %d %s", offlineModels.Code, offlineModels.Body)
	}
}

func TestChatRequestIsIdempotent(t *testing.T) {
	callCount := 0
	ollama := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"Single saved answer."}}`))
	})
	server, router, _, _ := testApp(t, ollama)
	body := `{"message":"Do the operation once","request_id":"request-123"}`
	first := performJSON(t, router, http.MethodPost, "/api/chat", body)
	second := performJSON(t, router, http.MethodPost, "/api/chat", body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate request was not replayed: %d %s; %d %s", first.Code, first.Body, second.Code, second.Body)
	}
	if callCount != 1 {
		t.Fatalf("Ollama called %d times, want 1", callCount)
	}
	var messageCount int
	if err := server.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 2 {
		t.Fatalf("stored %d messages, want one user and one assistant message", messageCount)
	}
}

func TestInferenceFailureDoesNotPersistPartialTurn(t *testing.T) {
	server, router, _, _ := testApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model is missing", http.StatusNotFound)
	}))
	response := performJSON(t, router, http.MethodPost, "/api/chat", `{"message":"This should not be stored"}`)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("inference failure status = %d, want 502: %s", response.Code, response.Body)
	}
	var chatCount, messageCount int
	if err := server.db.QueryRow(`SELECT COUNT(*) FROM chats`).Scan(&chatCount); err != nil {
		t.Fatal(err)
	}
	if err := server.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if chatCount != 0 || messageCount != 0 {
		t.Fatalf("failed turn left partial data: chats=%d messages=%d", chatCount, messageCount)
	}
}

func intString(value int64) string {
	return strings.TrimSpace(strconv.FormatInt(value, 10))
}
