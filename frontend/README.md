# SpiritAI Frontend

React and Vite interface for the SpiritAI local assistant. It talks to the Go/Gin API; see the repository [README](../README.md) for installation, configuration, local Ollama setup, API routes, and privacy details.

From this directory:

```bash
npm install
npm run dev
npm run build
npm run lint
```

Set `VITE_API_BASE` in a local `.env` file when the Go API is not at `http://127.0.0.1:5000`.
