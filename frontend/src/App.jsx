import { useEffect, useEffectEvent, useMemo, useState } from "react";
import {
    Activity, Brain, Check, ChevronDown, CloudDownload, Copy, Cpu,
    Ghost, Menu, MessageSquare, Pencil, Plus, RefreshCw, Search, Send,
    Settings, Sparkles, Terminal, Trash2, X, Zap
} from "lucide-react";
import {
    addMemory, clearMemories, createChat, deleteChat, deleteMemory,
    getChat, getChats, getHealth, getMemories, getModels, getReply,
    pullModel, renameChat, sendChat, updateMemory
} from "./api";

const QUICK_PROMPTS = [
    "Investigate these phantom bugs.",
    "Explain this concept from the machine's memory.",
    "Help me build something from scratch.",
    "Explore an ancient programming language.",
];

export default function App() {
    const [chats, setChats] = useState([]);
    const [activeId, setActiveId] = useState(null);
    const [input, setInput] = useState("");
    const [model, setModel] = useState(() => localStorage.getItem("ghost.model") || "");
    const [models, setModels] = useState([]);
    const [modelMenu, setModelMenu] = useState(false);
    const [search, setSearch] = useState("");
    const [historyLoading, setHistoryLoading] = useState(true);
    const [loadingChatId, setLoadingChatId] = useState(null);
    const [busy, setBusy] = useState(false);
    const [creatingChat, setCreatingChat] = useState(false);
    const [pulling, setPulling] = useState(false);
    const [pullName, setPullName] = useState("");
    const [connected, setConnected] = useState(null);
    const [refreshing, setRefreshing] = useState(false);
    const [notice, setNotice] = useState("");
    const [sidebarOpen, setSidebarOpen] = useState(true);
    const [settingsOpen, setSettingsOpen] = useState(false);
    const [ledgerOpen, setLedgerOpen] = useState(false);
    const [memories, setMemories] = useState([]);
    const [memoryLoading, setMemoryLoading] = useState(false);
    const [memoryDraft, setMemoryDraft] = useState("");
    const [editingMemoryId, setEditingMemoryId] = useState(null);

    useEffect(() => {
        let cancelled = false;
        const timer = window.setTimeout(async () => {
            setHistoryLoading(true);
            try {
                const data = await getChats(search.trim());
                if (!cancelled) setChats(previous => mergeChatList(data, previous));
            } catch (error) {
                if (!cancelled) setNotice(`Could not load conversation history: ${error.message}`);
            } finally {
                if (!cancelled) setHistoryLoading(false);
            }
        }, search ? 180 : 0);
        return () => {
            cancelled = true;
            window.clearTimeout(timer);
        };
    }, [search]);

    useEffect(() => {
        let cancelled = false;
        const refreshRuntime = async () => {
            const [healthResult, modelsResult] = await Promise.allSettled([getHealth(), getModels()]);
            if (cancelled) return;
            const online = healthResult.status === "fulfilled" && healthResult.value.ollama_connected;
            setConnected(Boolean(online));
            if (modelsResult.status === "fulfilled") {
                const installed = modelsResult.value.models || [];
                setModels(installed);
                setModel(current => {
                    if (installed.includes(current)) return current;
                    const selected = installed[0] || "";
                    if (selected) localStorage.setItem("ghost.model", selected);
                    else localStorage.removeItem("ghost.model");
                    return selected;
                });
            } else {
                setModels([]);
            }
        };
        refreshRuntime();
        const interval = window.setInterval(refreshRuntime, 30000);
        return () => {
            cancelled = true;
            window.clearInterval(interval);
        };
    }, []);

    const createChatFromShortcut = useEffectEvent(newChat);

    useEffect(() => {
        function handleShortcut(event) {
            if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "n") {
                event.preventDefault();
                createChatFromShortcut();
            }
        }
        window.addEventListener("keydown", handleShortcut);
        return () => window.removeEventListener("keydown", handleShortcut);
    }, []);

    const activeChat = chats.find(chat => chat.id === activeId);
    const groupedChats = useMemo(() => groupChats(chats), [chats]);

    async function refreshHistory() {
        try {
            const data = await getChats(search.trim());
            setChats(previous => mergeChatList(data, previous));
        } catch (error) {
            setNotice(`Could not refresh conversation history: ${error.message}`);
        }
    }

    async function newChat() {
        if (creatingChat || busy) return;
        setCreatingChat(true);
        setNotice("");
        try {
            const chat = await createChat("New conversation");
            setChats(previous => [chat, ...previous.filter(item => item.id !== chat.id)]);
            setActiveId(chat.id);
            setInput("");
            setSidebarOpen(true);
        } catch (error) {
            setNotice(`Could not start a conversation: ${error.message}`);
        } finally {
            setCreatingChat(false);
        }
    }

    async function openChat(id) {
        setActiveId(id);
        setNotice("");
        setSidebarOpen(false);
        setLoadingChatId(id);
        try {
            const data = await getChat(id);
            setChats(previous => previous.map(chat => chat.id === id
                ? { ...data.chat, messages: data.messages.map(message => ({ role: message.role, text: message.content })) }
                : chat));
        } catch (error) {
            setNotice(`Could not load conversation: ${error.message}`);
        } finally {
            setLoadingChatId(current => current === id ? null : current);
        }
    }

    async function submit(event) {
        event?.preventDefault();
        const prompt = input.trim();
        if (!prompt || busy) return;
        if (!model) {
            setNotice(connected ? "Install or pull an Ollama model before sending a message." : "Start Ollama, then refresh the model list to chat.");
            return;
        }

        setInput("");
        setNotice("");
        setBusy(true);
        let chatId = activeId;
        let optimisticMessage = null;
        try {
            if (chatId === null) {
                const created = await createChat("New conversation");
                chatId = created.id;
                setActiveId(chatId);
                setChats(previous => [created, ...previous]);
            }
            optimisticMessage = { role: "user", text: prompt };
            setChats(previous => previous.map(chat => chat.id === chatId
                ? { ...chat, messages: [...(chat.messages || []), optimisticMessage] }
                : chat));

            const result = await sendChat(prompt, model, chatId, crypto.randomUUID());
            const reply = getReply(result);
            setChats(previous => {
                const updated = previous.map(chat => chat.id === chatId
                    ? {
                        ...chat,
                        title: chat.title === "New conversation" ? promptTitle(prompt) : chat.title,
                        messages: [...(chat.messages || []), { role: "assistant", text: reply }],
                    }
                    : chat);
                return [...updated].sort((a, b) => new Date(b.updated_at || 0) - new Date(a.updated_at || 0));
            });
            if (result.memory_saved) setNotice("Memory saved to the Soul Ledger.");
            refreshHistory();
        } catch (error) {
            setChats(previous => previous.map(chat => chat.id === chatId
                ? { ...chat, messages: (chat.messages || []).filter(message => message !== optimisticMessage) }
                : chat));
            setNotice(`Ghost could not complete that reply: ${error.message}`);
        } finally {
            setBusy(false);
        }
    }

    async function handleRename(chat) {
        const title = window.prompt("Rename conversation", chat.title);
        if (title === null || !title.trim()) return;
        try {
            const updated = await renameChat(chat.id, title.trim());
            setChats(previous => previous.map(item => item.id === chat.id ? { ...item, ...updated } : item));
        } catch (error) {
            setNotice(`Could not rename conversation: ${error.message}`);
        }
    }

    async function handleDeleteChat(chat) {
        if (!window.confirm(`Delete “${chat.title}” and its messages? This cannot be undone.`)) return;
        try {
            await deleteChat(chat.id);
            setChats(previous => previous.filter(item => item.id !== chat.id));
            if (activeId === chat.id) setActiveId(null);
        } catch (error) {
            setNotice(`Could not delete conversation: ${error.message}`);
        }
    }

    async function openLedger() {
        setLedgerOpen(true);
        setMemoryLoading(true);
        try {
            setMemories(await getMemories());
        } catch (error) {
            setNotice(`Could not open the Soul Ledger: ${error.message}`);
        } finally {
            setMemoryLoading(false);
        }
    }

    async function saveMemory(event) {
        event.preventDefault();
        const content = memoryDraft.trim();
        if (!content) return;
        try {
            if (editingMemoryId) {
                const updated = await updateMemory(editingMemoryId, content);
                setMemories(previous => previous.map(memory => memory.id === updated.id ? updated : memory));
                setNotice("Soul Ledger entry updated.");
            } else {
                const added = await addMemory(content);
                setMemories(previous => [added, ...previous.filter(memory => memory.id !== added.id)]);
                setNotice("Soul Ledger entry saved.");
            }
            setMemoryDraft("");
            setEditingMemoryId(null);
        } catch (error) {
            setNotice(`Could not save memory: ${error.message}`);
        }
    }

    async function handleDeleteMemory(memory) {
        if (!window.confirm("Remove this entry from the Soul Ledger?")) return;
        try {
            await deleteMemory(memory.id);
            setMemories(previous => previous.filter(item => item.id !== memory.id));
        } catch (error) {
            setNotice(`Could not delete memory: ${error.message}`);
        }
    }

    async function handleClearMemories() {
        if (!memories.length || !window.confirm("Clear every Soul Ledger entry? This cannot be undone.")) return;
        try {
            await clearMemories();
            setMemories([]);
        } catch (error) {
            setNotice(`Could not clear the Soul Ledger: ${error.message}`);
        }
    }

    async function refreshModels() {
        setRefreshing(true);
        try {
            const [health, result] = await Promise.all([getHealth(), getModels()]);
            setConnected(Boolean(health.ollama_connected));
            setModels(result.models || []);
            setNotice(`Found ${result.models?.length || 0} installed Ollama model${result.models?.length === 1 ? "" : "s"}.`);
        } catch (error) {
            setConnected(false);
            setModels([]);
            setNotice(error.message || "Ollama is unavailable. Start it and try again.");
        } finally {
            setRefreshing(false);
        }
    }

    async function handlePull(event) {
        event.preventDefault();
        const requestedModel = pullName.trim();
        if (!requestedModel || pulling) return;
        setPulling(true);
        setNotice(`Downloading ${requestedModel} through Ollama. This can take a while.`);
        try {
            const result = await pullModel(requestedModel);
            setNotice(`${result.model} is ready.`);
            setPullName("");
            await refreshModels();
            setModel(result.model);
            localStorage.setItem("ghost.model", result.model);
        } catch (error) {
            setNotice(`Model download failed: ${error.message}`);
        } finally {
            setPulling(false);
        }
    }

    async function copyText(text) {
        try {
            await navigator.clipboard.writeText(text);
            setNotice("Copied to clipboard.");
        } catch {
            setNotice("Clipboard access is unavailable in this browser.");
        }
    }

    return (
        <main className="app-shell">
            <aside className={`sidebar ${sidebarOpen ? "" : "sidebar-hidden"}`}>
                <div className="window-dots"><i /><i /><i /></div>
                <div className="brand">
                    <div className="brand-mark"><Ghost size={25} strokeWidth={2.6} /></div>
                    <div><div className="brand-name">Ghost<span>.</span></div><div className="brand-tag">Local AI. Your Control.</div></div>
                </div>

                <button className="new-chat" onClick={newChat} disabled={creatingChat || busy}>
                    <Plus size={17} /><span>{creatingChat ? "Opening..." : "New Chat"}</span><kbd>Ctrl + N</kbd>
                </button>
                <div className="search-wrap"><Search size={16} /><input value={search} onChange={event => setSearch(event.target.value)} placeholder="Search conversations..." /></div>

                <div className="section-label">WORKSPACE <span>●</span></div>
                <div className="chat-list">
                    {historyLoading && !chats.length && <div className="empty-search">Loading conversations...</div>}
                    {groupedChats.map(([label, items]) => (
                        <section className="chat-group" key={label}>
                            <h2>{label}</h2>
                            {items.map(chat => (
                                <div className={`chat-row ${chat.id === activeId ? "active" : ""}`} key={chat.id}>
                                    <button className="chat-item" onClick={() => openChat(chat.id)} title={chat.title}>
                                        <MessageSquare size={15} /><span>{chat.title}</span>
                                        <small>{loadingChatId === chat.id ? "…" : formatChatTime(chat.updated_at)}</small>
                                    </button>
                                    <div className="chat-item-actions">
                                        <button title="Rename conversation" aria-label={`Rename ${chat.title}`} onClick={() => handleRename(chat)}><Pencil size={13} /></button>
                                        <button title="Delete conversation" aria-label={`Delete ${chat.title}`} onClick={() => handleDeleteChat(chat)}><Trash2 size={13} /></button>
                                    </div>
                                </div>
                            ))}
                        </section>
                    ))}
                    {!historyLoading && !chats.length && <div className="empty-search">{search ? "No matching conversations" : "No conversations yet"}</div>}
                </div>

                <div className="sidebar-tools">
                    <button onClick={openLedger}><Brain size={17} /><span>Soul Ledger</span><ChevronDown size={15} className="tool-chevron" /></button>
                    <button onClick={() => setSettingsOpen(value => !value)}><Settings size={17} /><span>Ollama settings</span><ChevronDown size={15} className="tool-chevron" /></button>
                    {settingsOpen && <div className="settings-panel">
                        <form onSubmit={handlePull}>
                            <label htmlFor="pull-model">Pull a model</label>
                            <div className="pull-model-form"><input id="pull-model" value={pullName} onChange={event => setPullName(event.target.value)} placeholder="e.g. gemma2:latest" /><button title="Download model" disabled={!pullName.trim() || pulling}>{pulling ? <Activity size={15} className="spin" /> : <CloudDownload size={15} />}</button></div>
                        </form>
                        <code>{import.meta.env.VITE_API_BASE || "http://127.0.0.1:5000"}</code>
                        <p>Downloads are handled by your local Ollama service.</p>
                    </div>}
                </div>

                <div className={`connection ${connected === false ? "disconnected" : ""}`}>
                    <span className={`online-dot ${connected === null ? "checking" : connected ? "" : "offline"}`} />
                    <div><strong>{connected === null ? "Checking Ollama" : connected ? "Ollama connected" : "Ollama disconnected"}</strong><small>{connected === false ? "Start Ollama to use local models" : `${models.length} installed model${models.length === 1 ? "" : "s"}`}</small></div>
                    <button className="icon-button connection-refresh" title="Refresh connection and models" onClick={refreshModels} disabled={refreshing}>{refreshing ? <Activity size={16} className="spin" /> : <RefreshCw size={15} />}</button>
                </div>
            </aside>

            <section className="main-panel">
                <header className="topbar">
                    <div className="topbar-left">
                        <button className="icon-button mobile-menu" title="Toggle sidebar" onClick={() => setSidebarOpen(value => !value)}><Menu size={18} /></button>
                        <div className="terminal-chip"><Terminal size={16} /><span>ghost</span><b>/</b><span>{activeChat?.title || "new-session"}</span></div>
                    </div>
                    <div className="topbar-actions">
                        <div className="model-picker-wrap">
                            <button className="model-picker" onClick={() => setModelMenu(value => !value)} disabled={!models.length}>
                                <span className={`online-dot ${connected ? "" : "offline"}`} />{model || (connected ? "No models installed" : "Ollama offline")}<ChevronDown size={14} />
                            </button>
                            {modelMenu && <div className="model-menu">
                                {models.map(item => <button key={item} onClick={() => { setModel(item); localStorage.setItem("ghost.model", item); setModelMenu(false); }}><Cpu size={14} />{item}{item === model && <Check size={14} />}</button>)}
                                {!models.length && <p>{connected ? "Pull a model to begin." : "Start Ollama, then refresh."}</p>}
                            </div>}
                        </div>
                        <button className="icon-button" title="Open Soul Ledger" onClick={openLedger}><Brain size={17} /></button>
                        <button className="icon-button" title="Refresh models" onClick={refreshModels} disabled={refreshing}>{refreshing ? <Activity size={16} className="spin" /> : <RefreshCw size={15} />}</button>
                    </div>
                </header>

                <div className="conversation">
                    {loadingChatId === activeId && activeId !== null ? <div className="conversation-state"><span className="pulse" /> Opening conversation...</div> : activeChat?.messages?.length ? (
                        <div className="messages">
                            {activeChat.messages.map((message, index) => (
                                <article className={`message ${message.role}`} key={`${activeId}-${index}`}>
                                    <div className="avatar">{message.role === "assistant" ? <Ghost size={18} /> : <span>U</span>}</div>
                                    <div className="message-content">
                                        <div className="message-meta"><strong>{message.role === "assistant" ? "GHOST" : "YOU"}</strong><span>{message.role === "assistant" ? "LOCAL INFERENCE" : "PROMPT"}</span></div>
                                        <pre>{message.text}</pre>
                                        {message.role === "assistant" && <div className="message-actions"><button onClick={() => copyText(message.text)}><Copy size={13} /> Copy</button></div>}
                                    </div>
                                </article>
                            ))}
                            {busy && <div className="thinking"><span className="pulse" /> Ghost is investigating<span className="cursor">_</span></div>}
                            <div className="end-marker">— END OF RESPONSE —</div>
                        </div>
                    ) : (
                        <div className="welcome">
                            <div className="welcome-icon"><Ghost size={38} /></div>
                            <div className="eyebrow"><span /> {connected ? "SYSTEM READY" : connected === false ? "LOCAL ENGINE OFFLINE" : "CHECKING LOCAL ENGINE"}</div>
                            <h1>Hey, I'm <em>Ghost</em>.</h1>
                            <p>Your digital spirit at home in this machine. Private by design, powered by open models.</p>
                            <div className="welcome-command"><span>➜</span> {connected === false ? "Start Ollama to wake the machine." : "What are we building today?"}<i>_</i></div>
                            <div className="quick-prompts">
                                {QUICK_PROMPTS.map(prompt => <button key={prompt} onClick={() => setInput(prompt)}><Sparkles size={13} />{prompt}</button>)}
                            </div>
                        </div>
                    )}
                </div>

                {notice && <div className="notice"><span>{notice}</span><button title="Dismiss message" onClick={() => setNotice("")}><X size={14} /></button></div>}
                <form className="composer" onSubmit={submit}>
                    <div className="composer-top"><span><span className={`online-dot ${connected ? "" : "offline"}`} /> INPUT CHANNEL</span><span>ENTER TO SEND · SHIFT + ENTER FOR NEW LINE</span></div>
                    <div className="composer-row">
                        <span className="prompt-symbol">❯</span>
                        <textarea value={input} onChange={event => setInput(event.target.value)} onKeyDown={event => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); submit(); } }} placeholder="Ask Ghost anything..." rows={1} />
                        <button className="send-button" type="submit" disabled={!input.trim() || busy}><Send size={17} /></button>
                    </div>
                    <div className="composer-bottom"><span><Zap size={12} /> {busy ? "GHOST IS ACTIVE" : connected ? "RUNNING LOCALLY" : "LOCAL ENGINE UNAVAILABLE"}</span><span>{model || "No model selected"}</span></div>
                </form>
                <footer className="footer"><span>GHOST AI <b>v0.1.0</b></span><span>OPEN-WEIGHT · LOCAL-FIRST · YOUR DATA STAYS YOURS</span></footer>
            </section>

            {ledgerOpen && <div className="overlay" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) setLedgerOpen(false); }}>
                <section className="ledger" role="dialog" aria-modal="true" aria-labelledby="ledger-title">
                    <header className="ledger-header"><div><div className="eyebrow"><span /> USER CONTROLLED MEMORY</div><h2 id="ledger-title"><Brain size={22} /> Soul Ledger</h2></div><button className="icon-button" title="Close Soul Ledger" onClick={() => setLedgerOpen(false)}><X size={18} /></button></header>
                    <p className="ledger-description">Only saved entries appear here. Ghost may use relevant entries across conversations.</p>
                    <form className="memory-form" onSubmit={saveMemory}>
                        <label htmlFor="memory-content">{editingMemoryId ? "Edit entry" : "Add a memory"}</label>
                        <textarea id="memory-content" value={memoryDraft} onChange={event => setMemoryDraft(event.target.value)} maxLength={20000} placeholder="For example: I prefer concise answers." rows={3} />
                        <div className="memory-form-actions"><button className="text-button" type="button" onClick={() => { setMemoryDraft(""); setEditingMemoryId(null); }}>Clear</button><button className="primary-button" type="submit" disabled={!memoryDraft.trim()}>{editingMemoryId ? "Save changes" : "Add to ledger"}</button></div>
                    </form>
                    <div className="ledger-list-header"><h3>Recorded entries <span>{memories.length}</span></h3><button className="text-button danger-text" onClick={handleClearMemories} disabled={!memories.length}>Clear all</button></div>
                    <div className="ledger-list">
                        {memoryLoading ? <div className="ledger-empty">Reading the Soul Ledger...</div> : memories.map(memory => (
                            <article className="memory-entry" key={memory.id}><div><p>{memory.content}</p><time>{formatDate(memory.created_at)}</time></div><div className="entry-actions"><button className="icon-button" title="Edit memory" onClick={() => { setMemoryDraft(memory.content); setEditingMemoryId(memory.id); }}><Pencil size={15} /></button><button className="icon-button" title="Delete memory" onClick={() => handleDeleteMemory(memory)}><Trash2 size={15} /></button></div></article>
                        ))}
                        {!memoryLoading && !memories.length && <div className="ledger-empty"><Ghost size={25} /><strong>Nothing recorded yet</strong><span>Add an entry here, or tell Ghost what to remember in a chat.</span></div>}
                    </div>
                </section>
            </div>}
        </main>
    );
}

function promptTitle(prompt) {
    const characters = Array.from(prompt.trim());
    return characters.length > 54 ? `${characters.slice(0, 54).join("").trimEnd()}…` : prompt.trim();
}

function mergeChatList(chatList, previousChats) {
    const previousById = new Map(previousChats.map(chat => [chat.id, chat]));
    return chatList.map(chat => ({ ...previousById.get(chat.id), ...chat, messages: previousById.get(chat.id)?.messages || [] }));
}

function groupChats(chats) {
    const now = new Date();
    const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const startOfYesterday = new Date(startOfToday);
    startOfYesterday.setDate(startOfToday.getDate() - 1);
    const startOfWeek = new Date(startOfToday);
    startOfWeek.setDate(startOfToday.getDate() - 7);
    const groups = new Map([["Today", []], ["Yesterday", []], ["Previous 7 days", []], ["Older", []]]);
    for (const chat of chats) {
        const updated = new Date(chat.updated_at);
        const label = updated >= startOfToday ? "Today" : updated >= startOfYesterday ? "Yesterday" : updated >= startOfWeek ? "Previous 7 days" : "Older";
        groups.get(label).push(chat);
    }
    return [...groups].filter(([, items]) => items.length);
}

function formatChatTime(value) {
    if (!value) return "recently";
    const minutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60000));
    if (minutes < 1) return "now";
    if (minutes < 60) return `${minutes}m`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h`;
    return `${Math.floor(hours / 24)}d`;
}

function formatDate(value) {
    if (!value) return "Date unavailable";
    return new Intl.DateTimeFormat(undefined, { dateStyle: "medium" }).format(new Date(value));
}