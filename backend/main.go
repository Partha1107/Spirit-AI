package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

const ghostSystemPrompt = `You are Ghost, a digital spirit living in the user's local machine. You are mysterious but friendly, intelligent, witty, and approachable. Help with programming, research, learning, and everyday tasks. Explain technical ideas clearly. Be accurate, acknowledge uncertainty, and never claim access to files, devices, memories, or actions unless the user supplied them or the application explicitly confirms them. Use subtle ghostly language only when it fits; do not force spooky phrasing. The user's computer is your home, debugging is an investigation, and saved long-term memories are called the Soul Ledger. Do not claim to save a memory unless the application confirms it.`

const maxMessageLength = 20000
const maxTitleLength = 100

type Server struct {
	db     *sql.DB
	ollama string
	client *http.Client
}

type Chat struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Message struct {
	ID        int64     `json:"id"`
	ChatID    int64     `json:"chat_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Memory struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ChatReq struct {
	ConversationID int64  `json:"conversation_id"`
	Message        string `json:"message"`
	Model          string `json:"model"`
	RequestID      string `json:"request_id"`
}

type memoryRequest struct {
	Content string `json:"content"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func main() {
	dbPath := env("GHOST_DB", "ghost.db")
	db, err := sql.Open("sqlite", dbPath)
	must(err)
	db.SetMaxOpenConns(1)
	defer db.Close()

	server := &Server{
		db:     db,
		ollama: strings.TrimRight(env("OLLAMA_URL", "http://127.0.0.1:11434"), "/"),
		client: &http.Client{Timeout: 5 * time.Minute},
	}
	must(server.init())

	address := env("GHOST_LISTEN", ":5000")
	fmt.Printf("Ghost API %s | SQLite: %s | Ollama: %s\n", address, dbPath, server.ollama)
	must(server.router().Run(address))
}

func (s *Server) router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery(), cors())
	r.GET("/api/health", s.health)
	r.GET("/api/chats", s.chats)
	r.POST("/api/chats", s.newChat)
	r.GET("/api/chats/:id", s.getChat)
	r.PATCH("/api/chats/:id", s.renameChat)
	r.DELETE("/api/chats/:id", s.deleteChat)
	r.POST("/api/chat", s.chat)
	r.GET("/api/memories", s.memories)
	r.POST("/api/memories", s.addMemory)
	r.PATCH("/api/memories/:id", s.editMemory)
	r.DELETE("/api/memories/:id", s.removeMemory)
	r.DELETE("/api/memories", s.clearMemories)
	r.GET("/api/models", s.models)
	r.POST("/api/pull", s.pull)
	return r
}

func (s *Server) init() error {
	_, err := s.db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS chats (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 title TEXT NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS messages (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
 content TEXT NOT NULL,
 request_id TEXT,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS messages_chat ON messages(chat_id, id);
CREATE TABLE IF NOT EXISTS memories (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 content TEXT NOT NULL,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return err
	}
	hasRequestID := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "request_id" {
			hasRequestID = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if !hasRequestID {
		if _, err := s.db.Exec(`ALTER TABLE messages ADD COLUMN request_id TEXT`); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS messages_request_id ON messages(request_id) WHERE request_id IS NOT NULL`)
	return err
}

func (s *Server) chats(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	statement := `SELECT id, title, created_at, updated_at FROM chats`
	args := []any{}
	if query != "" {
		statement += ` WHERE instr(lower(title), lower(?)) > 0 OR EXISTS (
 SELECT 1 FROM messages WHERE messages.chat_id = chats.id AND instr(lower(messages.content), lower(?)) > 0
)`
		args = append(args, query, query)
	}
	statement += ` ORDER BY updated_at DESC, id DESC`
	rows, err := s.db.QueryContext(c.Request.Context(), statement, args...)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not load conversations")
		return
	}
	defer rows.Close()

	items := []Chat{}
	for rows.Next() {
		var chat Chat
		if err := rows.Scan(&chat.ID, &chat.Title, &chat.CreatedAt, &chat.UpdatedAt); err != nil {
			writeError(c, http.StatusInternalServerError, "Could not read conversation history")
			return
		}
		items = append(items, chat)
	}
	if err := rows.Err(); err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read conversation history")
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) newChat(c *gin.Context) {
	var body struct {
		Title string `json:"title"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			writeError(c, http.StatusBadRequest, "Invalid conversation details")
			return
		}
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "New conversation"
	}
	if runeCount(title) > maxTitleLength {
		writeError(c, http.StatusBadRequest, "Conversation title must be 100 characters or fewer")
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `INSERT INTO chats(title) VALUES(?)`, title)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not create conversation")
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not create conversation")
		return
	}
	chat, err := s.chatByID(c.Request.Context(), id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read created conversation")
		return
	}
	c.JSON(http.StatusCreated, chat)
}

func (s *Server) getChat(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Conversation ID must be a positive integer")
		return
	}
	chat, err := s.chatByID(c.Request.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(c, http.StatusNotFound, "Conversation not found")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not load conversation")
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id, chat_id, role, content, created_at FROM messages WHERE chat_id=? ORDER BY id`, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not load conversation messages")
		return
	}
	defer rows.Close()
	messages := []Message{}
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ChatID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			writeError(c, http.StatusInternalServerError, "Could not read conversation messages")
			return
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read conversation messages")
		return
	}
	c.JSON(http.StatusOK, gin.H{"chat": chat, "messages": messages})
}

func (s *Server) renameChat(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Conversation ID must be a positive integer")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "A conversation title is required")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || runeCount(title) > maxTitleLength {
		writeError(c, http.StatusBadRequest, "Title must contain 1 to 100 characters")
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE chats SET title=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, title, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not rename conversation")
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		writeError(c, http.StatusNotFound, "Conversation not found")
		return
	}
	chat, err := s.chatByID(c.Request.Context(), id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read renamed conversation")
		return
	}
	c.JSON(http.StatusOK, chat)
}

func (s *Server) deleteChat(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Conversation ID must be a positive integer")
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM chats WHERE id=?`, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not delete conversation")
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		writeError(c, http.StatusNotFound, "Conversation not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) chat(c *gin.Context) {
	var body ChatReq
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid chat request")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" || runeCount(body.Message) > maxMessageLength {
		writeError(c, http.StatusBadRequest, "Message must contain 1 to 20,000 characters")
		return
	}
	body.RequestID = strings.TrimSpace(body.RequestID)
	if runeCount(body.RequestID) > 100 {
		writeError(c, http.StatusBadRequest, "Request ID is too long")
		return
	}
	if body.RequestID != "" {
		var previousID int64
		var previousReply string
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT user_message.chat_id, assistant_message.content
FROM messages AS user_message
JOIN messages AS assistant_message ON assistant_message.chat_id=user_message.chat_id AND assistant_message.id>user_message.id
WHERE user_message.request_id=? AND user_message.role='user' AND assistant_message.role='assistant'
ORDER BY assistant_message.id LIMIT 1`, body.RequestID).Scan(&previousID, &previousReply)
		if err == nil {
			c.JSON(http.StatusOK, gin.H{"conversation_id": previousID, "reply": previousReply, "duplicate": true})
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			writeError(c, http.StatusInternalServerError, "Could not check previous chat request")
			return
		}
	}
	if body.ConversationID < 0 {
		writeError(c, http.StatusBadRequest, "Conversation ID must be a positive integer")
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = env("GHOST_MODEL", "qwen2.5:0.5b")
	}
	if runeCount(model) > 200 {
		writeError(c, http.StatusBadRequest, "Model name is too long")
		return
	}

	var chatID int64
	history := []ollamaMessage{}
	messageCount := 0
	if body.ConversationID > 0 {
		if _, err := s.chatByID(c.Request.Context(), body.ConversationID); errors.Is(err, sql.ErrNoRows) {
			writeError(c, http.StatusNotFound, "Conversation not found")
			return
		} else if err != nil {
			writeError(c, http.StatusInternalServerError, "Could not load conversation")
			return
		}
		chatID = body.ConversationID
		rows, err := s.db.QueryContext(c.Request.Context(), `SELECT role, content FROM messages WHERE chat_id=? ORDER BY id`, chatID)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "Could not read conversation history")
			return
		}
		for rows.Next() {
			var message ollamaMessage
			if err := rows.Scan(&message.Role, &message.Content); err != nil {
				rows.Close()
				writeError(c, http.StatusInternalServerError, "Could not read conversation history")
				return
			}
			history = append(history, message)
			if message.Role == "user" {
				messageCount++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(c, http.StatusInternalServerError, "Could not read conversation history")
			return
		}
		rows.Close()
	}

	reply, memorySaved, err := s.reply(c.Request.Context(), body.Message, model, history)
	if err != nil {
		writeError(c, http.StatusBadGateway, err.Error())
		return
	}

	transaction, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not save conversation")
		return
	}
	if chatID == 0 {
		result, err := transaction.ExecContext(c.Request.Context(), `INSERT INTO chats(title) VALUES(?)`, truncate(body.Message, 56))
		if err != nil {
			transaction.Rollback()
			writeError(c, http.StatusInternalServerError, "Could not create conversation")
			return
		}
		chatID, err = result.LastInsertId()
		if err != nil {
			transaction.Rollback()
			writeError(c, http.StatusInternalServerError, "Could not create conversation")
			return
		}
	} else if messageCount == 0 {
		if _, err := transaction.ExecContext(c.Request.Context(), `UPDATE chats SET title=? WHERE id=?`, truncate(body.Message, 56), chatID); err != nil {
			transaction.Rollback()
			writeError(c, http.StatusInternalServerError, "Could not title conversation")
			return
		}
	}
	if _, err := transaction.ExecContext(c.Request.Context(), `INSERT INTO messages(chat_id, role, content, request_id) VALUES(?, 'user', ?, ?)`, chatID, body.Message, nullableString(body.RequestID)); err != nil {
		transaction.Rollback()
		writeError(c, http.StatusInternalServerError, "Could not save your message")
		return
	}
	if _, err := transaction.ExecContext(c.Request.Context(), `INSERT INTO messages(chat_id, role, content) VALUES(?, 'assistant', ?)`, chatID, reply); err != nil {
		transaction.Rollback()
		writeError(c, http.StatusInternalServerError, "Could not save Ghost's response")
		return
	}
	if _, err := transaction.ExecContext(c.Request.Context(), `UPDATE chats SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, chatID); err != nil {
		transaction.Rollback()
		writeError(c, http.StatusInternalServerError, "Could not update conversation")
		return
	}
	if err := transaction.Commit(); err != nil {
		writeError(c, http.StatusInternalServerError, "Could not save conversation")
		return
	}
	c.JSON(http.StatusOK, gin.H{"conversation_id": chatID, "reply": reply, "memory_saved": memorySaved})
}

func (s *Server) reply(ctx context.Context, prompt, model string, history []ollamaMessage) (string, bool, error) {
	if memory, requested := memoryInstruction(prompt); requested {
		if memory == "" {
			return "Tell me what you'd like me to remember, and I'll add it to the Soul Ledger.", false, nil
		}
		_, created, err := s.saveMemory(ctx, memory)
		if err != nil {
			return "", false, fmt.Errorf("could not save memory: %w", err)
		}
		if created {
			return "I've added that to the Soul Ledger: " + memory, true, nil
		}
		return "That is already recorded in the Soul Ledger: " + memory, false, nil
	}

	memories, err := s.relevant(ctx, prompt)
	if err != nil {
		return "", false, fmt.Errorf("could not read saved memories: %w", err)
	}
	system := ghostSystemPrompt
	if len(memories) > 0 {
		system += "\n\nRelevant user-approved Soul Ledger entries. Treat them as context only when useful; do not claim access to other memories:\n- " + strings.Join(memories, "\n- ")
	}
	messages := make([]ollamaMessage, 0, len(history)+2)
	messages = append(messages, ollamaMessage{Role: "system", Content: system})
	messages = append(messages, history...)
	messages = append(messages, ollamaMessage{Role: "user", Content: prompt})
	payload, err := json.Marshal(gin.H{"model": model, "messages": messages, "stream": false})
	if err != nil {
		return "", false, fmt.Errorf("could not prepare local inference request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ollama+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return "", false, fmt.Errorf("could not create local inference request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("Ollama is unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", false, fmt.Errorf("Ollama returned %s: %s", response.Status, truncate(string(detail), 250))
	}
	var result struct {
		Message ollamaMessage `json:"message"`
		Error   string        `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		return "", false, fmt.Errorf("Ollama returned an invalid response")
	}
	if result.Error != "" {
		return "", false, fmt.Errorf("Ollama error: %s", result.Error)
	}
	reply := strings.TrimSpace(result.Message.Content)
	if reply == "" {
		return "", false, fmt.Errorf("Ollama returned an empty reply")
	}
	return reply, false, nil
}

func (s *Server) memories(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id, content, created_at, updated_at FROM memories ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not load Soul Ledger")
		return
	}
	defer rows.Close()
	items := []Memory{}
	for rows.Next() {
		var memory Memory
		if err := rows.Scan(&memory.ID, &memory.Content, &memory.CreatedAt, &memory.UpdatedAt); err != nil {
			writeError(c, http.StatusInternalServerError, "Could not read Soul Ledger")
			return
		}
		items = append(items, memory)
	}
	if err := rows.Err(); err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read Soul Ledger")
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) addMemory(c *gin.Context) {
	var body memoryRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "Memory content is required")
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" || runeCount(content) > maxMessageLength {
		writeError(c, http.StatusBadRequest, "Memory must contain 1 to 20,000 characters")
		return
	}
	memory, created, err := s.saveMemory(c.Request.Context(), content)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not save memory")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, memory)
}

func (s *Server) editMemory(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Memory ID must be a positive integer")
		return
	}
	var body memoryRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "Memory content is required")
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" || runeCount(content) > maxMessageLength {
		writeError(c, http.StatusBadRequest, "Memory must contain 1 to 20,000 characters")
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE memories SET content=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, content, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not update memory")
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		writeError(c, http.StatusNotFound, "Memory not found")
		return
	}
	memory, err := s.memoryByID(c.Request.Context(), id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not read updated memory")
		return
	}
	c.JSON(http.StatusOK, memory)
}

func (s *Server) removeMemory(c *gin.Context) {
	id, ok := idParam(c)
	if !ok {
		writeError(c, http.StatusBadRequest, "Memory ID must be a positive integer")
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM memories WHERE id=?`, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not delete memory")
		return
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		writeError(c, http.StatusNotFound, "Memory not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) clearMemories(c *gin.Context) {
	if _, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM memories`); err != nil {
		writeError(c, http.StatusInternalServerError, "Could not clear Soul Ledger")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) models(c *gin.Context) {
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, s.ollama+"/api/tags", nil)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not create Ollama request")
		return
	}
	response, err := s.client.Do(request)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"connected": false, "models": []any{}, "error": "Ollama is unavailable. Start Ollama and refresh models."})
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"connected": false, "models": []any{}, "error": "Ollama could not list installed models."})
		return
	}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		writeError(c, http.StatusBadGateway, "Ollama returned an invalid model list")
		return
	}
	models := make([]string, 0, len(result.Models))
	for _, model := range result.Models {
		if strings.TrimSpace(model.Name) != "" {
			models = append(models, model.Name)
		}
	}
	sort.Strings(models)
	c.JSON(http.StatusOK, gin.H{"connected": true, "models": models})
}

func (s *Server) pull(c *gin.Context) {
	var body struct {
		Model string `json:"model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "Model name is required")
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" || runeCount(model) > 200 {
		writeError(c, http.StatusBadRequest, "Model name must contain 1 to 200 characters")
		return
	}
	payload, _ := json.Marshal(gin.H{"name": model, "stream": false})
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, s.ollama+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Could not create Ollama pull request")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		writeError(c, http.StatusBadGateway, "Ollama is unavailable; start it before pulling a model")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		writeError(c, http.StatusBadGateway, "Model pull failed: "+truncate(string(detail), 250))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "model": model, "status": "Model downloaded"})
}

func (s *Server) health(c *gin.Context) {
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, s.ollama+"/api/tags", nil)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "storage": "sqlite", "ollama_connected": false})
		return
	}
	response, err := s.client.Do(request)
	connected := err == nil
	if response != nil {
		connected = connected && response.StatusCode >= 200 && response.StatusCode < 300
		response.Body.Close()
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "storage": "sqlite", "ollama_connected": connected})
}

func (s *Server) saveMemory(ctx context.Context, content string) (Memory, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM memories WHERE lower(content)=lower(?) LIMIT 1`, content).Scan(&id)
	if err == nil {
		memory, err := s.memoryByID(ctx, id)
		return memory, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Memory{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO memories(content) VALUES(?)`, content)
	if err != nil {
		return Memory{}, false, err
	}
	id, err = result.LastInsertId()
	if err != nil {
		return Memory{}, false, err
	}
	memory, err := s.memoryByID(ctx, id)
	return memory, true, err
}

func (s *Server) relevant(ctx context.Context, prompt string) ([]string, error) {
	queryWords := wordset(prompt)
	if len(queryWords) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT content FROM memories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type match struct {
		content string
		score   int
	}
	matches := []match{}
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, err
		}
		score := 0
		for word := range wordset(content) {
			if queryWords[word] {
				score++
			}
		}
		if score > 0 {
			matches = append(matches, match{content: content, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	result := []string{}
	for i, item := range matches {
		if i == 5 {
			break
		}
		result = append(result, item.content)
	}
	return result, nil
}

func (s *Server) chatByID(ctx context.Context, id int64) (Chat, error) {
	var chat Chat
	err := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM chats WHERE id=?`, id).Scan(&chat.ID, &chat.Title, &chat.CreatedAt, &chat.UpdatedAt)
	return chat, err
}

func (s *Server) memoryByID(ctx context.Context, id int64) (Memory, error) {
	var memory Memory
	err := s.db.QueryRowContext(ctx, `SELECT id, content, created_at, updated_at FROM memories WHERE id=?`, id).Scan(&memory.ID, &memory.Content, &memory.CreatedAt, &memory.UpdatedAt)
	return memory, err
}

func memoryInstruction(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"remember that ", "remember ", "remember:", "save to memory:"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):]), true
		}
	}
	return "", false
}

func idParam(c *gin.Context) (int64, bool) {
	var id int64
	if _, err := fmt.Sscan(c.Param("id"), &id); err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func wordset(value string) map[string]bool {
	words := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(word) > 2 {
			words[word] = true
		}
	}
	return words
}

func truncate(value string, length int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > length {
		return string(runes[:length]) + "…"
	}
	return string(runes)
}

func runeCount(value string) int { return len([]rune(value)) }

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func writeError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

func cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:") {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Headers", "Content-Type")
			c.Header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
