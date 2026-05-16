(function () {
  const terminalEl = document.getElementById("terminal");
  const authPanel = document.getElementById("authPanel");
  const authForm = document.getElementById("authPanel");
  const loginModeButton = document.getElementById("loginModeButton");
  const registerModeButton = document.getElementById("registerModeButton");
  const usernameInput = document.getElementById("usernameInput");
  const passwordInput = document.getElementById("passwordInput");
  const authMessage = document.getElementById("authMessage");
  const sessionInfo = document.getElementById("sessionInfo");
  const statusDot = document.getElementById("statusDot");
  const accountChip = document.getElementById("accountChip");
  const accountAvatar = document.getElementById("accountAvatar");
  const accountName = document.getElementById("accountName");
  const accountRole = document.getElementById("accountRole");
  const logoutButton = document.getElementById("logoutButton");
  const terminalFooter = document.getElementById("terminalFooter");
  const workDirChip = document.getElementById("workDirChip");

  let completions = ["cat", "cd", "codex", "date", "docker", "kubectl", "ls", "pwd", "uname", "whoami"];

  let terminal = null;
  let fitAddon = null;
  let socket = null;
  let currentLine = "";
  let cursorIndex = 0;
  let history = [];
  let historyIndex = 0;
  let promptReady = false;
  let interactiveMode = false;
  let workDir = "";
  let completing = false;
  let authMode = "login";
  usernameInput.focus();
  setStatus("idle", "Account required");
  loadAccountIdentity();

  if (logoutButton) {
    logoutButton.addEventListener("click", handleLogout);
  }

  window.addEventListener("resize", () => {
    if (!terminal || !fitAddon) {
      return;
    }
    fitAddon.fit();
    sendResize();
  });

  authForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    if (!username || !password) {
      setAuthMessage("Account and password are required.", "error");
      return;
    }

    setAuthBusy(true);
    setAuthMessage("Verifying account...", "");
    setStatus("connecting", "Verifying account");

    try {
      await submitAccount(username, password);
      const edge = await fetchEdge();
      setCompletions(edge.commands);
      openTerminal(edge);
      await loadAccountIdentity();
      connect();
    } catch {
      setAuthBusy(false);
      setAuthMessage("Account verification failed.", "error");
      setStatus("error", "Account required");
    }
  });

  loginModeButton.addEventListener("click", () => setAuthMode("login"));
  registerModeButton.addEventListener("click", () => setAuthMode("register"));

  function setAuthMode(mode) {
    authMode = mode;
    loginModeButton.classList.toggle("active", mode === "login");
    registerModeButton.classList.toggle("active", mode === "register");
    authPanel.querySelector("button[type='submit']").textContent = mode === "register" ? "Create account" : "Connect";
    setAuthMessage("", "");
    usernameInput.focus();
  }

  async function submitAccount(username, password) {
    const path = authMode === "register" ? "/cloud-terminal-api/accounts/register" : "/cloud-terminal-api/accounts/login";
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password })
    });
    if (!response.ok) {
      throw new Error("account rejected");
    }
    passwordInput.value = "";
    return response.json();
  }

  async function fetchEdge() {
    const response = await fetch("/cloud-terminal-api/edge");
    if (!response.ok) {
      throw new Error("account rejected");
    }
    return response.json();
  }

  function openTerminal(edge) {
    authPanel.classList.add("hidden");
    terminalEl.hidden = false;
    if (terminalFooter) {
      terminalFooter.hidden = false;
    }
    setStatus("connecting", `Connecting · ${edge.name}`);

    if (!terminal) {
      terminal = new Terminal({
        cursorBlink: true,
        fontFamily: "JetBrains Mono, SFMono-Regular, Consolas, monospace",
        fontSize: 14,
        lineHeight: 1.18,
        theme: {
          background: "#0b0f14",
          foreground: "#e6edf3",
          cursor: "#39c6a3",
          selectionBackground: "#315d55",
          black: "#0b0f14",
          red: "#ff6b6b",
          green: "#39c6a3",
          yellow: "#f2c14e",
          blue: "#7aa2f7",
          magenta: "#bb9af7",
          cyan: "#7dcfff",
          white: "#e6edf3",
          brightBlack: "#637083",
          brightRed: "#ff8b8b",
          brightGreen: "#6ee7c8",
          brightYellow: "#ffd76d",
          brightBlue: "#9ab8ff",
          brightMagenta: "#d2b5ff",
          brightCyan: "#9ce6ff",
          brightWhite: "#ffffff"
        }
      });
      fitAddon = new FitAddon.FitAddon();
      terminal.loadAddon(fitAddon);
      terminal.open(terminalEl);
      terminal.attachCustomKeyEventHandler(handleTerminalKey);
      terminal.onData(handleTerminalData);
    } else {
      terminal.reset();
    }

    fitAddon.fit();
    terminal.focus();
    terminal.write("\x1b[2mConnecting...\x1b[0m");
  }

  function handleTerminalData(data) {
    if (interactiveMode) {
      sendInteractiveInput(data);
      return;
    }
    if (!promptReady) {
      return;
    }
    for (const char of data) {
      if (handleControlSequence(data)) {
        return;
      }
      if (char === "\r") {
        submitLine();
        continue;
      }
      if (char === "\t") {
        completeCurrentLine();
        continue;
      }
      if (char === "\u0003") {
        cancelCurrentLine();
        continue;
      }
      if (char === "\u000c") {
        terminal.clear();
        redrawLine();
        continue;
      }
      if (char === "\u0015") {
        replaceLine("");
        continue;
      }
      if (char === "\u007f") {
        if (cursorIndex > 0) {
          currentLine = currentLine.slice(0, cursorIndex - 1) + currentLine.slice(cursorIndex);
          cursorIndex -= 1;
          redrawLine();
        }
        continue;
      }
      if (char >= " " && char !== "\u007f") {
        insertText(char);
      }
    }
  }

  function handleTerminalKey(event) {
    if (interactiveMode || !promptReady || event.type !== "keydown") {
      return true;
    }

    switch (event.key) {
      case "Tab":
        event.preventDefault();
        completeCurrentLine();
        return false;
      case "ArrowUp":
        event.preventDefault();
        showHistory(-1);
        return false;
      case "ArrowDown":
        event.preventDefault();
        showHistory(1);
        return false;
      case "ArrowLeft":
        event.preventDefault();
        moveCursor(-1);
        return false;
      case "ArrowRight":
        event.preventDefault();
        moveCursor(1);
        return false;
      case "Home":
        event.preventDefault();
        moveCursorTo(0);
        return false;
      case "End":
        event.preventDefault();
        moveCursorTo(currentLine.length);
        return false;
      case "Delete":
        event.preventDefault();
        deleteAtCursor();
        return false;
      default:
        return true;
    }
  }

  function connect() {
    if (socket) {
      socket.close();
    }

    const protocol = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${protocol}://${window.location.host}/cloud-terminal-api/ws/terminal`;
    socket = new WebSocket(url);

    socket.addEventListener("open", () => {
      setStatus("connected", "Connected");
    });

    socket.addEventListener("message", (event) => {
      const msg = JSON.parse(event.data);
      handleMessage(msg);
    });

    socket.addEventListener("close", () => {
      promptReady = false;
      interactiveMode = false;
      if (terminal) {
        terminal.write("\r\n[disconnected]\r\n");
      }
      setStatus("disconnected", "Disconnected");
    });

    socket.addEventListener("error", () => {
      if (terminal) {
        terminal.write("\r\n\x1b[31mConnection error\x1b[0m\r\n");
      }
      setStatus("error", "Connection error");
    });
  }

  function handleMessage(msg) {
    switch (msg.type) {
      case "ready":
        setStatus("connected", `${msg.session_id} · ${msg.edge_id}`);
        if (msg.work_dir) {
          workDir = msg.work_dir;
          updateWorkDirChip();
        }
        terminal.write("\r\x1b[2K");
        if (msg.data) {
          terminal.write(msg.data);
        }
        prompt();
        break;
      case "start":
        promptReady = false;
        terminal.write("\r\n");
        break;
      case "interactive_ready":
        promptReady = false;
        interactiveMode = true;
        terminal.focus();
        break;
      case "interactive_output":
        if (msg.data) {
          terminal.write(msg.data);
        }
        break;
      case "interactive_exit":
        interactiveMode = false;
        promptReady = false;
        terminal.write(`\r\n\x1b[33m[interactive exit ${msg.exit_code || 0}] ${msg.duration || ""}\x1b[0m\r\n`);
        break;
      case "result":
        const exitCode = msg.exit_code ?? 0;
        if (msg.work_dir) {
          workDir = msg.work_dir;
          updateWorkDirChip();
        }
        if (msg.stdout) {
          terminal.write(normalizeNewlines(msg.stdout));
        }
        if (msg.stderr) {
          terminal.write(`\x1b[31m${normalizeNewlines(msg.stderr)}\x1b[0m`);
        }
        if (exitCode !== 0) {
          terminal.write(`\x1b[33m[exit ${exitCode}] ${msg.duration || ""}\x1b[0m\r\n`);
        }
        break;
      case "prompt":
        if (msg.work_dir) {
          workDir = msg.work_dir;
          updateWorkDirChip();
        }
        prompt();
        break;
      case "error":
        terminal.write(`\r\n\x1b[31m${msg.error}\x1b[0m\r\n`);
        prompt();
        break;
      case "pong":
        break;
      default:
        terminal.write(`\r\nunknown message: ${msg.type}\r\n`);
        prompt();
    }
  }

  function prompt() {
    currentLine = "";
    cursorIndex = 0;
    historyIndex = history.length;
    promptReady = true;
    terminal.write(promptText());
  }

  function submitLine() {
    if (!currentLine.trim()) {
      terminal.write("\r\n");
      prompt();
      return;
    }
    submitCommand(currentLine);
  }

  function submitCommand(line) {
    if (!socket || socket.readyState !== WebSocket.OPEN || interactiveMode) {
      return;
    }
    line = line.trim();
    if (!line) {
      return;
    }
    pushHistory(line);
    currentLine = "";
    cursorIndex = 0;
    promptReady = false;
    socket.send(JSON.stringify({
      type: isInteractiveCommand(line) ? "interactive_start" : "exec",
      line,
      rows: terminal.rows,
      cols: terminal.cols
    }));
  }

  function handleControlSequence(data) {
    switch (data) {
      case "\u001b[A":
        showHistory(-1);
        return true;
      case "\u001b[B":
        showHistory(1);
        return true;
      case "\u001b[D":
        moveCursor(-1);
        return true;
      case "\u001b[C":
        moveCursor(1);
        return true;
      case "\u001b[H":
      case "\u001bOH":
        moveCursorTo(0);
        return true;
      case "\u001b[F":
      case "\u001bOF":
        moveCursorTo(currentLine.length);
        return true;
      case "\u001b[3~":
        deleteAtCursor();
        return true;
      default:
        return data.startsWith("\u001b");
    }
  }

  function insertText(text) {
    currentLine = currentLine.slice(0, cursorIndex) + text + currentLine.slice(cursorIndex);
    cursorIndex += text.length;
    redrawLine();
  }

  function replaceLine(line) {
    currentLine = line;
    cursorIndex = line.length;
    redrawLine();
  }

  function redrawLine() {
    terminal.write(`\r\x1b[2K${promptText()}${currentLine}`);
    const moveLeft = currentLine.length - cursorIndex;
    if (moveLeft > 0) {
      terminal.write(`\x1b[${moveLeft}D`);
    }
  }

  function moveCursor(delta) {
    moveCursorTo(cursorIndex + delta);
  }

  function moveCursorTo(index) {
    const next = Math.max(0, Math.min(index, currentLine.length));
    const delta = next - cursorIndex;
    if (delta > 0) {
      terminal.write(`\x1b[${delta}C`);
    } else if (delta < 0) {
      terminal.write(`\x1b[${Math.abs(delta)}D`);
    }
    cursorIndex = next;
  }

  function deleteAtCursor() {
    if (cursorIndex >= currentLine.length) {
      return;
    }
    currentLine = currentLine.slice(0, cursorIndex) + currentLine.slice(cursorIndex + 1);
    redrawLine();
  }

  function showHistory(direction) {
    if (history.length === 0) {
      return;
    }
    historyIndex = Math.max(0, Math.min(historyIndex + direction, history.length));
    replaceLine(historyIndex === history.length ? "" : history[historyIndex]);
  }

  function pushHistory(line) {
    if (history[history.length - 1] !== line) {
      history.push(line);
    }
    if (history.length > 100) {
      history = history.slice(history.length - 100);
    }
    historyIndex = history.length;
  }

  async function completeCurrentLine() {
    if (completing) {
      return;
    }
    const context = completionContext();
    completing = true;
    let matches;
    try {
      matches = await fetchCompletions(context);
    } catch {
      matches = fallbackCompletions(context);
    } finally {
      completing = false;
    }

    if (matches.length === 0) {
      terminal.write("\x07");
      return;
    }
    if (matches.length === 1) {
      applyCompletion(context, matches[0]);
      return;
    }

    const common = commonPrefix(matches);
    if (common.length > context.prefix.length) {
      applyCompletion(context, common, false);
      return;
    }

    terminal.write(`\r\n${matches.join("  ")}\r\n`);
    redrawLine();
  }

  function completionContext() {
    const before = currentLine.slice(0, cursorIndex);
    const match = before.match(/(?:^|\s)([^\s]*)$/);
    const prefix = match ? match[1] : "";
    const start = cursorIndex - prefix.length;
    return {
      kind: start === 0 ? "command" : "path",
      prefix,
      start,
      end: cursorIndex
    };
  }

  async function fetchCompletions(context) {
    const params = new URLSearchParams({
      kind: context.kind,
      prefix: context.prefix,
      work_dir: workDir
    });
    const response = await fetch(`/cloud-terminal-api/complete?${params.toString()}`, {
      credentials: "same-origin"
    });
    if (!response.ok) {
      throw new Error("completion failed");
    }
    const data = await response.json();
    return Array.isArray(data.matches) ? data.matches : [];
  }

  function fallbackCompletions(context) {
    if (context.kind !== "command") {
      return [];
    }
    return completions.filter((command) => command.startsWith(context.prefix));
  }

  function applyCompletion(context, value, addTrailingSpace = true) {
    let replacement = value;
    if (addTrailingSpace && context.kind === "command") {
      replacement += " ";
    }
    if (addTrailingSpace && context.kind === "path" && !replacement.endsWith("/")) {
      replacement += " ";
    }
    currentLine = currentLine.slice(0, context.start) + replacement + currentLine.slice(context.end);
    cursorIndex = context.start + replacement.length;
    redrawLine();
  }

  function cancelCurrentLine() {
    currentLine = "";
    cursorIndex = 0;
    terminal.write("^C\r\n");
    prompt();
  }

  function promptText() {
    const label = workDir ? shortPath(workDir) : "terminal";
    return `\x1b[32m${label}>\x1b[0m `;
  }

  function shortPath(path) {
    const parts = path.split("/").filter(Boolean);
    if (parts.length === 0) {
      return "/";
    }
    return parts[parts.length - 1];
  }

  function setCompletions(commands) {
    if (!Array.isArray(commands)) {
      return;
    }
    completions = Array.from(new Set(commands.filter(Boolean))).sort();
  }

  function commonPrefix(values) {
    if (values.length === 0) {
      return "";
    }
    let prefix = values[0];
    for (const value of values.slice(1)) {
      while (!value.startsWith(prefix) && prefix) {
        prefix = prefix.slice(0, -1);
      }
    }
    return prefix;
  }

  function sendInteractiveInput(data) {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    socket.send(JSON.stringify({
      type: "interactive_input",
      data
    }));
  }

  function sendResize() {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    socket.send(JSON.stringify({
      type: "resize",
      rows: terminal.rows,
      cols: terminal.cols
    }));
  }

  function isInteractiveCommand(line) {
    const command = line.trim().split(/\s+/)[0];
    return command === "codex";
  }

  function setAuthBusy(busy) {
    usernameInput.disabled = busy;
    passwordInput.disabled = busy;
    authPanel.querySelectorAll("button").forEach((button) => {
      button.disabled = busy;
    });
  }

  function setAuthMessage(text, tone) {
    authMessage.textContent = text;
    authMessage.className = "auth-message";
    if (tone) {
      authMessage.classList.add(tone);
    }
  }

  function normalizeNewlines(value) {
    return value.replace(/\n/g, "\r\n");
  }

  function setStatus(state, label) {
    if (sessionInfo && typeof label === "string") {
      sessionInfo.textContent = label;
    }
    if (statusDot) {
      statusDot.dataset.state = state || "idle";
    }
  }

  function updateWorkDirChip() {
    if (!workDirChip) {
      return;
    }
    if (!workDir) {
      workDirChip.hidden = true;
      workDirChip.textContent = "";
      return;
    }
    workDirChip.hidden = false;
    workDirChip.textContent = workDir;
    workDirChip.title = workDir;
  }

  async function loadAccountIdentity() {
    if (!accountChip) {
      return;
    }
    try {
      const response = await fetch("/cloud-terminal-api/accounts/me", { credentials: "same-origin" });
      if (!response.ok) {
        accountChip.hidden = true;
        return;
      }
      const data = await response.json();
      renderAccountChip(data);
    } catch {
      accountChip.hidden = true;
    }
  }

  function renderAccountChip(data) {
    if (!accountChip || !data || !data.username) {
      if (accountChip) {
        accountChip.hidden = true;
      }
      return;
    }
    accountChip.hidden = false;
    accountName.textContent = data.username;
    accountRole.textContent = (data.role || "user").toUpperCase();
    accountAvatar.textContent = buildAvatarInitials(data.username);
  }

  function buildAvatarInitials(name) {
    const trimmed = String(name || "").trim();
    if (!trimmed) {
      return "--";
    }
    const parts = trimmed.split(/[\s@._-]+/).filter(Boolean);
    if (parts.length >= 2) {
      return (parts[0][0] + parts[1][0]).toUpperCase();
    }
    return trimmed.slice(0, 2).toUpperCase();
  }

  async function handleLogout() {
    logoutButton.disabled = true;
    try {
      await fetch("/cloud-terminal-api/accounts/logout", {
        method: "POST",
        credentials: "same-origin"
      });
    } catch {
      // ignore
    }
    if (socket) {
      try {
        socket.close();
      } catch {
        // ignore
      }
    }
    window.location.reload();
  }
})();
