# SpiritAI — Ghost in the Machine

SpiritAI is a local-first AI assistant built around Ollama and open-weight language models. Ghost lives in the app on your machine, keeps conversations and user-approved memories in a local SQLite database, and can be switched between models installed in your Ollama instance.

## What it does

- Chats with locally hosted Ollama models; no proprietary cloud LLM API is integrated.
- Stores conversation titles and messages in SQLite and restores them after restart.
- Organizes searchable conversation history by date, with rename and delete controls.
- Provides the Soul Ledger: inspect, add, edit, and remove user-controlled long-term memories.
- Recognizes explicit prompts such as “Remember that I prefer concise answers.” Ghost confirms the save only after the backend stores it.
- Includes real Ollama health/model discovery and a model pull control.
- Uses a Ghost persona that is friendly and lightly atmospheric while staying clear about uncertainty and system access.

## Architecture

```text
React + Vite frontend
        | JSON over local HTTP
        v
Go + Gin API :5000  ----> Ollama :11434
        |
        v
SQLite (chats, messages, Soul Ledger)
```

The supported application backend is `backend/main.go` (Go/Gin). `backend/app.py` is a legacy, stateless Flask prototype and is not used by the current frontend; use the Go server for persistence, memory, and dynamic model support.

## Requirements

- Go 1.22 or newer
- Node.js and npm compatible with the versions in `frontend/package-lock.json`
- Ollama installed locally
- At least one Ollama model downloaded before chatting

The project uses local inference, but a fully offline first-run has not been verified. Installing packages and downloading models require network access. Once dependencies and a model are present, the app’s AI requests target the local Ollama endpoint.

## Install Ollama and a model

Install Ollama using the instructions for your operating system at [ollama.com/download](https://ollama.com/download), then start the Ollama service. Pull a model from a terminal, for example:

```bash
ollama pull gemma2:latest
```

Other supported examples include `qwen2.5:0.5b`, `llama3.1:latest`, and `mistral:latest`; a model appears in SpiritAI only if Ollama reports it as installed. Model availability and hardware requirements vary. The frontend can also request a model pull from the Ollama settings panel.

## Run the application

Start the Go API in one terminal:

```bash
cd backend
go run .
```

Install and start the frontend in another terminal:

```bash
cd frontend
npm install
npm run dev
```

Open the local URL printed by Vite, usually `http://localhost:5173`.

## Configuration

The Go backend reads these optional environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `GHOST_DB` | `ghost.db` | SQLite database path; relative paths resolve from the backend working directory. |
| `GHOST_LISTEN` | `:5000` | API listen address. |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Local Ollama base URL. |
| `GHOST_MODEL` | `qwen2.5:0.5b` | Backend fallback model when a chat request omits a model. |

The frontend can use `frontend/.env` to override the API base:

```env
VITE_API_BASE=http://127.0.0.1:5000
```

The model selector uses the installed model list from Ollama; it does not treat the fallback model as installed. If no model is installed, use Ollama or the in-app pull control first.

## API

All routes are served by the Go API. JSON errors have an `error` field.

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/api/health` | API/storage status and `ollama_connected` state. |
| `GET` | `/api/chats?q=...` | Conversations ordered by most recently updated; optional search across titles and message content. |
| `POST` | `/api/chats` | Create a conversation with optional `{ "title": "..." }`. |
| `GET` | `/api/chats/:id` | Get a conversation and its ordered messages. |
| `PATCH` | `/api/chats/:id` | Rename with `{ "title": "..." }`. |
| `DELETE` | `/api/chats/:id` | Delete a conversation and its messages. |
| `POST` | `/api/chat` | Run local inference and atomically persist the user/assistant turn. Accepts `{ "message": "...", "model": "...", "conversation_id": 1, "request_id": "..." }`; the conversation ID and request ID are optional. |
| `GET` | `/api/memories` | List Soul Ledger entries. |
| `POST` | `/api/memories` | Add `{ "content": "..." }`; exact case-insensitive duplicates return the existing entry. |
| `PATCH` | `/api/memories/:id` | Edit `{ "content": "..." }`. |
| `DELETE` | `/api/memories/:id` | Delete one memory. |
| `DELETE` | `/api/memories` | Clear all memories. |
| `GET` | `/api/models` | Installed model names and Ollama connection state. Returns `503` if Ollama cannot list models. |
| `POST` | `/api/pull` | Pull `{ "model": "..." }` from Ollama. |

The Go server accepts browser requests from localhost origins for development. Keep the API bound to a trusted local interface if you change its listen address; authentication is not implemented.

## Local storage and privacy

SQLite stores chats, messages, and the Soul Ledger in `GHOST_DB` (default: `backend/ghost.db` when started from `backend/`). Deleting a conversation deletes its messages. Saving memories is explicit: the app does not automatically retain ordinary conversation text. Relevant saved memory entries may be sent to the local Ollama model as context for a response.

Data is local but is not encrypted by this application. Do not place secrets in chats or memories unless your device and database are protected separately. The app does not make a verified claim of complete offline operation.

## Screenshots

No project screenshots are checked into this repository yet. Add screenshots here when they are available.

## Development and verification

Run backend tests and formatting:

```bash
cd backend
gofmt -w main.go main_test.go
go test ./...
```

Build and lint the frontend:

```bash
cd frontend
npm run build
npm run lint
```

The Go tests use a local fake Ollama HTTP server to verify API contracts and failure cases without requiring an installed model. Actual responses, real model discovery, pull progress, latency, and offline behavior with a downloaded model still need to be verified against a running Ollama installation. The existing pull endpoint is synchronous and reports completion rather than streaming detailed progress.

## Troubleshooting

- **Ollama disconnected:** Start the Ollama service, verify `OLLAMA_URL`, then use the refresh button in SpiritAI.
- **No models installed:** Run `ollama pull gemma2:latest` or use the pull form in Ollama settings.
- **Conversation or memory request fails:** Confirm the Go server is running and `VITE_API_BASE` points to it.
- **Browser cannot reach the API:** Use the Vite localhost origin and confirm the API is listening on the configured local address.
- **Database appears in a different folder:** `GHOST_DB` relative paths use the backend process working directory; set an absolute path for a fixed location.
- **Large model download:** Pulling is synchronous and can take time depending on model size and network speed.

## Future improvements

- Streaming token generation and progress reporting for model pulls.
- Configurable, safer API origin policy and optional local authentication for non-loopback deployments.
- Browser-based workflow tests and checks against a real Ollama service.
- Optional clearly labelled educational simulations in Séance Mode.
- Packaged screenshots and release artifacts.

## Contributing

1. Fork the repository and create a focused branch.
2. Keep inference local-first and avoid proprietary cloud LLM dependencies.
3. Run `go test ./...`, `gofmt`, `npm run build`, and `npm run lint` for relevant changes.
4. Include tests for API behavior and document any dependency needed to verify a feature.
5. Open a pull request describing behavior, privacy impact, and test results.

## License

No license file is present in this repository. No license is asserted here; add a license file before redistributing this project under an open-source license.
