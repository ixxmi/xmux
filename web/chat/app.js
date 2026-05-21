(function () {
  const SESSION_KEY = "cloud-terminal-chat-session";
  const ACTIVE_AGENT_KEY = "cloud-terminal-chat-agent";
  const messageKey = (id) => `cloud-terminal-chat-messages:${id}`;
  const appPath = window.XMuxPath?.path || ((path) => path);
  const websocketURL = window.XMuxPath?.websocketURL || ((path) => {
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${protocol}//${window.location.host}${path}`;
  });

  const authView = document.getElementById("authView");
  const chatView = document.getElementById("chatView");
  const authForm = document.getElementById("authForm");
  const loginModeButton = document.getElementById("loginModeButton");
  const registerModeButton = document.getElementById("registerModeButton");
  const usernameLabel = document.getElementById("usernameLabel");
  const usernameInput = document.getElementById("usernameInput");
  const passwordLabel = document.getElementById("passwordLabel");
  const passwordInput = document.getElementById("passwordInput");
  const authMessage = document.getElementById("authMessage");
  const connectionState = document.getElementById("connectionState");
  const sessionButton = document.getElementById("sessionButton");
  const workspacePath = document.getElementById("workspacePath");
  const agentSelect = document.getElementById("agentSelect");
  const edgeName = document.getElementById("edgeName");
  const sessionSummary = document.getElementById("sessionSummary");
  const messageList = document.getElementById("messageList");
  const composer = document.getElementById("composer");
  const messageInput = document.getElementById("messageInput");
  const sendButton = document.getElementById("sendButton");
  const stopButton = document.getElementById("stopButton");
  const newButton = document.getElementById("newButton");
  const composerMeta = document.getElementById("composerMeta");

  let state = null;
  let socket = null;
  let sessionID = localStorage.getItem(SESSION_KEY) || "";
  let selectedAgent = normalizeAgentID(localStorage.getItem(ACTIVE_AGENT_KEY) || "codex");
  let authMode = "login";
  let socketSeq = 0;
  let reconnectTimer = 0;
  let reconnectAttempts = 0;
  let manualClose = false;
  let messages = [];
  let activeAssistantID = "";

  bootstrap();

  window.addEventListener("error", (event) => {
    setAuthBusy(false);
    showAuthError(event.message || "Page script error.");
    addSystem(event.message || "Page script error.");
  });

  window.addEventListener("unhandledrejection", (event) => {
    setAuthBusy(false);
    const message = event.reason?.message || String(event.reason || "Request failed.");
    showAuthError(message);
    addSystem(message);
  });

  authForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    if (!username || !password) {
      showAuthError("Account and password are required.");
      return;
    }
    setAuthBusy(true);
    authMessage.textContent = "Verifying...";
    try {
      const result = authMode === "register" ? await registerAccount(username, password) : await loginAccount(username, password);
      state = result.state || result;
      passwordInput.value = "";
      openChat();
    } catch (error) {
      showAuthError(error.message || "Login rejected.");
    } finally {
      setAuthBusy(false);
    }
  });

  loginModeButton.addEventListener("click", () => setAuthMode("login"));
  registerModeButton.addEventListener("click", () => setAuthMode("register"));

  composer.addEventListener("submit", (event) => {
    event.preventDefault();
    sendMessage();
  });

  messageInput.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      sendMessage();
    }
  });

  messageInput.addEventListener("input", () => {
    messageInput.style.height = "auto";
    messageInput.style.height = `${Math.min(messageInput.scrollHeight, 138)}px`;
    updateComposerMeta();
  });

  agentSelect.addEventListener("change", () => {
    selectedAgent = normalizeAgentID(agentSelect.value);
    localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
  });

  stopButton.addEventListener("click", () => {
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "stop" }));
      addSystem(`正在停止当前 ${agentLabel(selectedAgent)} 会话。`);
      setConnection("Stopping");
    }
  });

  newButton.addEventListener("click", () => {
    activeAssistantID = "";
    sessionID = "";
    localStorage.removeItem(SESSION_KEY);
    messages = [];
    renderMessages();
    manualClose = true;
    if (socket) {
      socket.close();
    }
    manualClose = false;
    connect(true);
  });

  sessionButton.addEventListener("click", () => {
    if (!state || !Array.isArray(state.sessions) || state.sessions.length === 0) {
      return;
    }
    const running = state.sessions.find((item) => item.running);
    const latest = running || state.sessions[0];
    if (latest && latest.id !== sessionID) {
      sessionID = latest.id;
      localStorage.setItem(SESSION_KEY, sessionID);
      connect(true);
    }
  });

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && (!socket || socket.readyState === WebSocket.CLOSED)) {
      connect(true);
    }
  });

  async function bootstrap() {
    try {
      state = await fetchJSON("/cloud-terminal-api/workbench/state", null, 5000);
      openChat();
    } catch {
      authView.hidden = false;
      chatView.hidden = true;
      usernameInput.focus();
    }
  }

  function setAuthMode(mode) {
    authMode = mode;
    loginModeButton.classList.toggle("active", mode === "login");
    registerModeButton.classList.toggle("active", mode === "register");
    usernameLabel.hidden = false;
    usernameInput.hidden = false;
    passwordLabel.hidden = false;
    passwordInput.hidden = false;
    authForm.querySelector("button[type='submit']").textContent = mode === "register" ? "创建账号" : "继续";
    authMessage.textContent = "";
    usernameInput.focus();
  }

  async function loginAccount(username, password) {
    return fetchJSON("/cloud-terminal-api/accounts/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password })
    }, 8000);
  }

  async function registerAccount(username, password) {
    return fetchJSON("/cloud-terminal-api/accounts/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password })
    }, 8000);
  }

  function openChat() {
    authView.hidden = true;
    chatView.hidden = false;
    ensureSelectedAgent();
    renderAgentSelect();
    updateContext();
    if (!sessionID && state?.sessions?.length > 0) {
      const running = state.sessions.find((item) => item.running);
      const latest = running || state.sessions[0];
      sessionID = latest.id;
      localStorage.setItem(SESSION_KEY, sessionID);
    }
    connect(true);
    messageInput.focus();
  }

  function connect(resetMessages) {
    clearTimeout(reconnectTimer);
    const seq = ++socketSeq;
    if (socket) {
      manualClose = true;
      socket.close();
    }

    setConnection("Connecting");
    setComposerEnabled(false);
    const params = new URLSearchParams({ rows: "32", cols: "120" });
    if (sessionID) {
      params.set("session_id", sessionID);
    } else {
      params.set("agent", selectedAgent);
    }
    socket = new WebSocket(websocketURL(`/cloud-terminal-api/ws/workbench?${params.toString()}`));
    manualClose = false;

    socket.addEventListener("open", () => {
      if (seq !== socketSeq) {
        return;
      }
      reconnectAttempts = 0;
      setConnection("Connected");
    });

    socket.addEventListener("message", (event) => {
      if (seq !== socketSeq) {
        return;
      }
      handleSocketMessage(JSON.parse(event.data), resetMessages);
    });

    socket.addEventListener("close", (event) => {
      if (seq !== socketSeq) {
        return;
      }
      setComposerEnabled(false);
      setConnection(event.code ? `Detached (${event.code})` : "Detached");
      if (manualClose) {
        return;
      }
      if (event.code === 1008 || event.code === 1002 || event.code === 1003 || event.code === 1011) {
        addSystem(`WebSocket 已停止：${event.reason || event.code}`);
        return;
      }
      if (reconnectAttempts < 5) {
        reconnectAttempts += 1;
        reconnectTimer = window.setTimeout(() => connect(false), Math.min(1000 + reconnectAttempts * 700, 6000));
      } else {
        addSystem("WebSocket 重连已停止，点击 New 或刷新页面可重新连接。");
      }
    });

    socket.addEventListener("error", () => {
      if (seq !== socketSeq) {
        return;
      }
      setConnection("Connection error");
      addSystem("WebSocket 连接失败，请检查账号登录、Origin 白名单或反向代理 Upgrade 配置。");
    });
  }

  function handleSocketMessage(msg, resetMessages) {
    switch (msg.type) {
      case "ready":
        sessionID = msg.session_id;
        selectedAgent = normalizeAgentID(msg.agent || selectedAgent);
        localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
        localStorage.setItem(SESSION_KEY, sessionID);
        renderAgentSelect();
        updateContext(msg);
        loadMessages();
        setComposerEnabled(msg.running !== false);
        setConnection(msg.running === false ? "Finished" : "Attached");
        break;
      case "replay":
        handleReplay(msg.data || "");
        break;
      case "output":
        appendAssistantOutput(msg.data || "");
        break;
      case "exit":
        activeAssistantID = "";
        setConnection("Finished");
        setComposerEnabled(false);
        addSystem(`${agentLabel(selectedAgent)} 会话已结束，退出码 ${msg.exit_code ?? 0}。`);
        if (msg.error) {
          addSystem(msg.error);
        }
        break;
      case "error":
        activeAssistantID = "";
        addSystem(msg.error || `${agentLabel(selectedAgent)} 返回错误。`);
        setConnection("Error");
        break;
      case "pong":
        break;
    }
  }

  function sendMessage() {
    const text = messageInput.value.trim();
    if (!text || !socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    addMessage("user", text, "Request");
    messageInput.value = "";
    messageInput.style.height = "auto";
    updateComposerMeta();
    activeAssistantID = addMessage("assistant", "", agentLabel(selectedAgent));
    socket.send(JSON.stringify({ type: "input", data: text.replace(/\n/g, "\r") + "\r" }));
  }

  function handleReplay(raw) {
    if (!raw || messages.some((item) => item.role === "user" || item.role === "assistant")) {
      return;
    }
    const text = normalizeTerminalText(raw);
    if (text) {
      addMessage("assistant", text.slice(-12000), "会话回放");
    }
  }

  function appendAssistantOutput(raw) {
    const text = normalizeTerminalText(raw);
    if (!text) {
      return;
    }
    if (!activeAssistantID) {
      activeAssistantID = addMessage("assistant", "", agentLabel(selectedAgent));
    }
    const item = messages.find((message) => message.id === activeAssistantID);
    if (!item) {
      activeAssistantID = addMessage("assistant", text, agentLabel(selectedAgent));
      return;
    }
    item.text = trimMessage((item.text || "") + text);
    updateMessageElement(item);
    persistMessages();
    scrollToBottom();
  }

  function addSystem(text) {
    if (!chatView.hidden) {
      addMessage("system", text, "Status", false);
    }
  }

  function addMessage(role, text, title, persist = true) {
    const item = {
      id: `msg-${Date.now()}-${Math.random().toString(16).slice(2)}`,
      role,
      text: trimMessage(text || ""),
      title: title || "",
      time: new Date().toISOString(),
      session: sessionID || ""
    };
    messages.push(item);
    renderMessage(item);
    if (persist) {
      persistMessages();
    }
    scrollToBottom();
    return item.id;
  }

  function loadMessages() {
    try {
      messages = JSON.parse(localStorage.getItem(messageKey(sessionID)) || "[]");
      if (!Array.isArray(messages)) {
        messages = [];
      }
    } catch {
      messages = [];
    }
    renderMessages();
  }

  function persistMessages() {
    if (!sessionID) {
      return;
    }
    localStorage.setItem(messageKey(sessionID), JSON.stringify(messages.slice(-120)));
  }

  function renderMessages() {
    messageList.innerHTML = "";
    if (messages.length === 0) {
      renderEmptyState();
      return;
    }
    for (const item of messages) {
      renderMessage(item);
    }
    scrollToBottom();
  }

  function renderMessage(item) {
    const empty = messageList.querySelector(".empty-state");
    if (empty) {
      empty.remove();
    }
    const row = document.createElement("article");
    row.className = `message-row ${item.role}`;
    row.dataset.id = item.id;
    if (item.role === "system") {
      row.innerHTML = `<div class="system-bubble">${escapeHTML(item.text)}</div>`;
    } else {
      row.innerHTML = `
        <div class="bubble">
          <div class="bubble-title">
            <span>${escapeHTML(item.title || defaultTitle(item.role))}</span>
            <time>${escapeHTML(formatTime(item.time))}</time>
          </div>
          <pre></pre>
        </div>
      `;
      row.querySelector("pre").textContent = item.text;
    }
    messageList.appendChild(row);
  }

  function renderEmptyState() {
    const samples = [
      "检查当前项目结构",
      "运行测试并修复失败",
      "整理最近改动"
    ];
    const empty = document.createElement("div");
    empty.className = "empty-state";
    empty.innerHTML = `
      <div class="empty-mark">${escapeHTML(agentShortLabel(selectedAgent))}</div>
      <h2>准备接管 ${escapeHTML(agentLabel(selectedAgent))}</h2>
      <p>${escapeHTML(workspacePath.textContent || "等待工作区")}</p>
      <div class="prompt-chips"></div>
    `;
    const chips = empty.querySelector(".prompt-chips");
    for (const sample of samples) {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = sample;
      button.addEventListener("click", () => {
        messageInput.value = sample;
        messageInput.focus();
        messageInput.dispatchEvent(new Event("input"));
      });
      chips.appendChild(button);
    }
    messageList.appendChild(empty);
  }

  function updateMessageElement(item) {
    const row = messageList.querySelector(`[data-id="${CSS.escape(item.id)}"]`);
    if (!row) {
      renderMessage(item);
      return;
    }
    const pre = row.querySelector("pre");
    if (pre) {
      pre.textContent = item.text;
    }
  }

  async function fetchJSON(url, options, timeout = 10000) {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), timeout);
    let response;
    try {
      response = await fetch(appPath(url), Object.assign({ credentials: "same-origin", signal: controller.signal }, options || {}));
    } catch (error) {
      if (error.name === "AbortError") {
        throw new Error("Request timeout.");
      }
      throw error;
    } finally {
      window.clearTimeout(timer);
    }
    if (!response.ok) {
      const detail = (await response.text()).trim();
      throw new Error(detail || `HTTP ${response.status}`);
    }
    return response.json();
  }

  function normalizeTerminalText(value) {
    return cleanAgentNoise(stripAnsi(value))
      .replace(/\r\n/g, "\n")
      .replace(/\r/g, "\n")
      .replace(/\u0007/g, "")
      .replace(/\n{4,}/g, "\n\n\n");
  }

  function stripAnsi(value) {
    return String(value)
      .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
      .replace(/\x1b[@-_][0-?]*[ -/]*[@-~]/g, "")
      .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, "");
  }

  function cleanAgentNoise(value) {
    return String(value)
      .split("\n")
      .filter((line) => {
        const trimmed = line.trim();
        if (!trimmed) {
          return true;
        }
        if (/^Connecting to .* session/i.test(trimmed)) {
          return false;
        }
        if (/^\[websocket closed/i.test(trimmed)) {
          return false;
        }
        return true;
      })
      .join("\n");
  }

  function trimMessage(value) {
    const limit = 20000;
    if (value.length <= limit) {
      return value;
    }
    return `...[前文已折叠]\n${value.slice(-limit)}`;
  }

  function setConnection(value) {
    connectionState.textContent = value;
    connectionState.dataset.state = value.toLowerCase();
  }

  function updateContext(snapshot) {
    const workDir = snapshot?.work_dir || state?.work_dir || "-";
    const sessions = Array.isArray(state?.sessions) ? state.sessions : [];
    const runningCount = sessions.filter((item) => item.running).length;
    workspacePath.textContent = workDir;
    workspacePath.title = workDir;
    edgeName.textContent = state?.edge_name || state?.edge_id || "Local";
    edgeName.title = state?.edge_id || "";
    sessionButton.textContent = shortSession(snapshot?.session_id || sessionID);
    sessionSummary.textContent = `${sessions.length} total / ${runningCount} running`;
    if (snapshot?.agent) {
      selectedAgent = normalizeAgentID(snapshot.agent);
      localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
      renderAgentSelect();
    }
  }

  function defaultTitle(role) {
    if (role === "user") {
      return "Request";
    }
    if (role === "assistant") {
      return agentLabel(selectedAgent);
    }
    return "Status";
  }

  function setComposerEnabled(enabled) {
    messageInput.disabled = !enabled;
    sendButton.disabled = !enabled;
    stopButton.disabled = !enabled;
    updateComposerMeta();
  }

  function renderAgentSelect() {
    const agents = availableAgents();
    agentSelect.innerHTML = "";
    for (const agent of agents) {
      const option = document.createElement("option");
      option.value = agent.id;
      option.textContent = agent.enabled ? agent.label : `${agent.label} (disabled)`;
      option.disabled = !agent.enabled;
      agentSelect.appendChild(option);
    }
    agentSelect.value = selectedAgent;
    agentSelect.disabled = Boolean(sessionID);
  }

  function availableAgents() {
    const configured = Array.isArray(state?.agents) ? state.agents : [];
    if (configured.length > 0) {
      return configured.map((agent) => ({
        id: normalizeAgentID(agent.id),
        label: agent.label || fallbackAgentLabel(agent.id),
        enabled: agent.enabled !== false
      }));
    }
    return [
      { id: "codex", label: "Codex", enabled: true },
      { id: "claude", label: "Claude Code", enabled: false },
      { id: "gemini", label: "Gemini", enabled: false }
    ];
  }

  function ensureSelectedAgent() {
    selectedAgent = normalizeAgentID(selectedAgent);
    if (!availableAgents().some((agent) => agent.id === selectedAgent)) {
      selectedAgent = "codex";
    }
    localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
  }

  function agentLabel(agent) {
    const id = normalizeAgentID(agent);
    const configured = availableAgents().find((item) => item.id === id);
    if (configured?.label) {
      return configured.label;
    }
    return fallbackAgentLabel(id);
  }

  function fallbackAgentLabel(agent) {
    const id = normalizeAgentID(agent);
    if (id === "claude") {
      return "Claude Code";
    }
    if (id === "gemini") {
      return "Gemini";
    }
    return "Codex";
  }

  function agentShortLabel(agent) {
    const id = normalizeAgentID(agent);
    if (id === "claude") {
      return "CL";
    }
    if (id === "gemini") {
      return "GM";
    }
    return "AI";
  }

  function normalizeAgentID(agent) {
    const value = String(agent || "").trim().toLowerCase().replace(/_/g, "-");
    if (value === "claude" || value === "claude-code") {
      return "claude";
    }
    if (value === "gemini") {
      return "gemini";
    }
    return "codex";
  }

  function setAuthBusy(busy) {
    usernameInput.disabled = busy;
    passwordInput.disabled = busy;
    authForm.querySelectorAll("button").forEach((button) => {
      button.disabled = busy;
    });
  }

  function showAuthError(value) {
    authMessage.textContent = value;
  }

  function updateComposerMeta() {
    const length = messageInput.value.trim().length;
    if (messageInput.disabled) {
      composerMeta.textContent = `${agentLabel(selectedAgent)} unavailable`;
      return;
    }
    composerMeta.textContent = length > 0 ? `${length} chars` : "Ready";
  }

  function shortSession(value) {
    return value ? `Session ${value.slice(0, 8)}` : "No session";
  }

  function formatTime(value) {
    if (!value) {
      return "";
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return "";
    }
    return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
  }

  function scrollToBottom() {
    window.requestAnimationFrame(() => {
      messageList.scrollTop = messageList.scrollHeight;
    });
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, (char) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      "\"": "&quot;",
      "'": "&#039;"
    })[char]);
  }
})();
