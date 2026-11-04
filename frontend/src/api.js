const API_BASE = import.meta.env.VITE_API_BASE || "http://127.0.0.1:5000";

async function request(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...options.headers },
    });

    const raw = await response.text();
    let data;
    try { data = raw ? JSON.parse(raw) : {}; }
    catch { data = { response: raw }; }

    if (!response.ok) {
        throw new Error(data.error || data.message || `Request failed (${response.status})`);
    }
    return data;
}

function sendJSON(path, method, body) {
    return request(path, { method, body: JSON.stringify(body) });
}

export function getChats(query = "") {
    const suffix = query ? `?q=${encodeURIComponent(query)}` : "";
    return request(`/api/chats${suffix}`);
}

export function getChat(id) {
    return request(`/api/chats/${id}`);
}

export function createChat(title) {
    return sendJSON("/api/chats", "POST", { title });
}

export function renameChat(id, title) {
    return sendJSON(`/api/chats/${id}`, "PATCH", { title });
}

export function deleteChat(id) {
    return request(`/api/chats/${id}`, { method: "DELETE" });
}

export async function sendChat(message, model, conversationId, requestId) {
    return sendJSON("/api/chat", "POST", { message, model, conversation_id: conversationId, request_id: requestId });
}

export function getMemories() {
    return request("/api/memories");
}

export function addMemory(content) {
    return sendJSON("/api/memories", "POST", { content });
}

export function updateMemory(id, content) {
    return sendJSON(`/api/memories/${id}`, "PATCH", { content });
}

export function deleteMemory(id) {
    return request(`/api/memories/${id}`, { method: "DELETE" });
}

export function clearMemories() {
    return request("/api/memories", { method: "DELETE" });
}

export function getModels() {
    return request("/api/models");
}

export function getHealth() {
    return request("/api/health");
}

export async function pullModel(model) {
    return sendJSON("/api/pull", "POST", { model });
}

export function getReply(data) {
    if (typeof data === "string") return data;
    return data.reply ?? data.response ?? data.content ?? data.answer ?? "";
}