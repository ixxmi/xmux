(function () {
  const SESSION_KEY = "cloud-terminal-mobile-session";
  const TARGET_KEY = "cloud-terminal-mobile-target";
  const FOLDER_KEY = "cloud-terminal-mobile-folders";
  const FOLDER_SESSION_KEY = "cloud-terminal-mobile-folder-sessions";
  const ARCHIVED_FOLDER_KEY = "cloud-terminal-mobile-archived-folders";
  const ARCHIVED_SESSION_KEY = "cloud-terminal-mobile-archived-sessions";
  const ACTIVE_AGENT_KEY = "cloud-terminal-mobile-agent";
  const MOBILE_SYMBOLS = "~!@#$%^&*()_+-=[]{}\\|;:'\",.<>/?`！￥……（）【】《》、，。？；：‘’“”·—";

  const mobileApp = document.querySelector(".mobile-app");
  const authView = document.getElementById("authView");
  const workbenchView = document.getElementById("workbenchView");
  const targetView = document.getElementById("targetView");
  const authForm = document.getElementById("authForm");
  const tokenInput = document.getElementById("tokenInput");
  const authMessage = document.getElementById("authMessage");
  const connectionState = document.getElementById("connectionState");
  const sessionButton = document.getElementById("sessionButton");
  const workspacePath = document.getElementById("workspacePath");
  const processButton = document.getElementById("processButton");
  const menuButton = document.getElementById("menuButton");
  const actionMenu = document.getElementById("actionMenu");
  const processPanel = document.getElementById("processPanel");
  const processBackdrop = document.getElementById("processBackdrop");
  const processList = document.getElementById("processList");
  const processSummary = document.getElementById("processSummary");
  const addFolderButton = document.getElementById("addFolderButton");
  const folderPicker = document.getElementById("folderPicker");
  const folderPickerPath = document.getElementById("folderPickerPath");
  const folderPickerList = document.getElementById("folderPickerList");
  const folderBackButton = document.getElementById("folderBackButton");
  const terminalPage = document.getElementById("terminalPage");
  const terminalEl = document.getElementById("terminal");
  const keybar = document.getElementById("keybar");
  const bottomTabs = document.getElementById("bottomTabs");
  const reconnectButton = document.getElementById("reconnectButton");
  const newSessionButton = document.getElementById("newSessionButton");
  const stopSessionButton = document.getElementById("stopSessionButton");
  const startAgentButton = document.getElementById("startAgentButton");
  const agentSelector = document.getElementById("agentSelector");
  const targetParentButton = document.getElementById("targetParentButton");
  const targetRefreshButton = document.getElementById("targetRefreshButton");
  const targetCurrentPath = document.getElementById("targetCurrentPath");
  const targetSelection = document.getElementById("targetSelection");
  const targetFileList = document.getElementById("targetFileList");
  const parentButton = document.getElementById("parentButton");
  const refreshFilesButton = document.getElementById("refreshFilesButton");
  const currentPathEl = document.getElementById("currentPath");
  const fileList = document.getElementById("fileList");
  const fileViewer = document.getElementById("fileViewer");
  const viewerTitle = document.getElementById("viewerTitle");
  const fileContent = document.getElementById("fileContent");
  const fileDiffButton = document.getElementById("fileDiffButton");
  const refreshDiffButton = document.getElementById("refreshDiffButton");
  const diffOutput = document.getElementById("diffOutput");
  const diffScope = document.getElementById("diffScope");
  const previewPort = document.getElementById("previewPort");
  const openPreviewButton = document.getElementById("openPreviewButton");
  const refreshPreviewButton = document.getElementById("refreshPreviewButton");
  const previewFrame = document.getElementById("previewFrame");

  let state = null;
  let terminal = null;
  let fitAddon = null;
  let socket = null;
  let sessionID = localStorage.getItem(SESSION_KEY) || "";
  let selectedTarget = readSavedTarget();
  let selectedAgent = normalizeAgentID(localStorage.getItem(ACTIVE_AGENT_KEY) || "codex");
  let currentPath = "";
  let targetPath = "";
  let targetParent = "";
  let selectedFile = "";
  let activeTab = "terminal";
  let reconnectTimer = 0;
  let manualClose = false;
  let accessToken = "";
  let reconnectAttempts = 0;
  let socketSeq = 0;
  let agentStarted = false;
  let sessions = [];
  let processFolders = readSavedFolders();
  let folderSessions = migrateFolderSessions(readObject(FOLDER_SESSION_KEY));
  let archivedFolders = readStringSet(ARCHIVED_FOLDER_KEY);
  let archivedSessions = readStringSet(ARCHIVED_SESSION_KEY);
  let processPanelOpen = false;
  let folderPickerOpen = false;
  let folderPickerParent = "";
  let newTerminalFolder = "";
  let lastXtermInput = { data: "", at: 0 };
  const busyTimers = new Map();

  window.addEventListener("error", (event) => {
    setAuthBusy(false);
    setAuthMessage(event.message || "Page script error.");
    writeTerminalError(event.message || "Page script error.");
  });

  window.addEventListener("unhandledrejection", (event) => {
    setAuthBusy(false);
    const message = event.reason?.message || String(event.reason || "Request failed.");
    setAuthMessage(message);
    writeTerminalError(message);
  });

  bootstrap();

  authForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const token = tokenInput.value.trim();
    if (!token) {
      setAuthMessage("Token is required.");
      return;
    }
    setAuthBusy(true);
    setAuthMessage("Verifying...");
    try {
      accessToken = token;
      state = await auth(token);
      tokenInput.value = "";
      showWorkbench();
    } catch (error) {
      setAuthMessage(error.message || "Token rejected.");
    } finally {
      setAuthBusy(false);
    }
  });

  startAgentButton.addEventListener("click", async () => {
    if (!selectedTarget) {
      return;
    }
    await startAgent();
  });

  agentSelector.addEventListener("click", (event) => {
    const button = event.target.closest("[data-agent]");
    if (!button || button.disabled) {
      return;
    }
    setSelectedAgent(button.dataset.agent);
  });

  reconnectButton.addEventListener("click", () => {
    closeActionMenu();
    if (!selectedTarget && !sessionID) {
      showTargetPicker();
      return;
    }
    if (!agentStarted) {
      agentStarted = true;
      hideTargetPicker();
      initTerminal();
      activateTab("terminal");
    }
    connect(true);
  });

  stopSessionButton.addEventListener("click", () => {
    closeActionMenu();
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "stop" }));
      setConnection("Stopping");
      markSessionDone(sessionID);
      upsertSession({
        id: sessionID,
        agent: currentSession()?.agent || selectedAgent,
        agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
        work_dir: currentSession()?.work_dir || currentPath || activeWorkDir(),
        running: false,
        busy: false,
        last_active: new Date().toISOString()
      });
    }
  });

  newSessionButton.addEventListener("click", () => {
    closeActionMenu();
    closeSocketOnly();
    sessionID = "";
    agentStarted = false;
    sessionButton.textContent = "No session";
    showTargetPicker();
  });

  processButton.addEventListener("click", () => {
    processPanelOpen = !processPanelOpen;
    if (!processPanelOpen) {
      newTerminalFolder = "";
    }
    renderProcessList();
  });

  processBackdrop.addEventListener("click", () => {
    processPanelOpen = false;
    newTerminalFolder = "";
    renderProcessList();
  });

  addFolderButton.addEventListener("click", () => {
    openFolderPicker();
  });

  folderBackButton.addEventListener("click", () => {
    if (folderPickerParent) {
      loadFolderPicker(folderPickerParent);
    } else {
      renderFolderPickerRoots();
    }
  });

  menuButton.addEventListener("click", (event) => {
    event.stopPropagation();
    actionMenu.hidden = !actionMenu.hidden;
  });

  document.addEventListener("click", (event) => {
    if (!actionMenu.hidden && !actionMenu.contains(event.target) && event.target !== menuButton) {
      closeActionMenu();
    }
    if (processPanelOpen && !processPanel.contains(event.target) && event.target !== processButton && !processButton.contains(event.target)) {
      processPanelOpen = false;
      newTerminalFolder = "";
      renderProcessList();
      return;
    }
    const clickTarget = event.target instanceof Element ? event.target : null;
    if (processPanelOpen && newTerminalFolder && clickTarget && processPanel.contains(clickTarget) && !clickTarget.closest(".folder-agent-menu") && !clickTarget.closest(".folder-new")) {
      newTerminalFolder = "";
      renderProcessList();
    }
  });

  targetParentButton.addEventListener("click", () => {
    if (targetParent) {
      loadTargetFiles(targetParent);
    }
  });

  targetRefreshButton.addEventListener("click", () => loadTargetFiles(targetPath || state?.work_dir || ""));

  document.querySelectorAll("[data-tab]").forEach((button) => {
    button.addEventListener("click", () => activateTab(button.dataset.tab));
  });

  document.querySelectorAll("[data-key]").forEach((button) => {
    button.addEventListener("click", () => sendShortcut(button.dataset.key));
  });

  parentButton.addEventListener("click", () => {
    if (state && state.parentPath) {
      loadFiles(state.parentPath);
    }
  });

  refreshFilesButton.addEventListener("click", () => loadFiles(currentPath || activeWorkDir()));
  refreshDiffButton.addEventListener("click", () => loadDiff(""));
  openPreviewButton.addEventListener("click", () => openPreview());
  refreshPreviewButton.addEventListener("click", () => openPreview(true));
  fileDiffButton.addEventListener("click", () => {
    if (selectedFile) {
      activateTab("diff");
      loadDiff(selectedFile);
    }
  });

  sessionButton.addEventListener("click", () => {
    if (!sessions.length) {
      return;
    }
    const running = sessions.find((item) => item.running);
    const latest = running || sessions[0];
    if (latest && latest.id !== sessionID) {
      attachSession(latest.id);
    }
  });

  window.addEventListener("resize", () => {
    fitTerminal();
    sendResize();
    scrollTerminalToBottom();
  });

  document.addEventListener("visibilitychange", () => {
    if (!document.hidden && agentStarted && (!socket || socket.readyState === WebSocket.CLOSED)) {
      connect(true);
    }
  });

  async function bootstrap() {
    tokenInput.value = "";
    tokenInput.focus();
    try {
      state = await fetchJSON("/cloud-terminal-api/workbench/state", null, 5000);
      showWorkbench();
    } catch {
      authView.hidden = false;
      workbenchView.hidden = true;
    }
  }

  async function auth(token) {
    return fetchJSON("/cloud-terminal-api/workbench/auth", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token })
    }, 8000);
  }

  function showWorkbench() {
    authView.hidden = true;
    workbenchView.hidden = false;
    sessions = Array.isArray(state.sessions) ? state.sessions : [];
    ensureSelectedAgent();
    workspacePath.textContent = state.work_dir || "";
    currentPath = selectedTarget?.workDir || state.work_dir;
    syncFoldersFromSessions();
    renderAgentSelector();
    renderPreviewPorts();
    renderProcessList();
    if (sessionID && sessions.some((item) => item.id === sessionID && item.running)) {
      sessionButton.textContent = shortSession(sessionID);
      restoreSession();
      return;
    }
    const running = sessions.find((item) => item.running);
    if (running) {
      attachSession(running.id);
      return;
    }
    showTargetPicker();
  }

  function showTargetPicker() {
    agentStarted = false;
    targetView.hidden = false;
    terminalPage.hidden = true;
    keybar.hidden = true;
    bottomTabs.hidden = true;
    updateProcessPanelVisibility();
    setConnection("Choose target");
    setWorkbenchTabsEnabled(false);
    renderAgentSelector();
    updateSelectedTarget();
    loadTargetFiles(selectedTarget?.workDir || state?.work_dir || "");
  }

  function hideTargetPicker() {
    targetView.hidden = true;
    terminalPage.hidden = false;
    keybar.hidden = false;
    bottomTabs.hidden = false;
    updateProcessPanelVisibility();
    setWorkbenchTabsEnabled(true);
  }

  async function startAgent() {
    const nextTarget = selectedTarget;
    if (!agentEnabled(selectedAgent)) {
      targetSelection.textContent = `${agentLabel(selectedAgent)} 未在后台命令策略中启用`;
      return;
    }
    startAgentButton.disabled = true;
    const previousText = startAgentButton.textContent;
    startAgentButton.textContent = "Preparing...";
    targetSelection.textContent = "正在预热工作区文件索引...";
    try {
      const warmed = await warmupTarget(nextTarget.workDir);
      targetSelection.textContent = `已预热 ${warmed.dirs || 0} 个目录、${warmed.files || 0} 个文件${warmed.truncated ? "，已达到上限" : ""}`;
    } catch (error) {
      targetSelection.textContent = `预热失败，仍将启动：${error.message || "unknown error"}`;
    } finally {
      startAgentButton.textContent = previousText;
      startAgentButton.disabled = false;
    }
    closeSocketOnly();
    sessionID = "";
    agentStarted = true;
    hideTargetPicker();
    initTerminal();
    activateTab("terminal");
    connect(true, nextTarget);
    currentPath = nextTarget.workDir;
    loadFiles(currentPath);
    loadDiff("");
  }

  function openFolderSession(folder, agentOverride, forceNew = false) {
    const nextAgent = normalizeAgentID(agentOverride || selectedAgent);
    selectedAgent = nextAgent;
    localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
    selectedTarget = { kind: "dir", path: folder, workDir: folder, label: folder };
    localStorage.setItem(TARGET_KEY, JSON.stringify(selectedTarget));
    workspacePath.textContent = folder;
    currentPath = folder;
    if (forceNew) {
      processPanelOpen = true;
      newTerminalFolder = "";
    } else {
      processPanelOpen = false;
      newTerminalFolder = "";
    }
    renderProcessList();

    if (forceNew) {
      startAgent();
      return;
    }

    const remembered = folderSessionID(folder, nextAgent);
    if (remembered && sessions.some((item) => item.id === remembered && item.running)) {
      attachSession(remembered);
      return;
    }
    const existing = sessions.find((item) => sessionMatchesFolderAgent(item, folder, nextAgent) && item.running && !archivedSessions.has(item.id));
    if (existing) {
      attachSession(existing.id);
      return;
    }
    startAgent();
  }

  function restoreSession() {
    agentStarted = true;
    hideTargetPicker();
    initTerminal();
    activateTab("terminal");
    connect(true);
    const current = currentSession();
    currentPath = current?.work_dir || selectedTarget?.workDir || state?.work_dir || "";
    if (currentPath) {
      loadFiles(currentPath);
      loadDiff("");
    }
  }

  function attachSession(id) {
    if (!id) {
      return;
    }
    closeSocketOnly();
    sessionID = id;
    localStorage.setItem(SESSION_KEY, sessionID);
    const next = sessions.find((item) => item.id === id);
    if (next?.work_dir) {
      currentPath = next.work_dir;
      workspacePath.textContent = next.work_dir;
    }
    if (next?.agent) {
      selectedAgent = normalizeAgentID(next.agent);
      localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
      renderAgentSelector();
    }
    restoreSession();
  }

  function initTerminal() {
    if (terminal) {
      fitTerminal();
      return;
    }
    terminal = new Terminal({
      cursorBlink: true,
      allowProposedApi: false,
      fontFamily: "SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: isSmallScreen() ? 12 : 14,
      lineHeight: isSmallScreen() ? 1.08 : 1.12,
      scrollback: 5000,
      theme: {
        background: "#080b0e",
        foreground: "#e7ecef",
        cursor: "#74d3ae",
        selectionBackground: "#29483f",
        black: "#080b0e",
        red: "#ff6a64",
        green: "#74d3ae",
        yellow: "#e8c46c",
        blue: "#78a8ff",
        magenta: "#c49aff",
        cyan: "#72d7e0",
        white: "#e7ecef",
        brightBlack: "#626d78",
        brightRed: "#ff8b85",
        brightGreen: "#99e8c9",
        brightYellow: "#f3d98b",
        brightBlue: "#9abfff",
        brightMagenta: "#d8b8ff",
        brightCyan: "#a0edf3",
        brightWhite: "#ffffff"
      }
    });
    fitAddon = new FitAddon.FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(terminalEl);
    terminal.onData((data) => {
      lastXtermInput = { data, at: Date.now() };
      sendInput(data);
    });
    terminal.onWriteParsed(() => scrollTerminalToBottom());
    installMobileInputFallback();
    fitTerminal();
  }

  function connect(resetTerminal, targetOverride) {
    clearTimeout(reconnectTimer);
    if (!terminal) {
      return;
    }
    const seq = ++socketSeq;
    if (socket) {
      manualClose = true;
      socket.close();
    }
    if (resetTerminal) {
      terminal.reset();
      terminal.write(`\x1b[2mConnecting to ${agentLabel(selectedAgent)} session...\x1b[0m`);
    }
    setConnection("Connecting");

    const params = new URLSearchParams({
      rows: String(terminal.rows || 24),
      cols: String(terminal.cols || 100)
    });
    if (sessionID) {
      params.set("session_id", sessionID);
    } else {
      params.set("agent", selectedAgent);
      const target = targetOverride || selectedTarget;
      if (target) {
        params.set("work_dir", target.workDir);
        if (target.kind === "file") {
          params.set("target", target.path);
        }
      }
    }
    if (accessToken) {
      params.set("token", accessToken);
    }
    const protocol = window.location.protocol === "https:" ? "wss" : "ws";
    socket = new WebSocket(`${protocol}://${window.location.host}/cloud-terminal-api/ws/workbench?${params.toString()}`);
    manualClose = false;

    socket.addEventListener("message", (event) => {
      if (seq !== socketSeq) {
        return;
      }
      handleSocketMessage(JSON.parse(event.data));
    });
    socket.addEventListener("open", () => {
      if (seq !== socketSeq) {
        return;
      }
      reconnectAttempts = 0;
      setConnection("Connected");
    });
    socket.addEventListener("close", (event) => {
      if (seq !== socketSeq) {
        return;
      }
      const reason = event.code ? `Detached (${event.code})` : "Detached";
      setConnection(reason);
      writeTerminal(`\r\n\x1b[33m[websocket closed ${event.code || ""}] ${event.reason || ""}\x1b[0m\r\n`);
      if (manualClose) {
        return;
      }
      if (event.code === 1008 || event.code === 1002 || event.code === 1003 || event.code === 1011) {
        writeTerminal("\x1b[31mWebSocket stopped. Please check token, Origin allowlist, and server logs.\x1b[0m\r\n");
        return;
      }
      if (agentStarted && reconnectAttempts < 5) {
        reconnectAttempts += 1;
        reconnectTimer = window.setTimeout(() => connect(true), Math.min(1000 + reconnectAttempts * 700, 6000));
      } else if (agentStarted) {
        writeTerminal("\x1b[31mWebSocket reconnect stopped after 5 attempts. Tap Reconnect to retry.\x1b[0m\r\n");
      }
    });
    socket.addEventListener("error", () => {
      if (seq !== socketSeq) {
        return;
      }
      setConnection("Connection error");
      markSessionDone(sessionID);
      writeTerminalError("WebSocket connection error. Check token, Origin allowlist, and reverse proxy WebSocket upgrade.");
    });
  }

  function handleSocketMessage(msg) {
    switch (msg.type) {
      case "ready":
        sessionID = msg.session_id;
        const readyAgent = normalizeAgentID(msg.agent || currentSession()?.agent || selectedAgent);
        selectedAgent = readyAgent;
        localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
        localStorage.setItem(SESSION_KEY, sessionID);
        sessionButton.textContent = shortSession(sessionID);
        const previous = currentSession();
        upsertSession({
          id: sessionID,
          agent: readyAgent,
          agent_label: msg.agent_label || previous?.agent_label || agentLabel(readyAgent),
          work_dir: msg.work_dir || selectedTarget?.workDir || state?.work_dir || "",
          running: msg.running !== false,
          busy: previous?.busy || false,
          last_active: msg.last_active || new Date().toISOString(),
          started_at: msg.started_at || ""
        });
        workspacePath.textContent = msg.work_dir || selectedTarget?.label || state?.work_dir || "";
        currentPath = msg.work_dir || currentPath;
        terminal.reset();
        setConnection(msg.running === false ? "Finished" : "Attached");
        renderAgentSelector();
        renderProcessList();
        fitTerminal();
        scrollTerminalToBottom();
        break;
      case "replay":
        writeTerminal(msg.data || "");
        scrollTerminalToBottom();
        break;
      case "output":
        noteSessionOutput(sessionID, msg.data || "");
        writeTerminal(msg.data || "");
        scrollTerminalToBottom();
        break;
      case "exit":
        setConnection("Finished");
        stopSessionButton.disabled = true;
        markSessionDone(sessionID);
        upsertSession({
          id: sessionID,
          agent: currentSession()?.agent || selectedAgent,
          agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
          work_dir: currentSession()?.work_dir || currentPath || selectedTarget?.workDir || "",
          running: false,
          busy: false,
          exit_code: msg.exit_code ?? 0,
          duration: msg.duration || "",
          error: msg.error || "",
          last_active: new Date().toISOString()
        });
        writeTerminal(`\r\n\x1b[33m[${agentLabel(currentSession()?.agent || selectedAgent)} exit ${msg.exit_code ?? 0}] ${msg.duration || ""}\x1b[0m\r\n`);
        if (msg.error) {
          writeTerminal(`\x1b[31m${msg.error}\x1b[0m\r\n`);
        }
        break;
      case "error":
        writeTerminal(`\r\n\x1b[31m${msg.error || "error"}\x1b[0m\r\n`);
        setConnection("Error");
        break;
      case "pong":
        break;
    }
  }

  async function loadTargetFiles(path) {
    if (!path) {
      return;
    }
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/files?path=${encodeURIComponent(path)}`);
      targetPath = data.path;
      targetParent = data.parent;
      targetCurrentPath.textContent = data.path;
      targetParentButton.disabled = !data.parent;
      renderTargetList(data.entries || [], data.path);
    } catch (error) {
      targetFileList.innerHTML = `<div class="empty">${escapeHTML(error.message || "Load failed")}</div>`;
    }
  }

  function openFolderPicker() {
    folderPickerOpen = true;
    processPanelOpen = true;
    updateProcessPanelVisibility();
    renderFolderPickerRoots();
  }

  function renderFolderPickerRoots() {
    folderPicker.hidden = false;
    processList.hidden = true;
    folderPickerParent = "";
    folderPickerPath.textContent = "允许访问路径";
    folderBackButton.disabled = true;
    folderPickerList.innerHTML = "";
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    if (roots.length === 0) {
      folderPickerList.innerHTML = '<div class="process-empty">后台未配置允许访问路径</div>';
      return;
    }
    for (const root of roots) {
      folderPickerList.appendChild(folderPickerRow(root, true));
    }
  }

  async function loadFolderPicker(path) {
    if (!path) {
      renderFolderPickerRoots();
      return;
    }
    folderPicker.hidden = false;
    processList.hidden = true;
    folderPickerPath.textContent = baseName(path);
    folderPickerPath.title = path;
    folderPickerList.innerHTML = '<div class="process-empty">Loading...</div>';
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/files?path=${encodeURIComponent(path)}`);
      folderPickerParent = data.parent || "";
      folderBackButton.disabled = false;
      folderPickerList.innerHTML = "";
      folderPickerList.appendChild(folderPickerRow(data.path, true));
      for (const entry of data.entries || []) {
        if (entry.is_dir) {
          folderPickerList.appendChild(folderPickerRow(entry.path, false));
        }
      }
    } catch (error) {
      folderPickerList.innerHTML = `<div class="process-empty">${escapeHTML(error.message || "Load failed")}</div>`;
    }
  }

  function closeFolderPicker() {
    folderPickerOpen = false;
    folderPicker.hidden = true;
    processList.hidden = false;
  }

  function folderPickerRow(path, current) {
    const row = document.createElement("div");
    row.className = "folder-picker-row";
    row.title = path;
    row.innerHTML = `
      <span class="folder-icon">${folderIcon()}</span>
      <span class="folder-name">${escapeHTML(current ? baseName(path) || path : baseName(path))}</span>
      <span class="folder-row-actions">
        <button class="folder-enter" type="button" title="进入" aria-label="进入">${chevronRightIcon()}</button>
        <button class="folder-add" type="button" title="添加" aria-label="添加">${plusIcon()}</button>
      </span>
    `;
    row.querySelector(".folder-enter").addEventListener("click", (event) => {
      event.stopPropagation();
      loadFolderPicker(path);
    });
    row.querySelector(".folder-add").addEventListener("click", (event) => {
      event.stopPropagation();
      addProcessFolder(path);
      closeFolderPicker();
      renderProcessList();
    });
    row.addEventListener("click", () => loadFolderPicker(path));
    return row;
  }

  async function warmupTarget(path) {
    if (!path) {
      return null;
    }
    return fetchJSON(`/cloud-terminal-api/workbench/warmup?path=${encodeURIComponent(path)}`, {
      method: "POST"
    }, 6000);
  }

  function renderTargetList(entries, path) {
    targetFileList.innerHTML = "";
    targetFileList.appendChild(targetRow({
      name: ".",
      path,
      is_dir: true,
      size: 0
    }, true));
    for (const entry of entries) {
      targetFileList.appendChild(targetRow(entry, false));
    }
  }

  function targetRow(entry, currentDir) {
    const button = document.createElement("button");
    const isSelected = selectedTarget && selectedTarget.path === entry.path && ((entry.is_dir && selectedTarget.kind === "dir") || (!entry.is_dir && selectedTarget.kind === "file"));
    button.className = `file-row target-row${isSelected ? " selected" : ""}`;
    button.type = "button";
    button.innerHTML = `
      <span class="file-icon">${entry.is_dir ? folderIcon() : fileIcon()}</span>
      <span class="file-name">${escapeHTML(currentDir ? "当前目录" : entry.name)}</span>
      <span class="file-actions">
        ${entry.is_dir && !currentDir ? '<span class="open-dir">进入</span>' : ""}
        <span class="choose-label">${isSelected ? "已选择" : "启动"}</span>
      </span>
    `;
    button.addEventListener("click", (event) => {
      const action = event.target.closest(".open-dir");
      if (action && entry.is_dir) {
        loadTargetFiles(entry.path);
        return;
      }
      chooseTarget(entry, currentDir);
    });
    return button;
  }

  function chooseTarget(entry) {
    const workDir = entry.is_dir ? entry.path : dirname(entry.path);
    selectedTarget = {
      kind: entry.is_dir ? "dir" : "file",
      path: entry.path,
      workDir,
      label: entry.is_dir ? entry.path : entry.path
    };
    localStorage.setItem(TARGET_KEY, JSON.stringify(selectedTarget));
    workspacePath.textContent = selectedTarget.label;
    currentPath = selectedTarget.workDir;
    addProcessFolder(selectedTarget.workDir, false);
    updateSelectedTarget();
    renderTargetSelectionInList();
  }

  function renderTargetSelectionInList() {
    targetFileList.querySelectorAll(".target-row").forEach((row) => row.classList.remove("selected"));
    loadTargetFiles(targetPath);
  }

  function updateSelectedTarget() {
    startAgentButton.textContent = `Start ${agentLabel(selectedAgent)}`;
    if (!selectedTarget) {
      targetSelection.textContent = "尚未选择";
      startAgentButton.disabled = true;
      return;
    }
    const agent = agentLabel(selectedAgent);
    targetSelection.textContent = selectedTarget.kind === "dir" ? `${agent} · 目录：${selectedTarget.path}` : `${agent} · 文件：${selectedTarget.path}`;
    startAgentButton.textContent = `Start ${agent}`;
    startAgentButton.disabled = !agentEnabled(selectedAgent);
  }

  function renderProcessList() {
    updateProcessPanelVisibility();
    processButton.classList.toggle("active", processPanelOpen);
    const busyCount = sessions.filter((item) => item.busy).length;
    processSummary.textContent = `${busyCount} 执行中 / ${sessions.length} 会话`;
    processList.innerHTML = "";
    const folders = (processFolders.length > 0 ? processFolders : inferFoldersFromSessions()).filter((folder) => !archivedFolders.has(folder));
    if (folders.length === 0) {
      processList.innerHTML = '<div class="process-empty">先选择一个文件夹，或在目录选择器中添加。</div>';
      return;
    }
    for (const folder of folders) {
      const group = document.createElement("section");
      group.className = "process-folder";
      group.title = folder;
      const folderItems = sessions.filter((item) => item.work_dir === folder);
      const visibleSessions = folderItems.filter((item) => !archivedSessions.has(item.id));
      const activeCount = visibleSessions.filter((item) => item.busy).length;
      group.innerHTML = `
        <div class="folder-head">
          <span class="folder-icon">${folderIcon()}</span>
          <span class="folder-main">
            <strong>${escapeHTML(baseName(folder))}</strong>
            <small>${activeCount}/${visibleSessions.length} 执行中</small>
          </span>
          <span class="folder-actions">
            <button class="folder-new icon-only${newTerminalFolder === folder ? " active" : ""}" type="button" aria-label="Open terminal" title="打开终端" aria-expanded="${newTerminalFolder === folder ? "true" : "false"}">
              ${terminalIcon()}
            </button>
            <button class="folder-archive icon-only" type="button" aria-label="Archive folder" title="归档目录">
              ${archiveIcon()}
            </button>
          </span>
        </div>
        <div class="folder-agent-menu" role="menu" hidden></div>
        <div class="folder-sessions"></div>
      `;
      group.querySelector(".folder-new").addEventListener("click", (event) => {
        event.stopPropagation();
        newTerminalFolder = newTerminalFolder === folder ? "" : folder;
        renderProcessList();
      });
      group.querySelector(".folder-archive").addEventListener("click", () => archiveFolder(folder));
      const agentMenu = group.querySelector(".folder-agent-menu");
      renderFolderAgentMenu(agentMenu, folder);
      const list = group.querySelector(".folder-sessions");
      if (visibleSessions.length === 0) {
        list.innerHTML = '<div class="process-empty compact">暂无终端会话</div>';
      } else {
        for (const item of visibleSessions) {
          list.appendChild(processSessionButton(item));
        }
      }
      processList.appendChild(group);
    }
  }

  function renderFolderAgentMenu(container, folder) {
    container.hidden = newTerminalFolder !== folder;
    container.innerHTML = "";
    for (const agent of availableAgents()) {
      const button = document.createElement("button");
      button.type = "button";
      button.disabled = !agent.enabled;
      button.setAttribute("role", "menuitem");
      button.innerHTML = `
        <span class="agent-chip mini">${escapeHTML(agentShortLabel(agent.id))}</span>
        <span>${escapeHTML(agent.label || agentLabel(agent.id))}</span>
      `;
      button.addEventListener("click", (event) => {
        event.stopPropagation();
        openFolderSession(folder, agent.id, true);
      });
      container.appendChild(button);
    }
  }

  function processSessionButton(item) {
    const label = item.agent_label || agentLabel(item.agent);
    const button = document.createElement("div");
    button.className = `process-item ${item.id === sessionID ? "active" : ""} ${item.busy ? "busy" : "done"}`;
    button.innerHTML = `
      <span class="process-main">
        <strong><span class="agent-chip mini">${escapeHTML(agentShortLabel(item.agent))}</span>${escapeHTML(shortSessionID(item.id))}</strong>
        <small>${escapeHTML(item.duration || item.last_active || "")}</small>
      </span>
      <span class="process-status"></span>
      <span class="process-actions">
        <button class="process-archive" type="button" title="归档进程" aria-label="归档进程">${archiveIcon()}</button>
      </span>
    `;
    button.querySelector(".process-archive").addEventListener("click", (event) => {
      event.stopPropagation();
      archiveSession(item.id);
    });
    button.title = `${label} · ${item.id}`;
    button.addEventListener("click", () => {
      if (item.id !== sessionID) {
        attachSession(item.id);
      }
      processPanelOpen = false;
      renderProcessList();
    });
    return button;
  }

  function upsertSession(next) {
    if (!next || !next.id) {
      return;
    }
    next.agent = normalizeAgentID(next.agent || currentSession()?.agent || selectedAgent);
    next.agent_label = next.agent_label || agentLabel(next.agent);
    if (next.work_dir) {
      addProcessFolder(next.work_dir, false);
      if (next.running !== false) {
        rememberFolderSession(next.work_dir, next.agent, next.id);
      } else if (folderSessionID(next.work_dir, next.agent) === next.id) {
        forgetFolderSession(next.work_dir, next.agent);
      }
      localStorage.setItem(FOLDER_SESSION_KEY, JSON.stringify(folderSessions));
    }
    const index = sessions.findIndex((item) => item.id === next.id);
    if (index >= 0) {
      sessions[index] = Object.assign({}, sessions[index], next);
    } else {
      sessions.unshift(next);
    }
    sessions.sort((a, b) => String(b.last_active || "").localeCompare(String(a.last_active || "")));
    renderProcessList();
  }

  function markSessionSubmitted(id) {
    if (!id) {
      return;
    }
    window.clearTimeout(busyTimers.get(id));
    upsertSession({
      id,
      agent: currentSession()?.agent || selectedAgent,
      agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
      work_dir: currentSession()?.work_dir || currentPath || activeWorkDir(),
      busy: true,
      last_active: new Date().toISOString()
    });
    busyTimers.set(id, window.setTimeout(() => markSessionDone(id), 15000));
  }

  function noteSessionOutput(id, data) {
    if (!id || !currentSession()?.busy) {
      return;
    }
    window.clearTimeout(busyTimers.get(id));
    if (looksLikeAgentReady(data)) {
      markSessionDone(id);
      return;
    }
    busyTimers.set(id, window.setTimeout(() => markSessionDone(id), 5000));
  }

  function markSessionDone(id) {
    if (!id) {
      return;
    }
    window.clearTimeout(busyTimers.get(id));
    busyTimers.delete(id);
    upsertSession({
      id,
      agent: currentSession()?.agent || selectedAgent,
      agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
      work_dir: currentSession()?.work_dir || currentPath || activeWorkDir(),
      busy: false,
      last_active: new Date().toISOString()
    });
  }

  function looksLikeAgentReady(value) {
    const text = stripAnsi(String(value || "")).trim();
    if (!text) {
      return false;
    }
    return /(?:›|>)\s*$/.test(text) || /(?:send a message|ask codex|ask claude|ask gemini|type a message)/i.test(text);
  }

  function stripAnsi(value) {
    return String(value)
      .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, "")
      .replace(/\x1b[@-_][0-?]*[ -/]*[@-~]/g, "");
  }

  function currentSession() {
    return sessions.find((item) => item.id === sessionID) || null;
  }

  function activeWorkDir() {
    return currentSession()?.work_dir || selectedTarget?.workDir || state?.work_dir || "";
  }

  function addProcessFolder(path, rerender = true) {
    path = String(path || "").trim();
    if (!path) {
      return;
    }
    archivedFolders.delete(path);
    persistSet(ARCHIVED_FOLDER_KEY, archivedFolders);
    if (!processFolders.includes(path)) {
      processFolders.unshift(path);
      localStorage.setItem(folderKey(), JSON.stringify(processFolders.slice(0, 30)));
    }
    if (rerender) {
      processPanelOpen = true;
      renderProcessList();
    }
  }

  function archiveFolder(path) {
    if (!path) {
      return;
    }
    archivedFolders.add(path);
    persistSet(ARCHIVED_FOLDER_KEY, archivedFolders);
    renderProcessList();
  }

  function archiveSession(id) {
    if (!id) {
      return;
    }
    const archived = sessions.find((item) => item.id === id);
    archivedSessions.add(id);
    persistSet(ARCHIVED_SESSION_KEY, archivedSessions);
    if (archived?.work_dir && folderSessionID(archived.work_dir, archived.agent) === id) {
      forgetFolderSession(archived.work_dir, archived.agent);
      localStorage.setItem(FOLDER_SESSION_KEY, JSON.stringify(folderSessions));
    }
    if (id === sessionID) {
      closeSocketOnly();
      sessionID = "";
      localStorage.removeItem(SESSION_KEY);
      sessionButton.textContent = "No session";
      agentStarted = false;
    }
    renderProcessList();
  }

  function syncFoldersFromSessions() {
    for (const item of sessions) {
      if (item.work_dir) {
        addProcessFolder(item.work_dir, false);
      }
    }
  }

  function inferFoldersFromSessions() {
    return Array.from(new Set(sessions.map((item) => item.work_dir).filter(Boolean)));
  }

  function updateProcessPanelVisibility() {
    processPanel.hidden = false;
    processBackdrop.hidden = false;
    processPanel.classList.toggle("open", processPanelOpen);
    processBackdrop.classList.toggle("open", processPanelOpen);
    if (!processPanelOpen) {
      newTerminalFolder = "";
      closeFolderPicker();
      window.setTimeout(() => {
        if (!processPanelOpen) {
          processPanel.hidden = true;
          processBackdrop.hidden = true;
        }
      }, 180);
    }
  }

  function closeActionMenu() {
    actionMenu.hidden = true;
  }

  function sendInput(data) {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    if (data === "\r" || data === "\n") {
      markSessionSubmitted(sessionID);
    } else if (data === "\u0003" || data === "\u0004") {
      markSessionDone(sessionID);
    }
    socket.send(JSON.stringify({ type: "input", data }));
  }

  function sendResize() {
    if (!socket || socket.readyState !== WebSocket.OPEN || !terminal) {
      return;
    }
    socket.send(JSON.stringify({ type: "resize", rows: terminal.rows, cols: terminal.cols }));
  }

  function sendShortcut(key) {
    const values = {
      esc: "\u001b",
      tab: "\t",
      up: "\u001b[A",
      down: "\u001b[B",
      left: "\u001b[D",
      right: "\u001b[C",
      "ctrl-c": "\u0003",
      "ctrl-d": "\u0004"
    };
    sendInput(values[key] || "");
    terminal.focus();
  }

  function installMobileInputFallback() {
    const textarea = terminal?.textarea;
    if (!textarea || textarea.dataset.mobileInputFallback === "true") {
      return;
    }
    textarea.dataset.mobileInputFallback = "true";
    textarea.autocapitalize = "none";
    textarea.autocomplete = "off";
    textarea.autocorrect = "off";
    textarea.spellcheck = false;
    textarea.inputMode = "text";

    let composing = false;
    textarea.addEventListener("compositionstart", () => {
      composing = true;
    });
    textarea.addEventListener("compositionend", () => {
      composing = false;
      window.setTimeout(() => {
        textarea.value = "";
      }, 0);
    });
    textarea.addEventListener("beforeinput", (event) => {
      const data = event.data || "";
      if (!data || composing || !isMobileSymbolInput(data) || recentXtermInput(data)) {
        return;
      }
      event.preventDefault();
      sendInput(data);
      textarea.value = "";
    });
    textarea.addEventListener("input", () => {
      if (composing) {
        return;
      }
      const data = textarea.value || "";
      if (data && isMobileSymbolInput(data) && !recentXtermInput(data)) {
        sendInput(data);
      }
      textarea.value = "";
    });
  }

  function isMobileSymbolInput(value) {
    return [...value].every((char) => MOBILE_SYMBOLS.includes(char));
  }

  function recentXtermInput(value) {
    return lastXtermInput.data === value && Date.now() - lastXtermInput.at < 80;
  }

  function fitTerminal() {
    if (!terminal || !fitAddon || activeTab !== "terminal" || targetView.hidden === false) {
      return;
    }
    window.requestAnimationFrame(() => {
      fitAddon.fit();
      resizeTerminalToVisibleArea();
      sendResize();
      scrollTerminalToBottom();
    });
  }

  function resizeTerminalToVisibleArea() {
    const element = terminal?.element;
    const cell = terminal?._core?._renderService?.dimensions?.css?.cell;
    if (!element || !cell || !cell.width || !cell.height) {
      return false;
    }
    const rect = terminalEl.getBoundingClientRect();
    if (!rect.width || !rect.height) {
      return false;
    }
    const style = window.getComputedStyle(terminalEl);
    const contentWidth = rect.width - parseFloat(style.paddingLeft || "0") - parseFloat(style.paddingRight || "0");
    const contentHeight = rect.height - parseFloat(style.paddingTop || "0") - parseFloat(style.paddingBottom || "0");
    const scrollbarWidth = terminal.options.scrollback === 0 ? 0 : terminal._core?.viewport?.scrollBarWidth || 0;
    const cols = Math.max(2, Math.floor((contentWidth - scrollbarWidth) / cell.width));
    const rows = Math.max(1, Math.floor(contentHeight / cell.height));
    if (cols === terminal.cols && rows === terminal.rows) {
      return false;
    }
    terminal._core?._renderService?.clear?.();
    terminal.resize(cols, rows);
    return true;
  }

  async function loadFiles(path) {
    if (!path) {
      return;
    }
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/files?path=${encodeURIComponent(path)}`);
      currentPath = data.path;
      state.parentPath = data.parent;
      currentPathEl.textContent = data.path;
      parentButton.disabled = !data.parent;
      renderFileList(data.entries || []);
    } catch (error) {
      fileList.innerHTML = `<div class="empty">${escapeHTML(error.message || "Load failed")}</div>`;
    }
  }

  function renderFileList(entries) {
    if (entries.length === 0) {
      fileList.innerHTML = '<div class="empty">Empty directory</div>';
      return;
    }
    fileList.innerHTML = "";
    for (const entry of entries) {
      const button = document.createElement("button");
      button.className = "file-row";
      button.type = "button";
      button.innerHTML = `
        <span class="file-icon">${entry.is_dir ? folderIcon() : fileIcon()}</span>
        <span class="file-name">${escapeHTML(entry.name)}</span>
        <span class="file-meta">${entry.is_dir ? "Directory" : formatBytes(entry.size)}</span>
      `;
      button.addEventListener("click", () => {
        if (entry.is_dir) {
          fileViewer.hidden = true;
          loadFiles(entry.path);
        } else {
          openFile(entry.path);
        }
      });
      fileList.appendChild(button);
    }
  }

  async function openFile(path) {
    selectedFile = path;
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/file?path=${encodeURIComponent(path)}`);
      viewerTitle.textContent = data.name + (data.truncated ? " · truncated" : "");
      fileContent.textContent = data.content;
      fileViewer.hidden = false;
      fileViewer.scrollIntoView({ block: "nearest" });
    } catch (error) {
      viewerTitle.textContent = "Load failed";
      fileContent.textContent = error.message || "Load failed";
      fileViewer.hidden = false;
    }
  }

  async function loadDiff(path) {
    const params = new URLSearchParams({ work_dir: activeWorkDir() || currentPath || "" });
    if (path) {
      params.set("path", path);
      diffScope.textContent = path;
    } else {
      diffScope.textContent = "Workspace diff";
    }
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/diff?${params.toString()}`);
      diffOutput.textContent = [data.error, data.status, data.stat, data.diff].filter(Boolean).join("\n\n") || "No changes.";
    } catch (error) {
      diffOutput.textContent = error.message || "Load failed";
    }
  }

  async function fetchJSON(url, options, timeout = 10000) {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), timeout);
    let response;
    try {
      response = await fetch(url, Object.assign({ credentials: "same-origin", signal: controller.signal }, options || {}));
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

  function activateTab(tab) {
    if (!agentStarted && tab !== "terminal") {
      return;
    }
    activeTab = tab;
    document.querySelectorAll("[data-page]").forEach((page) => {
      page.classList.toggle("active", page.dataset.page === tab);
    });
    document.querySelectorAll("[data-tab]").forEach((button) => {
      button.classList.toggle("active", button.dataset.tab === tab);
    });
    if (tab === "terminal") {
      fitTerminal();
      if (terminal) {
        terminal.focus();
        scrollTerminalToBottom();
      }
    }
    if (tab === "preview" && !previewFrame.src) {
      openPreview();
    }
  }

  function renderPreviewPorts() {
    const ports = Array.isArray(state.preview_ports) ? state.preview_ports : [];
    previewPort.innerHTML = "";
    for (const port of ports) {
      const option = document.createElement("option");
      option.value = String(port);
      option.textContent = `localhost:${port}`;
      previewPort.appendChild(option);
    }
    openPreviewButton.disabled = ports.length === 0;
    refreshPreviewButton.disabled = ports.length === 0;
  }

  function openPreview(forceReload) {
    const port = previewPort.value;
    if (!port) {
      return;
    }
    const url = `/preview/${encodeURIComponent(port)}/`;
    previewFrame.src = forceReload ? `${url}?_=${Date.now()}` : url;
  }

  function setConnection(value) {
    connectionState.textContent = value;
    connectionState.dataset.state = value.toLowerCase();
    stopSessionButton.disabled = value === "Finished" || value === "Detached" || value === "Choose target";
  }

  function setAuthBusy(busy) {
    tokenInput.disabled = busy;
    authForm.querySelector("button").disabled = busy;
  }

  function setAuthMessage(value) {
    authMessage.textContent = value;
  }

  function setWorkbenchTabsEnabled(enabled) {
    document.querySelectorAll("[data-tab]").forEach((button) => {
      button.disabled = !enabled && button.dataset.tab !== "terminal";
    });
    reconnectButton.disabled = !sessionID;
    stopSessionButton.disabled = !enabled || !sessionID;
  }

  function renderAgentSelector() {
    const agents = availableAgents();
    agentSelector.innerHTML = "";
    for (const agent of agents) {
      const button = document.createElement("button");
      button.type = "button";
      button.dataset.agent = agent.id;
      button.className = "agent-option";
      button.disabled = !agent.enabled;
      button.classList.toggle("active", agent.id === selectedAgent);
      button.title = agent.enabled ? agent.label : `${agent.label} 未启用`;
      button.innerHTML = `
        <span class="agent-mark">${escapeHTML(agentShortLabel(agent.id))}</span>
        <span>${escapeHTML(agent.label || agentLabel(agent.id))}</span>
      `;
      agentSelector.appendChild(button);
    }
    updateSelectedTarget();
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

  function setSelectedAgent(agent) {
    selectedAgent = normalizeAgentID(agent);
    localStorage.setItem(ACTIVE_AGENT_KEY, selectedAgent);
    renderAgentSelector();
    renderProcessList();
  }

  function agentEnabled(agent) {
    const id = normalizeAgentID(agent);
    return availableAgents().some((item) => item.id === id && item.enabled);
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
    return "CX";
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

  function sessionMatchesFolderAgent(item, folder, agent) {
    return item.work_dir === folder && normalizeAgentID(item.agent) === normalizeAgentID(agent);
  }

  function folderSessionID(folder, agent) {
    const value = folderSessions[folder];
    if (!value) {
      return "";
    }
    if (typeof value === "string") {
      return normalizeAgentID(agent) === "codex" ? value : "";
    }
    return value[normalizeAgentID(agent)] || "";
  }

  function rememberFolderSession(folder, agent, id) {
    if (!folder) {
      return;
    }
    const current = folderSessions[folder];
    if (!current || typeof current === "string") {
      folderSessions[folder] = current ? { codex: current } : {};
    }
    folderSessions[folder][normalizeAgentID(agent)] = id;
  }

  function forgetFolderSession(folder, agent) {
    const current = folderSessions[folder];
    if (!current) {
      return;
    }
    if (typeof current === "string") {
      if (normalizeAgentID(agent) === "codex") {
        delete folderSessions[folder];
      }
      return;
    }
    delete current[normalizeAgentID(agent)];
    if (Object.keys(current).length === 0) {
      delete folderSessions[folder];
    }
  }

  function migrateFolderSessions(value) {
    const migrated = {};
    for (const [folder, sessionsByAgent] of Object.entries(value || {})) {
      if (typeof sessionsByAgent === "string") {
        migrated[folder] = { codex: sessionsByAgent };
      } else if (sessionsByAgent && typeof sessionsByAgent === "object" && !Array.isArray(sessionsByAgent)) {
        migrated[folder] = {};
        for (const [agent, id] of Object.entries(sessionsByAgent)) {
          if (typeof id === "string" && id) {
            migrated[folder][normalizeAgentID(agent)] = id;
          }
        }
      }
    }
    return migrated;
  }

  function closeSocketOnly() {
    clearTimeout(reconnectTimer);
    socketSeq += 1;
    manualClose = true;
    if (socket) {
      socket.close();
    }
    socket = null;
    manualClose = false;
  }

  function writeTerminal(value) {
    if (terminal && value) {
      terminal.write(value);
      scrollTerminalToBottom();
    }
  }

  function scrollTerminalToBottom() {
    if (!terminal) {
      return;
    }
    window.requestAnimationFrame(() => {
      terminal.scrollToBottom();
    });
  }

  function writeTerminalError(value) {
    writeTerminal(`\r\n\x1b[31m${value}\x1b[0m\r\n`);
  }

  function shortSession(value) {
    return value ? `Session ${value.slice(0, 8)}` : "No session";
  }

  function shortSessionID(value) {
    return value ? value.slice(0, 8) : "session";
  }

  function baseName(value) {
    if (!value) {
      return "-";
    }
    const cleaned = String(value).replace(/\/+$/, "");
    const index = cleaned.lastIndexOf("/");
    return index >= 0 ? cleaned.slice(index + 1) || cleaned : cleaned;
  }

  function isSmallScreen() {
    return window.matchMedia("(max-width: 720px)").matches;
  }

  function formatBytes(value) {
    if (value < 1024) {
      return `${value} B`;
    }
    if (value < 1024 * 1024) {
      return `${(value / 1024).toFixed(1)} KB`;
    }
    return `${(value / 1024 / 1024).toFixed(1)} MB`;
  }

  function dirname(path) {
    const index = path.lastIndexOf("/");
    if (index <= 0) {
      return "/";
    }
    return path.slice(0, index);
  }

  function readSavedTarget() {
    try {
      const value = JSON.parse(localStorage.getItem(TARGET_KEY) || "null");
      return value && value.path && value.workDir ? value : null;
    } catch {
      return null;
    }
  }

  function readSavedFolders() {
    try {
      const value = JSON.parse(localStorage.getItem(folderKey()) || "[]");
      return Array.isArray(value) ? value.filter((item) => typeof item === "string" && item.trim()) : [];
    } catch {
      return [];
    }
  }

  function folderKey() {
    return FOLDER_KEY;
  }

  function readStringSet(key) {
    try {
      const value = JSON.parse(localStorage.getItem(key) || "[]");
      return new Set(Array.isArray(value) ? value.filter((item) => typeof item === "string" && item.trim()) : []);
    } catch {
      return new Set();
    }
  }

  function readObject(key) {
    try {
      const value = JSON.parse(localStorage.getItem(key) || "{}");
      return value && typeof value === "object" && !Array.isArray(value) ? value : {};
    } catch {
      return {};
    }
  }

  function persistSet(key, value) {
    localStorage.setItem(key, JSON.stringify(Array.from(value)));
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

  function folderIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6.8A2.8 2.8 0 0 1 5.8 4h4.5l2 2.2h5.9A2.8 2.8 0 0 1 21 9v7.2a2.8 2.8 0 0 1-2.8 2.8H5.8A2.8 2.8 0 0 1 3 16.2V6.8Z"/></svg>';
  }

  function fileIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h8.3L19 7.7V19a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm7.5 1.8V8H17"/></svg>';
  }

  function terminalIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 7 4 4-4 4M11 17h8"/></svg>';
  }

  function plusIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>';
  }

  function chevronRightIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>';
  }

  function archiveIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M6 7l1 13h10l1-13M9 11h6M8 4h8l1 3H7l1-3Z"/></svg>';
  }
})();
