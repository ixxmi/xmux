(function () {
  const SESSION_KEY = "cloud-terminal-mobile-session";
  const TARGET_KEY = "cloud-terminal-mobile-target";
  const FOLDER_KEY = "cloud-terminal-mobile-folders";
  const FOLDER_SESSION_KEY = "cloud-terminal-mobile-folder-sessions";
  const ARCHIVED_FOLDER_KEY = "cloud-terminal-mobile-archived-folders";
  const ARCHIVED_SESSION_KEY = "cloud-terminal-mobile-archived-sessions";
  const FORGOTTEN_FOLDER_KEY = "cloud-terminal-mobile-forgotten-folders";
  const FORGOTTEN_SESSION_KEY = "cloud-terminal-mobile-forgotten-sessions";
  const SESSION_NAME_KEY = "cloud-terminal-mobile-session-names";
  const ACTIVE_AGENT_KEY = "cloud-terminal-mobile-agent";
  const MOBILE_SYMBOLS = "~!@#$%^&*()_+-=[]{}\\|;:'\",.<>/?`！￥……（）【】《》、，。？；：‘’“”·— 　";
  const FULLWIDTH_SPACE = "　";
  const appPath = window.XMuxPath?.path || ((path) => path);
  const websocketURL = window.XMuxPath?.websocketURL || ((path) => {
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${protocol}//${window.location.host}${path}`;
  });

  const mobileApp = document.querySelector(".mobile-app");
  const authView = document.getElementById("authView");
  const workbenchView = document.getElementById("workbenchView");
  const targetView = document.getElementById("targetView");
  const authForm = document.getElementById("authForm");
  const loginModeButton = document.getElementById("loginModeButton");
  const registerModeButton = document.getElementById("registerModeButton");
  const forgotModeLink = document.getElementById("forgotModeLink");
  const googleAuthButton = document.getElementById("googleAuthButton");
  const usernameLabel = document.getElementById("usernameLabel");
  const usernameInput = document.getElementById("usernameInput");
  const passwordLabel = document.getElementById("passwordLabel");
  const passwordInput = document.getElementById("passwordInput");
  const forgotEmailLabel = document.getElementById("forgotEmailLabel");
  const forgotEmailInput = document.getElementById("forgotEmailInput");
  const registerEmailLabel = document.getElementById("registerEmailLabel");
  const registerEmailInput = document.getElementById("registerEmailInput");
  const registerEmailRow = document.getElementById("registerEmailRow");
  const registerCodeLabel = document.getElementById("registerCodeLabel");
  const registerCodeInput = document.getElementById("registerCodeInput");
  const registerSendCodeButton = document.getElementById("registerSendCodeButton");
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
  const tunnelBlockedView = document.getElementById("tunnelBlockedView");
  const reloadStateButton = document.getElementById("reloadStateButton");
  const addFolderButton = document.getElementById("addFolderButton");
  const folderPicker = document.getElementById("folderPicker");
  const folderPickerPath = document.getElementById("folderPickerPath");
  const folderPickerList = document.getElementById("folderPickerList");
  const folderBackButton = document.getElementById("folderBackButton");
  const folderCancelButton = document.getElementById("folderCancelButton");
  const terminalPage = document.getElementById("terminalPage");
  const terminalEl = document.getElementById("terminal");
  const keybar = document.getElementById("keybar");
  const quickbar = document.getElementById("quickbar");
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
  const rootsButton = document.getElementById("rootsButton");
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
  const accountAvatar = document.getElementById("accountAvatar");
  const settingsAccount = document.getElementById("settingsAccount");
  const settingsRole = document.getElementById("settingsRole");
  const logoutButton = document.getElementById("logoutButton");
  const currentPasswordInput = document.getElementById("currentPasswordInput");
  const newPasswordInput = document.getElementById("newPasswordInput");
  const profileMessage = document.getElementById("profileMessage");
  const saveProfileButton = document.getElementById("saveProfileButton");
  const togglePasswordButton = document.getElementById("togglePasswordButton");
  const cancelProfileButton = document.getElementById("cancelProfileButton");
  const passwordForm = document.getElementById("passwordForm");
  const archiveCountEl = document.getElementById("archiveCount");
  const archivedFoldersList = document.getElementById("archivedFoldersList");
  const archivedSessionsList = document.getElementById("archivedSessionsList");

  const settingsMainPage = document.getElementById("settingsMainPage");
  const settingsAccountPage = document.getElementById("settingsAccountPage");
  const settingsArchivePage = document.getElementById("settingsArchivePage");
  const accountTrigger = document.getElementById("accountTrigger");
  const gotoArchiveButton = document.getElementById("gotoArchiveButton");
  const backFromAccountButton = document.getElementById("backFromAccountButton");
  const backFromArchiveButton = document.getElementById("backFromArchiveButton");

  let state = null;
  let currentAccount = null;
  let terminal = null;
  let fitAddon = null;
  let socket = null;
  let sessionID = "";
  let selectedTarget = null;
  let selectedAgent = "codex";
  let currentPath = "";
  let targetPath = "";
  let targetParent = "";
  let selectedFile = "";
  let activeTab = "terminal";
  let reconnectTimer = 0;
  let manualClose = false;
  let authMode = "login";
  let reconnectAttempts = 0;
  let socketSeq = 0;
  let agentStarted = false;
  let workbenchTabsEnabled = false;
  let sessions = [];
  let processFolders = [];
  let folderSessions = {};
  let archivedFolders = new Set();
  let archivedSessions = new Set();
  let forgottenFolders = new Set();
  let forgottenSessions = new Set();
  let sessionNames = {};
  let quickCommands = [];
  let quickCommandsLoaded = false;
  let quickCommandsSaveTimer = 0;
  const sessionInputBuffers = new Map();
  const submittedSessions = new Set();
  let processPanelOpen = false;

  function initUserContext() {
    sessionID = localStorage.getItem(userKey(SESSION_KEY)) || "";
    selectedAgent = normalizeAgentID(localStorage.getItem(userKey(ACTIVE_AGENT_KEY)) || "codex");
    processFolders = readSavedFolders();
    folderSessions = migrateFolderSessions(readObject(FOLDER_SESSION_KEY));
    archivedFolders = readStringSet(ARCHIVED_FOLDER_KEY);
    archivedSessions = readStringSet(ARCHIVED_SESSION_KEY);
    forgottenFolders = readStringSet(FORGOTTEN_FOLDER_KEY);
    forgottenSessions = readStringSet(FORGOTTEN_SESSION_KEY);
    sessionNames = readObject(SESSION_NAME_KEY) || {};
    selectedTarget = readSavedTarget();
  }
  let folderPickerOpen = false;
  let folderPickerParent = "";
  let newTerminalFolder = "";
  let pendingContinuationInput = "";
  let continuingHistoricalSession = false;
  let archiveSyncTimer = 0;
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
    if (authMode === "forgot") {
      const email = forgotEmailInput.value.trim();
      if (!email) {
        setAuthMessage("请填写邮箱");
        return;
      }
      setAuthBusy(true);
      setAuthMessage("提交中...");
      try {
        const response = await fetch(appPath("/cloud-terminal-api/accounts/forgot-password"), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          credentials: "same-origin",
          body: JSON.stringify({ email }),
        });
        if (!response.ok) {
          throw new Error(await response.text());
        }
        setAuthMessage("如该邮箱存在，已发送重置链接，请查收。");
      } catch (error) {
        setAuthMessage(error.message || "提交失败");
      } finally {
        setAuthBusy(false);
      }
      return;
    }
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    if (!username || !password) {
      setAuthMessage("Account and password are required.");
      return;
    }
    setAuthBusy(true);
    setAuthMessage("Verifying...");
    try {
      if (authMode === "register") {
        const email = registerEmailInput ? registerEmailInput.value.trim() : "";
        const code = registerCodeInput ? registerCodeInput.value.trim() : "";
        if (!email) {
          setAuthMessage("请填写邮箱");
          return;
        }
        if (!code) {
          setAuthMessage("请填写邮箱验证码");
          return;
        }
        const outcome = await registerAccount(username, password, email, code);
        currentAccount = { username: outcome.username || username, role: outcome.role || "user" };
        state = outcome.state || outcome;
      } else {
        const result = await loginAccount(username, password);
        currentAccount = { username: result.username || username, role: result.role || "user" };
        state = result.state || result;
      }
      passwordInput.value = "";
      showWorkbench();
    } catch (error) {
      setAuthMessage(error.message || "Login rejected.");
    } finally {
      setAuthBusy(false);
    }
  });

  loginModeButton.addEventListener("click", () => setAuthMode("login"));
  registerModeButton.addEventListener("click", () => setAuthMode("register"));
  if (forgotModeLink) {
    forgotModeLink.addEventListener("click", (event) => {
      event.preventDefault();
      setAuthMode(authMode === "forgot" ? "login" : "forgot");
    });
  }
  if (googleAuthButton) {
    googleAuthButton.innerHTML = mobileGoogleButtonContent();
    googleAuthButton.addEventListener("click", () => {
      const startURL = googleStartURL("/mobile/");
      console.log("[OAUTH-CLIENT] google button clicked", { start_url: startURL });
      window.location.href = startURL;
    });
  }
  if (registerSendCodeButton) {
    let codeCooldownTimer = 0;
    registerSendCodeButton.addEventListener("click", async () => {
      const email = registerEmailInput ? registerEmailInput.value.trim() : "";
      if (!email) {
        setAuthMessage("请先填写邮箱");
        registerEmailInput?.focus();
        return;
      }
      registerSendCodeButton.disabled = true;
      setAuthMessage("验证码发送中...");
      try {
        await requestRegistrationCode(email);
        setAuthMessage("验证码已发送，请到邮箱查收（10 分钟内有效）");
        let remaining = 60;
        const original = "发送验证码";
        window.clearInterval(codeCooldownTimer);
        registerSendCodeButton.textContent = `${remaining}s`;
        codeCooldownTimer = window.setInterval(() => {
          remaining -= 1;
          if (remaining <= 0) {
            window.clearInterval(codeCooldownTimer);
            registerSendCodeButton.disabled = false;
            registerSendCodeButton.textContent = original;
          } else {
            registerSendCodeButton.textContent = `${remaining}s`;
          }
        }, 1000);
      } catch (error) {
        setAuthMessage(error.message || "验证码发送失败");
        registerSendCodeButton.disabled = false;
      }
    });
  }
  loadMobilePublicConfig();

  startAgentButton.addEventListener("click", async () => {
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }
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
    const existing = currentSession();
    if (existing && existing.running === false) {
      if (existing.work_dir) {
        openFolderSession(existing.work_dir, existing.agent || selectedAgent, true);
      } else {
        showTargetPicker();
      }
      return;
    }
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }
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
      if (currentSession()?.running === false) {
        return;
      }
      socket.send(JSON.stringify({ type: "stop" }));
      setConnection("Stopping");
      if (submittedSessions.has(sessionID) || currentSession()?.submitted) {
        markSessionDone(sessionID);
        upsertSession({
          id: sessionID,
          agent: currentSession()?.agent || selectedAgent,
          agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
          work_dir: currentSession()?.work_dir || currentPath || activeWorkDir(),
          submitted: true,
          running: false,
          busy: false,
          last_active: new Date().toISOString()
        });
      }
    }
  });

  newSessionButton.addEventListener("click", () => {
    closeActionMenu();
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }
    closeSocketOnly();
    sessionID = "";
    agentStarted = false;
    sessionButton.textContent = "No session";
    showTargetPicker();
  });

  processButton.addEventListener("click", () => {
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }
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

  folderCancelButton.addEventListener("click", () => closeFolderPicker());

  togglePasswordButton.addEventListener("click", () => {
    const willShow = passwordForm.hidden;
    if (willShow) {
      currentPasswordInput.value = "";
      newPasswordInput.value = "";
      profileMessage.textContent = "";
      profileMessage.className = "settings-message";
      passwordForm.hidden = false;
      togglePasswordButton.hidden = true;
      setTimeout(() => currentPasswordInput.focus(), 0);
    }
  });

  cancelProfileButton.addEventListener("click", () => closePasswordForm());

  accountTrigger.addEventListener("click", () => {
    settingsMainPage.hidden = true;
    settingsAccountPage.hidden = false;
  });

  gotoArchiveButton.addEventListener("click", () => {
    renderArchivePanel();
    settingsMainPage.hidden = true;
    settingsArchivePage.hidden = false;
  });

  backFromAccountButton.addEventListener("click", () => {
    settingsAccountPage.hidden = true;
    settingsMainPage.hidden = false;
  });

  backFromArchiveButton.addEventListener("click", () => {
    settingsArchivePage.hidden = true;
    settingsMainPage.hidden = false;
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
    } else {
      renderAccessibleRoots();
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
    } else {
      renderFileRoots();
    }
  });

  rootsButton.addEventListener("click", () => renderFileRoots());

  refreshFilesButton.addEventListener("click", () => {
    if (currentPath) {
      loadFiles(currentPath);
    } else {
      renderFileRoots();
    }
  });
  refreshDiffButton.addEventListener("click", () => loadDiff(""));
  openPreviewButton.addEventListener("click", () => openPreview());
  refreshPreviewButton.addEventListener("click", () => openPreview(true));
  logoutButton.addEventListener("click", logoutAccount);
  saveProfileButton.addEventListener("click", saveProfile);
  reloadStateButton.addEventListener("click", refreshWorkbenchState);
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
    try {
      const account = await fetchJSON("/cloud-terminal-api/accounts/me", null, 5000);
      currentAccount = { username: account.username, role: account.role || "user" };
      initUserContext();
      state = await fetchJSON("/cloud-terminal-api/workbench/state", null, 5000);
      showWorkbench();
    } catch {
      authView.hidden = false;
      workbenchView.hidden = true;
      usernameInput.focus();
    }
  }

  function setAuthMode(mode) {
    authMode = mode;
    loginModeButton.classList.toggle("active", mode === "login");
    registerModeButton.classList.toggle("active", mode === "register");
    if (forgotModeLink) {
      forgotModeLink.textContent = mode === "forgot" ? "返回登录" : "忘记密码？";
    }
    const credentialsHidden = mode === "forgot";
    usernameLabel.hidden = credentialsHidden;
    usernameInput.hidden = credentialsHidden;
    passwordLabel.hidden = credentialsHidden;
    passwordInput.hidden = credentialsHidden;
    if (forgotEmailLabel && forgotEmailInput) {
      forgotEmailLabel.hidden = !credentialsHidden;
      forgotEmailInput.hidden = !credentialsHidden;
    }
    if (registerEmailLabel && registerEmailRow) {
      const showEmail = mode === "register";
      registerEmailLabel.hidden = !showEmail;
      registerEmailRow.hidden = !showEmail;
      if (registerCodeLabel) registerCodeLabel.hidden = !showEmail;
      if (registerCodeInput) registerCodeInput.hidden = !showEmail;
    }
    const submit = authForm.querySelector("button[type='submit']");
    if (mode === "register") {
      submit.textContent = "创建账号";
    } else if (mode === "forgot") {
      submit.textContent = "发送重置链接";
    } else {
      submit.textContent = "登录";
    }
    if (googleAuthButton) {
      googleAuthButton.hidden = mode !== "login" || !googleAuthButton.dataset.enabled;
    }
    setAuthMessage("");
    if (mode === "forgot") {
      forgotEmailInput?.focus();
    } else {
      usernameInput.focus();
    }
  }

  async function loadMobilePublicConfig() {
    if (!googleAuthButton) {
      return;
    }
    try {
      const response = await fetch(appPath("/cloud-terminal-api/auth/public-config"), { credentials: "same-origin" });
      if (response.ok) {
        const cfg = await response.json();
        if (cfg.oauth_google_enabled) {
          googleAuthButton.dataset.enabled = "1";
          googleAuthButton.hidden = authMode !== "login";
        }
      }
    } catch (_) {
      /* ignore */
    }
  }

  function mobileGoogleButtonContent() {
    return `<svg viewBox="0 0 24 24" aria-hidden="true">
      <path fill="#4285F4" d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.75h3.57c2.08-1.92 3.28-4.74 3.28-8.07z"/>
      <path fill="#34A853" d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.75c-.99.66-2.25 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"/>
      <path fill="#FBBC05" d="M5.84 14.09a6.93 6.93 0 0 1 0-4.18V7.07H2.18a11 11 0 0 0 0 9.86l3.66-2.84z"/>
      <path fill="#EA4335" d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84C6.71 7.31 9.14 5.38 12 5.38z"/>
    </svg><span>使用 Google 登录</span>`;
  }

  function googleStartURL(returnPath) {
    const params = new URLSearchParams({ return_to: appPath(returnPath) });
    return `${appPath("/cloud-terminal-api/accounts/oauth/google/start")}?${params.toString()}`;
  }

  async function loginAccount(username, password) {
    return fetchJSON("/cloud-terminal-api/accounts/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password })
    }, 8000);
  }

  async function registerAccount(username, password, email, code) {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 8000);
    let response;
    try {
      response = await fetch(appPath("/cloud-terminal-api/accounts/register"), {
        method: "POST",
        credentials: "same-origin",
        signal: controller.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password, email, code }),
      });
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

  async function requestRegistrationCode(email) {
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 8000);
    try {
      const response = await fetch(appPath("/cloud-terminal-api/accounts/register/send-code"), {
        method: "POST",
        credentials: "same-origin",
        signal: controller.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });
      if (!response.ok) {
        const detail = (await response.text()).trim();
        throw new Error(detail || `HTTP ${response.status}`);
      }
      return response.json();
    } finally {
      window.clearTimeout(timer);
    }
  }

  async function logoutAccount() {
    await fetchJSON("/cloud-terminal-api/accounts/logout", { method: "POST" }, 5000).catch(() => null);
    closeSocketOnly();
    sessionID = "";
    state = null;
    currentAccount = null;
    quickCommands = [];
    quickCommandsLoaded = false;
    if (quickbar) {
      quickbar.innerHTML = "";
    }
    localStorage.removeItem(userKey(SESSION_KEY));
    localStorage.removeItem(userKey(TARGET_KEY));
    authView.hidden = false;
    workbenchView.hidden = true;
    passwordInput.value = "";
    setAuthMessage("");
    usernameInput.focus();
  }

  function renderSettingsShell() {
    const username = currentAccount?.username || "-";
    const role = currentAccount?.role || "-";
    settingsAccount.textContent = username;
    settingsRole.textContent = role === "admin" ? "管理员" : "用户";
    accountAvatar.textContent = avatarText(username);
    updateArchiveCount();
  }

  async function saveProfile() {
    const currentPassword = currentPasswordInput.value;
    const newPassword = newPasswordInput.value;
    profileMessage.className = "settings-message";
    if (!currentPassword || !newPassword) {
      profileMessage.textContent = "请输入当前密码和新密码";
      profileMessage.classList.add("error");
      return;
    }
    saveProfileButton.disabled = true;
    profileMessage.textContent = "保存中...";
    try {
      await fetchJSON("/cloud-terminal-api/accounts/me", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
      }, 8000);
      profileMessage.textContent = "账号信息已更新";
      profileMessage.classList.add("ok");
      setTimeout(closePasswordForm, 800);
    } catch (error) {
      profileMessage.textContent = error.message || "保存失败";
      profileMessage.classList.add("error");
    } finally {
      saveProfileButton.disabled = false;
    }
  }

  async function refreshWorkbenchState() {
    try {
      state = await fetchJSON("/cloud-terminal-api/workbench/state", null, 5000);
      applyArchiveState(state);
      sessions = normalizeStateSessions(state.sessions);
      hydrateArchivedSessionMetadata(state.archived_session_items);
      syncFoldersFromSessions();
      ensureSelectedAgent();
      renderAgentSelector();
      renderPreviewPorts();
      renderProcessList();
      updateArchiveCount();
      showWorkbench();
    } catch (error) {
      setConnection(error.message || "Refresh failed");
    }
  }

  function showWorkbench() {
    authView.hidden = true;
    workbenchView.hidden = false;
    initUserContext();
    applyArchiveState(state);
    sessions = normalizeStateSessions(state.sessions);
    hydrateArchivedSessionMetadata(state?.archived_session_items);
    ensureSelectedAgent();
    reconcileSavedTargetWithState();
    currentPath = selectedTarget?.workDir || state.work_dir;
    workspacePath.textContent = currentPath || "";
    syncFoldersFromSessions();
    renderAgentSelector();
    renderPreviewPorts();
    renderProcessList();
    renderSettingsShell();
    loadQuickCommandsOnce();
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }
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

  function normalizeStateSessions(items) {
    submittedSessions.clear();
    return (Array.isArray(items) ? items : [])
      .filter((item) => item && item.submitted !== false && !forgottenSessions.has(item.id))
      .map((item) => {
        submittedSessions.add(item.id);
        return Object.assign({ submitted: true }, item);
      });
  }

  function applyArchiveState(nextState) {
    if (!nextState || typeof nextState !== "object") {
      return;
    }
    archivedFolders = mergeRemoteSet(ARCHIVED_FOLDER_KEY, archivedFolders, nextState.archived_folders, true);
    archivedSessions = mergeRemoteSet(ARCHIVED_SESSION_KEY, archivedSessions, nextState.archived_sessions, false);
    forgottenFolders = mergeRemoteSet(FORGOTTEN_FOLDER_KEY, forgottenFolders, nextState.forgotten_folders, true);
    forgottenSessions = mergeRemoteSet(FORGOTTEN_SESSION_KEY, forgottenSessions, nextState.forgotten_sessions, false);
    for (const folder of forgottenFolders) {
      archivedFolders.delete(folder);
    }
    for (const id of forgottenSessions) {
      archivedSessions.delete(id);
    }
    persistArchiveState();
    updateArchiveCount();
  }

  function mergeRemoteSet(key, local, remote, paths) {
    const merged = new Set(local || []);
    for (const item of Array.isArray(remote) ? remote : []) {
      const value = paths ? normalizeArchivePath(item) : normalizeArchiveID(item);
      if (value) {
        merged.add(value);
      }
    }
    persistSet(key, merged);
    return merged;
  }

  function hydrateArchivedSessionMetadata(items) {
    if (!Array.isArray(items)) {
      return;
    }
    for (const item of items) {
      if (!item?.id || sessions.some((session) => session.id === item.id)) {
        continue;
      }
      sessions.push(Object.assign({ submitted: true, running: false }, item));
    }
  }

  function showTargetPicker() {
    agentStarted = false;
    tunnelBlockedView.hidden = true;
    targetView.hidden = false;
    terminalPage.hidden = true;
    keybar.hidden = true;
    if (quickbar) quickbar.hidden = true;
    bottomTabs.hidden = false;
    setActivePage("terminal");
    setActiveTabButton("terminal");
    updateProcessPanelVisibility();
    setConnection("Choose target");
    setWorkbenchTabsEnabled(false);
    renderAgentSelector();
    updateSelectedTarget();
    renderAccessibleRoots();
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    if (roots.length === 0) {
      targetSelection.textContent = "请先在用户后台配置允许访问路径";
      startAgentButton.disabled = true;
    }
  }

  function hideTargetPicker() {
    tunnelBlockedView.hidden = true;
    targetView.hidden = true;
    terminalPage.hidden = false;
    keybar.hidden = false;
    if (quickbar) quickbar.hidden = false;
    bottomTabs.hidden = false;
    updateProcessPanelVisibility();
    setWorkbenchTabsEnabled(true);
  }

  function showTunnelBlocked() {
    closeSocketOnly();
    closeActionMenu();
    agentStarted = false;
    sessionID = "";
    localStorage.removeItem(userKey(SESSION_KEY));
    processPanelOpen = false;
    folderPickerOpen = false;
    newTerminalFolder = "";
    updateProcessPanelVisibility();
    targetView.hidden = true;
    tunnelBlockedView.hidden = false;
    terminalPage.hidden = true;
    keybar.hidden = true;
    if (quickbar) quickbar.hidden = true;
    bottomTabs.hidden = false;
    activeTab = "terminal";
    setActivePage("terminal");
    setActiveTabButton("terminal");
    setWorkbenchTabsEnabled(false);
    setConnection("Tunnel disabled");
  }

  function tunnelUnavailableForUser() {
    return Boolean(state?.tunnel && !state?.edge_online);
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
    localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
    selectedTarget = { kind: "dir", path: folder, workDir: folder, label: folder };
    localStorage.setItem(userKey(TARGET_KEY), JSON.stringify(selectedTarget));
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
    localStorage.setItem(userKey(SESSION_KEY), sessionID);
    const next = sessions.find((item) => item.id === id);
    if (next?.work_dir) {
      currentPath = next.work_dir;
      workspacePath.textContent = next.work_dir;
    }
    if (next?.agent) {
      selectedAgent = normalizeAgentID(next.agent);
      localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
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
      const normalized = normalizeFullwidthSpace(data);
      lastXtermInput = { data: normalized, at: Date.now() };
      sendInput(normalized);
    });
    terminal.onWriteParsed(() => {
      const buffer = terminal.buffer?.active;
      if (!buffer || buffer.viewportY >= buffer.baseY) {
        scrollTerminalToBottom();
      }
    });
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
    socket = new WebSocket(websocketURL(`/cloud-terminal-api/ws/workbench?${params.toString()}`));
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
        writeTerminal("\x1b[31mWebSocket stopped. Please check account login, Origin allowlist, and server logs.\x1b[0m\r\n");
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
      writeTerminalError("WebSocket connection error. Check account login, Origin allowlist, and reverse proxy WebSocket upgrade.");
    });
  }

  function handleSocketMessage(msg) {
    switch (msg.type) {
      case "ready":
        sessionID = msg.session_id;
        const readyAgent = normalizeAgentID(msg.agent || currentSession()?.agent || selectedAgent);
        selectedAgent = readyAgent;
        localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
        localStorage.setItem(userKey(SESSION_KEY), sessionID);
        sessionButton.textContent = shortSession(sessionID);
        if (msg.submitted) {
          submittedSessions.add(sessionID);
          upsertSession(sessionPayloadFromMessage(msg, {
            agent: readyAgent,
            running: msg.running !== false,
            busy: currentSession()?.busy || false
          }));
        }
        workspacePath.textContent = msg.work_dir || selectedTarget?.label || state?.work_dir || "";
        currentPath = msg.work_dir || currentPath;
        terminal.reset();
        setConnection(msg.running === false ? "History" : "Attached");
        updateSessionControls();
        renderAgentSelector();
        renderProcessList();
        fitTerminal();
        scrollTerminalToBottom();
        flushPendingContinuationInput();
        break;
      case "submitted":
        sessionID = msg.session_id || sessionID;
        localStorage.setItem(userKey(SESSION_KEY), sessionID);
        submittedSessions.add(sessionID);
        upsertSession(sessionPayloadFromMessage(msg, {
          running: msg.running !== false,
          busy: true
        }));
        setSessionNameFromTitle(sessionID, msg.title);
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
        const historicalExit = currentSession()?.running === false;
        setConnection(historicalExit ? "History" : "Finished");
        if (!historicalExit && (submittedSessions.has(sessionID) || currentSession()?.submitted)) {
          markSessionDone(sessionID);
          upsertSession({
            id: sessionID,
            agent: currentSession()?.agent || selectedAgent,
            agent_label: currentSession()?.agent_label || agentLabel(selectedAgent),
            work_dir: currentSession()?.work_dir || currentPath || selectedTarget?.workDir || "",
            submitted: true,
            running: false,
            busy: false,
            exit_code: msg.exit_code ?? 0,
            duration: msg.duration || "",
            error: msg.error || "",
            last_active: new Date().toISOString()
          });
        }
        if (!historicalExit) {
          writeTerminal(`\r\n\x1b[33m[${agentLabel(currentSession()?.agent || selectedAgent)} exit ${msg.exit_code ?? 0}] ${msg.duration || ""}\x1b[0m\r\n`);
        }
        if (msg.error && !historicalExit) {
          writeTerminal(`\x1b[31m${msg.error}\x1b[0m\r\n`);
        }
        updateSessionControls();
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
    if (tunnelUnavailableForUser()) {
      renderAccessibleRoots();
      return;
    }
    if (!path) {
      renderAccessibleRoots();
      return;
    }
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/files?path=${encodeURIComponent(path)}`);
      targetPath = data.path;
      targetParent = data.parent;
      targetCurrentPath.textContent = data.path;
      targetParentButton.disabled = false; // Always allow going back to roots
      renderTargetList(data.entries || [], data.path);
    } catch (error) {
      targetFileList.innerHTML = `<div class="empty">${escapeHTML(error.message || "Load failed")}</div>`;
    }
  }

  function openFolderPicker() {
    if (tunnelUnavailableForUser()) {
      return;
    }
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

  function folderPickerRow(path, current, unavailable = false) {
    const row = document.createElement("div");
    row.className = "folder-picker-row";
    row.title = path;
    row.innerHTML = `
      <span class="folder-icon">${folderIcon()}</span>
      <span class="folder-name">${escapeHTML(current ? baseName(path) || path : baseName(path))}</span>
      <span class="folder-row-actions">
        ${unavailable ? '<span class="folder-sync">等待同步</span>' : `<button class="folder-enter" type="button" title="进入" aria-label="进入">${chevronRightIcon()}</button><button class="folder-add" type="button" title="添加" aria-label="添加">${plusIcon()}</button>`}
      </span>
    `;
    if (unavailable) {
      return row;
    }
    row.querySelector(".folder-enter")?.addEventListener("click", (event) => {
      event.stopPropagation();
      loadFolderPicker(path);
    });
    row.querySelector(".folder-add")?.addEventListener("click", (event) => {
      event.stopPropagation();
      addProcessFolder(path, true, true);
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

  function renderAccessibleRoots() {
    targetPath = "";
    targetParent = "";
    targetCurrentPath.textContent = "可访问路径";
    targetParentButton.disabled = true;
    targetFileList.innerHTML = "";
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    if (roots.length === 0) {
      targetFileList.innerHTML = '<div class="empty">用户后台未配置允许访问路径</div>';
      return;
    }
    for (const root of roots) {
      targetFileList.appendChild(targetRow({
        name: root,
        path: root,
        is_dir: true,
        size: 0
      }, false));
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
    localStorage.setItem(userKey(TARGET_KEY), JSON.stringify(selectedTarget));
    workspacePath.textContent = selectedTarget.label;
    currentPath = selectedTarget.workDir;
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
    if (!selectedTarget.workDir) {
      targetSelection.textContent = "请先在用户后台配置允许访问路径";
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
    processList.innerHTML = "";
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    const folders = (processFolders.length > 0 ? processFolders : inferFoldersFromSessions())
      .filter((folder) => !archivedFolders.has(folder));
    const visibleFolderSet = new Set(folders);
    const summaryVisibleSessions = sessions.filter((item) => !archivedSessions.has(item.id) && visibleFolderSet.has(item.work_dir));
    const summaryActive = summaryVisibleSessions.filter((item) => item.running !== false).length;
    processSummary.textContent = `${summaryActive} 执行中 / ${summaryVisibleSessions.length} 会话`;
    if (folders.length === 0) {
      if (roots.length === 0) {
        processList.innerHTML = '<div class="process-empty">用户后台未配置允许访问路径。</div>';
      } else {
        processList.appendChild(emptyProcessMessage("点击右上角「添加」从可访问路径里选目录。"));
      }
      return;
    }
    for (const folder of folders) {
      const group = document.createElement("section");
      group.className = "process-folder";
      group.title = folder;
      const folderItems = sessions.filter((item) => item.work_dir === folder);
      const visibleSessions = folderItems.filter((item) => !archivedSessions.has(item.id));
      const activeCount = visibleSessions.filter((item) => item.running !== false).length;
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

  function emptyProcessMessage(text) {
    const node = document.createElement("div");
    node.className = "process-empty";
    node.textContent = text;
    return node;
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
    const displayName = sessionDisplayName(item);
    button.innerHTML = `
      <span class="process-main">
        <strong class="session-title">
          <span class="agent-chip mini">${escapeHTML(agentShortLabel(item.agent))}</span>
          <span class="session-name" data-session-id="${escapeHTML(item.id)}">${escapeHTML(displayName)}</span>
        </strong>
        <small>${escapeHTML(item.duration || formatSessionTime(item.last_active))}</small>
      </span>
      <span class="process-status"></span>
      <span class="process-actions">
        <button class="process-rename" type="button" title="重命名" aria-label="重命名">${pencilIcon()}</button>
        <button class="process-archive" type="button" title="归档进程" aria-label="归档进程">${archiveIcon()}</button>
      </span>
    `;
    button.querySelector(".process-archive").addEventListener("click", (event) => {
      event.stopPropagation();
      archiveSession(item.id);
    });
    button.querySelector(".process-rename").addEventListener("click", (event) => {
      event.stopPropagation();
      beginRenameSession(button, item);
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

  function sessionDisplayName(item) {
    if (!item || !item.id) {
      return "";
    }
    const explicit = sessionNames[item.id];
    if (explicit && explicit.trim()) {
      return sanitizeSessionTitle(explicit) || shortSessionID(item.id);
    }
    if (item.title && String(item.title).trim()) {
      return sanitizeSessionTitle(item.title) || shortSessionID(item.id);
    }
    return shortSessionID(item.id);
  }

  function sanitizeSessionTitle(value) {
    let text = stripAnsi(String(value || ""));
    text = text.replace(/[\x00-\x1F\x7F]/g, "");
    text = text.replace(/[​-‏‪-‮⁠﻿]/g, "");
    text = text.replace(/[─-◿]/g, "");
    text = text.replace(/\s+/g, " ").trim();
    return text.length > 60 ? text.slice(0, 60) : text;
  }

  function formatSessionTime(value) {
    if (!value) {
      return "";
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return String(value);
    }
    const pad = (n) => String(n).padStart(2, "0");
    const now = new Date();
    const sameDay = date.getFullYear() === now.getFullYear()
      && date.getMonth() === now.getMonth()
      && date.getDate() === now.getDate();
    const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`;
    if (sameDay) {
      return time;
    }
    const sameYear = date.getFullYear() === now.getFullYear();
    const datePart = sameYear
      ? `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
      : `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    return `${datePart} ${time}`;
  }

  function beginRenameSession(container, item) {
    const nameNode = container.querySelector(".session-name");
    if (!nameNode || container.querySelector(".session-name-input")) {
      return;
    }
    const current = sessionNames[item.id] || "";
    const input = document.createElement("input");
    input.type = "text";
    input.className = "session-name-input";
    input.value = current;
    input.placeholder = shortSessionID(item.id);
    input.maxLength = 60;
    nameNode.replaceWith(input);
    input.focus();
    input.select();
    let finished = false;
    const commit = (save) => {
      if (finished) {
        return;
      }
      finished = true;
      if (save) {
        const next = input.value.trim();
        if (next) {
          sessionNames[item.id] = next;
        } else {
          delete sessionNames[item.id];
        }
        localStorage.setItem(userKey(SESSION_NAME_KEY), JSON.stringify(sessionNames));
      }
      renderProcessList();
    };
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        commit(true);
      } else if (event.key === "Escape") {
        event.preventDefault();
        commit(false);
      }
    });
    input.addEventListener("blur", () => commit(true));
    input.addEventListener("click", (event) => event.stopPropagation());
  }

  function recordFirstInput(targetSessionID, data) {
    if (!targetSessionID || !data) {
      return;
    }
    if (sessionNames[targetSessionID]) {
      return;
    }
    const buffer = sessionInputBuffers.get(targetSessionID) || "";
    const containsNewline = /[\r\n]/.test(data);
    const cleaned = data.replace(/[\r\n]+/g, " ");
    const stripped = cleaned.replace(/[ -]/g, "");
    const combined = (buffer + stripped).slice(0, 80);
    if (containsNewline || combined.length >= 60) {
      const candidate = combined.trim();
      if (candidate.length >= 2) {
        sessionNames[targetSessionID] = candidate.length > 40 ? candidate.slice(0, 40) + "…" : candidate;
        localStorage.setItem(userKey(SESSION_NAME_KEY), JSON.stringify(sessionNames));
        renderProcessList();
      }
      sessionInputBuffers.delete(targetSessionID);
      return;
    }
    sessionInputBuffers.set(targetSessionID, combined);
  }

  function sessionPayloadFromMessage(msg, overrides = {}) {
    const id = msg.session_id || sessionID;
    const previous = sessions.find((item) => item.id === id) || {};
    const agent = normalizeAgentID(msg.agent || overrides.agent || previous.agent || selectedAgent);
    return Object.assign({
      id,
      agent,
      agent_label: msg.agent_label || previous.agent_label || agentLabel(agent),
      work_dir: msg.work_dir || previous.work_dir || currentPath || selectedTarget?.workDir || state?.work_dir || "",
      running: msg.running !== false,
      busy: previous.busy || false,
      submitted: msg.submitted !== false,
      title: msg.title || previous.title || "",
      last_active: msg.last_active || new Date().toISOString(),
      started_at: msg.started_at || previous.started_at || ""
    }, overrides);
  }

  function setSessionNameFromTitle(id, title) {
    const clean = sanitizeSessionTitle(title);
    if (!id || !clean || sessionNames[id]) {
      return;
    }
    sessionNames[id] = clean;
    localStorage.setItem(userKey(SESSION_NAME_KEY), JSON.stringify(sessionNames));
    renderProcessList();
  }

  function upsertSession(next) {
    if (!next || !next.id) {
      return;
    }
    if (!next.submitted && !submittedSessions.has(next.id)) {
      return;
    }
    submittedSessions.add(next.id);
    if (forgottenSessions.has(next.id)) {
      return;
    }
    const existing = sessions.find((item) => item.id === next.id) || null;
    next.agent = normalizeAgentID(next.agent || existing?.agent || (next.id === sessionID ? selectedAgent : ""));
    if (!next.agent_label) {
      next.agent_label = existing?.agent_label || agentLabel(next.agent);
    }
    if (!next.work_dir && existing?.work_dir) {
      next.work_dir = existing.work_dir;
    }
    if (next.work_dir) {
      addProcessFolder(next.work_dir, false);
      if (next.running !== false) {
        rememberFolderSession(next.work_dir, next.agent, next.id);
      } else if (folderSessionID(next.work_dir, next.agent) === next.id) {
        forgetFolderSession(next.work_dir, next.agent);
      }
      localStorage.setItem(userKey(FOLDER_SESSION_KEY), JSON.stringify(folderSessions));
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
    const target = sessions.find((item) => item.id === id);
    if (!submittedSessions.has(id) && !target?.submitted) {
      return;
    }
    window.clearTimeout(busyTimers.get(id));
    const isCurrent = id === sessionID;
    upsertSession({
      id,
      agent: target?.agent || (isCurrent ? selectedAgent : undefined),
      agent_label: target?.agent_label || (isCurrent ? agentLabel(selectedAgent) : undefined),
      work_dir: target?.work_dir || (isCurrent ? (currentPath || activeWorkDir()) : ""),
      submitted: true,
      busy: true,
      last_active: new Date().toISOString()
    });
    busyTimers.set(id, window.setTimeout(() => markSessionDone(id), 15000));
  }

  function noteSessionOutput(id, data) {
    if (!id) {
      return;
    }
    const target = sessions.find((item) => item.id === id);
    if (!target?.busy) {
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
    const target = sessions.find((item) => item.id === id);
    if (!submittedSessions.has(id) && !target?.submitted) {
      return;
    }
    const isCurrent = id === sessionID;
    upsertSession({
      id,
      agent: target?.agent || (isCurrent ? selectedAgent : undefined),
      agent_label: target?.agent_label || (isCurrent ? agentLabel(selectedAgent) : undefined),
      work_dir: target?.work_dir || (isCurrent ? (currentPath || activeWorkDir()) : ""),
      submitted: true,
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

  function addProcessFolder(path, rerender = true, unarchive = false) {
    path = String(path || "").trim();
    if (!path) {
      return;
    }
    if (unarchive) {
      archivedFolders.delete(path);
      forgottenFolders.delete(path);
      persistArchiveState();
      syncArchiveState();
    } else if (archivedFolders.has(path) || forgottenFolders.has(path)) {
      return;
    }
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
    path = normalizeArchivePath(path);
    if (!path) {
      return;
    }
    archivedFolders.add(path);
    forgottenFolders.delete(path);
    persistArchiveState();
    syncArchiveState();
    renderProcessList();
    updateArchiveCount();
  }

  function unarchiveFolder(path) {
    path = normalizeArchivePath(path);
    if (!path) {
      return;
    }
    archivedFolders.delete(path);
    forgottenFolders.delete(path);
    persistArchiveState();
    syncArchiveState();
    if (!processFolders.includes(path)) {
      processFolders.unshift(path);
      localStorage.setItem(folderKey(), JSON.stringify(processFolders.slice(0, 30)));
    }
    renderProcessList();
    renderArchivePanel();
    updateArchiveCount();
  }

  function deleteArchivedFolder(path) {
    path = normalizeArchivePath(path);
    if (!path) {
      return;
    }
    archivedFolders.delete(path);
    forgottenFolders.add(path);
    persistArchiveState();
    syncArchiveState();
    const idx = processFolders.indexOf(path);
    if (idx >= 0) {
      processFolders.splice(idx, 1);
      localStorage.setItem(folderKey(), JSON.stringify(processFolders.slice(0, 30)));
    }
    renderProcessList();
    renderArchivePanel();
    updateArchiveCount();
  }

  function unarchiveSession(id) {
    id = normalizeArchiveID(id);
    if (!id) {
      return;
    }
    archivedSessions.delete(id);
    forgottenSessions.delete(id);
    persistArchiveState();
    syncArchiveState();
    renderProcessList();
    renderArchivePanel();
    updateArchiveCount();
  }

  function deleteArchivedSession(id) {
    id = normalizeArchiveID(id);
    if (!id) {
      return;
    }
    archivedSessions.delete(id);
    forgottenSessions.add(id);
    persistArchiveState();
    syncArchiveState();
    const idx = sessions.findIndex((item) => item.id === id);
    if (idx >= 0) {
      sessions.splice(idx, 1);
    }
    delete sessionNames[id];
    localStorage.setItem(userKey(SESSION_NAME_KEY), JSON.stringify(sessionNames));
    renderProcessList();
    renderArchivePanel();
    updateArchiveCount();
  }

  function updateArchiveCount() {
    if (!archiveCountEl) {
      return;
    }
    const total = archivedFolders.size + archivedSessions.size;
    archiveCountEl.textContent = total === 0 ? "0" : String(total);
  }

  function renderArchivePanel() {
    archivedFoldersList.innerHTML = "";
    const folders = Array.from(archivedFolders);
    if (folders.length === 0) {
      archivedFoldersList.innerHTML = '<div class="archive-empty">没有归档的文件夹</div>';
    } else {
      for (const folder of folders) {
        const row = document.createElement("div");
        row.className = "archive-row";
        row.title = folder;
        row.innerHTML = `
          <span class="archive-name">${escapeHTML(baseName(folder) || folder)}</span>
          <span class="archive-actions">
            <button class="ghost archive-restore" type="button">恢复</button>
            <button class="ghost archive-delete" type="button">删除</button>
          </span>
        `;
        row.querySelector(".archive-restore").addEventListener("click", () => unarchiveFolder(folder));
        row.querySelector(".archive-delete").addEventListener("click", () => {
          if (confirm(`确定删除归档文件夹「${baseName(folder) || folder}」？`)) {
            deleteArchivedFolder(folder);
          }
        });
        archivedFoldersList.appendChild(row);
      }
    }

    archivedSessionsList.innerHTML = "";
    const sessionIds = Array.from(archivedSessions);
    if (sessionIds.length === 0) {
      archivedSessionsList.innerHTML = '<div class="archive-empty">没有归档的终端</div>';
    } else {
      for (const id of sessionIds) {
        const meta = sessions.find((item) => item.id === id);
        const label = sessionNames[id] || (meta?.work_dir ? `${baseName(meta.work_dir) || meta.work_dir} · ${meta.agent || "session"}` : id);
        const row = document.createElement("div");
        row.className = "archive-row";
        row.title = id;
        row.innerHTML = `
          <span class="archive-name">${escapeHTML(label)}</span>
          <span class="archive-actions">
            <button class="ghost archive-restore" type="button">恢复</button>
            <button class="ghost archive-delete" type="button">删除</button>
          </span>
        `;
        row.querySelector(".archive-restore").addEventListener("click", () => unarchiveSession(id));
        row.querySelector(".archive-delete").addEventListener("click", () => {
          if (confirm(`确定删除归档终端「${label}」？此后不再恢复。`)) {
            deleteArchivedSession(id);
          }
        });
        archivedSessionsList.appendChild(row);
      }
    }
  }

  function persistArchiveState() {
    for (const folder of forgottenFolders) {
      archivedFolders.delete(folder);
    }
    for (const id of forgottenSessions) {
      archivedSessions.delete(id);
    }
    persistSet(ARCHIVED_FOLDER_KEY, archivedFolders);
    persistSet(ARCHIVED_SESSION_KEY, archivedSessions);
    persistSet(FORGOTTEN_FOLDER_KEY, forgottenFolders);
    persistSet(FORGOTTEN_SESSION_KEY, forgottenSessions);
    if (state) {
      state.archived_folders = Array.from(archivedFolders);
      state.archived_sessions = Array.from(archivedSessions);
      state.forgotten_folders = Array.from(forgottenFolders);
      state.forgotten_sessions = Array.from(forgottenSessions);
    }
  }

  function syncArchiveState() {
    window.clearTimeout(archiveSyncTimer);
    archiveSyncTimer = window.setTimeout(async () => {
      try {
        const updated = await fetchJSON("/cloud-terminal-api/user/archive", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            archived_folders: Array.from(archivedFolders),
            archived_sessions: Array.from(archivedSessions),
            forgotten_folders: Array.from(forgottenFolders),
            forgotten_sessions: Array.from(forgottenSessions)
          })
        }, 8000);
        applyArchiveState(updated);
        await refreshWorkbenchState();
      } catch (error) {
        profileMessage.textContent = error.message || "归档同步失败";
        profileMessage.className = "settings-message error";
      }
    }, 150);
  }

  function normalizeArchivePath(value) {
    return String(value || "").trim();
  }

  function normalizeArchiveID(value) {
    return String(value || "").trim();
  }

  function archiveSession(id) {
    id = normalizeArchiveID(id);
    if (!id) {
      return;
    }
    const archived = sessions.find((item) => item.id === id);
    archivedSessions.add(id);
    forgottenSessions.delete(id);
    persistArchiveState();
    syncArchiveState();
    if (archived?.work_dir && folderSessionID(archived.work_dir, archived.agent) === id) {
      forgetFolderSession(archived.work_dir, archived.agent);
      localStorage.setItem(userKey(FOLDER_SESSION_KEY), JSON.stringify(folderSessions));
    }
    if (id === sessionID) {
      closeSocketOnly();
      sessionID = "";
      localStorage.removeItem(userKey(SESSION_KEY));
      sessionButton.textContent = "No session";
      agentStarted = false;
    }
    renderProcessList();
    updateArchiveCount();
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
    if (!data) {
      return;
    }
    if (currentSession()?.running === false) {
      continueFromHistoricalSession(data);
      return;
    }
    if (continuingHistoricalSession) {
      pendingContinuationInput += data;
      return;
    }
    sendLiveInput(data);
  }

  function sendLiveInput(data) {
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    if (currentSession()?.running === false) {
      return;
    }
    if ((submittedSessions.has(sessionID) || currentSession()?.submitted) && (data === "\r" || data === "\n")) {
      markSessionSubmitted(sessionID);
    } else if (data === "\u0003" || data === "\u0004") {
      markSessionDone(sessionID);
    }
    if (submittedSessions.has(sessionID) || currentSession()?.submitted) {
      recordFirstInput(sessionID, data);
    }
    socket.send(JSON.stringify({ type: "input", data }));
  }

  function continueFromHistoricalSession(data) {
    const previous = currentSession();
    const workDir = previous?.work_dir || currentPath || activeWorkDir();
    if (!workDir || !isMeaningfulContinuationInput(data)) {
      return;
    }
    const agent = normalizeAgentID(previous?.agent || selectedAgent);
    if (!agentEnabled(agent)) {
      writeTerminalError(`${agentLabel(agent)} 未在后台命令策略中启用`);
      return;
    }
    if (tunnelUnavailableForUser()) {
      showTunnelBlocked();
      return;
    }

    pendingContinuationInput += data;
    continuingHistoricalSession = true;
    selectedAgent = agent;
    localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
    selectedTarget = { kind: "dir", path: workDir, workDir, label: workDir };
    localStorage.setItem(userKey(TARGET_KEY), JSON.stringify(selectedTarget));
    currentPath = workDir;
    workspacePath.textContent = workDir;
    closeSocketOnly();
    sessionID = "";
    agentStarted = true;
    hideTargetPicker();
    initTerminal();
    activateTab("terminal");
    connect(true, selectedTarget);
    loadFiles(currentPath);
    loadDiff("");
  }

  function flushPendingContinuationInput() {
    if (!continuingHistoricalSession || !pendingContinuationInput || currentSession()?.running === false) {
      return;
    }
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return;
    }
    const data = pendingContinuationInput;
    pendingContinuationInput = "";
    continuingHistoricalSession = false;
    sendLiveInput(data);
  }

  function isMeaningfulContinuationInput(data) {
    return /[^\x00-\x20\x7f]/.test(String(data || ""));
  }

  function sendResize() {
    if (!socket || socket.readyState !== WebSocket.OPEN || !terminal) {
      return;
    }
    if (currentSession()?.running === false) {
      return;
    }
    socket.send(JSON.stringify({ type: "resize", rows: terminal.rows, cols: terminal.cols }));
  }

  function sendShortcut(key) {
    if (key === "paste") {
      pasteFromClipboard();
      return;
    }
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

  function quickCommandLabel(item) {
    const label = String(item?.label || "").trim();
    if (label) {
      return label;
    }
    const cmd = String(item?.command || "").trim();
    if (cmd.length <= 18) {
      return cmd;
    }
    return cmd.slice(0, 16) + "…";
  }

  function renderQuickbar() {
    if (!quickbar) {
      return;
    }
    quickbar.innerHTML = "";
    quickCommands.forEach((item, index) => {
      if (!item || !item.command) {
        return;
      }
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = quickCommandLabel(item);
      btn.title = item.command;
      btn.dataset.quickIndex = String(index);
      attachQuickCommandHandlers(btn, index);
      quickbar.appendChild(btn);
    });
  }

  async function pasteFromClipboard() {
    let text = "";
    try {
      if (navigator.clipboard && navigator.clipboard.readText) {
        text = await navigator.clipboard.readText();
      }
    } catch (_) {
      text = "";
    }
    if (!text) {
      const fallback = await XDialog.prompt("粘贴内容到终端", "", { title: "粘贴", okText: "粘贴" });
      if (fallback === null) {
        return;
      }
      text = fallback;
    }
    if (!text) {
      return;
    }
    sendInput(text);
    try {
      terminal?.focus();
    } catch (_) {
      /* ignore */
    }
  }

  function attachQuickCommandHandlers(btn, index) {
    let pressTimer = 0;
    let longPressed = false;
    const startPress = () => {
      longPressed = false;
      window.clearTimeout(pressTimer);
      pressTimer = window.setTimeout(() => {
        longPressed = true;
        btn.classList.add("deleting");
        confirmRemoveQuickCommand(index);
        window.setTimeout(() => btn.classList.remove("deleting"), 200);
      }, 550);
    };
    const cancelPress = () => {
      window.clearTimeout(pressTimer);
      pressTimer = 0;
    };
    btn.addEventListener("touchstart", startPress, { passive: true });
    btn.addEventListener("touchend", cancelPress);
    btn.addEventListener("touchcancel", cancelPress);
    btn.addEventListener("touchmove", cancelPress);
    btn.addEventListener("mousedown", startPress);
    btn.addEventListener("mouseup", cancelPress);
    btn.addEventListener("mouseleave", cancelPress);
    btn.addEventListener("click", (event) => {
      if (longPressed) {
        event.preventDefault();
        return;
      }
      const item = quickCommands[index];
      if (!item || !item.command) {
        return;
      }
      sendInput(item.command);
      try {
        terminal?.focus();
      } catch (_) {
        /* ignore */
      }
    });
  }

  async function confirmRemoveQuickCommand(index) {
    const item = quickCommands[index];
    if (!item) {
      return;
    }
    const display = quickCommandLabel(item) || item.command;
    const ok = await XDialog.confirm(`删除「${display}」？`, { title: "删除常用指令", okText: "删除", danger: true });
    if (!ok) {
      return;
    }
    quickCommands.splice(index, 1);
    renderQuickbar();
    scheduleSaveQuickCommands();
  }

  function scheduleSaveQuickCommands() {
    window.clearTimeout(quickCommandsSaveTimer);
    quickCommandsSaveTimer = window.setTimeout(saveQuickCommandsNow, 200);
  }

  async function saveQuickCommandsNow() {
    try {
      const updated = await fetchJSON("/cloud-terminal-api/user/quick-commands", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ quick_commands: quickCommands })
      }, 6000);
      if (Array.isArray(updated?.quick_commands)) {
        quickCommands = updated.quick_commands.filter((item) => item && item.command);
        renderQuickbar();
      }
    } catch (error) {
      console.warn("save quick commands failed", error);
    }
  }

  async function loadQuickCommandsOnce() {
    if (quickCommandsLoaded) {
      return;
    }
    quickCommandsLoaded = true;
    try {
      const settings = await fetchJSON("/cloud-terminal-api/user/settings", null, 6000);
      const list = Array.isArray(settings?.quick_commands) ? settings.quick_commands : [];
      quickCommands = list.filter((item) => item && item.command);
    } catch (error) {
      console.warn("load quick commands failed", error);
      quickCommands = [];
    }
    renderQuickbar();
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
      sendInput(normalizeFullwidthSpace(data));
      textarea.value = "";
    });
    textarea.addEventListener("input", () => {
      if (composing) {
        return;
      }
      const data = textarea.value || "";
      if (data && !recentXtermInput(data)) {
        sendInput(normalizeFullwidthSpace(data));
      }
      textarea.value = "";
    });
  }

  function normalizeFullwidthSpace(value) {
    return value.includes(FULLWIDTH_SPACE) ? value.replace(/　/g, " ") : value;
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
    if (tunnelUnavailableForUser()) {
      fileList.innerHTML = '<div class="empty">本地穿透未开启，文件不可访问。</div>';
      currentPathEl.textContent = "";
      currentPathEl.title = "";
      parentButton.disabled = true;
      return;
    }
    if (!path) {
      renderFileRoots();
      return;
    }
    try {
      const data = await fetchJSON(`/cloud-terminal-api/workbench/files?path=${encodeURIComponent(path)}`);
      currentPath = data.path;
      if (state) {
        state.parentPath = data.parent;
      }
      currentPathEl.textContent = shortenPath(data.path);
      currentPathEl.title = data.path;
      parentButton.disabled = !data.parent;
      renderFileList(data.entries || []);
    } catch (error) {
      fileList.innerHTML = `<div class="empty">${escapeHTML(error.message || "Load failed")}</div>`;
    }
  }

  function renderFileRoots() {
    currentPath = "";
    if (state) {
      state.parentPath = "";
    }
    currentPathEl.textContent = "可访问路径";
    currentPathEl.title = "";
    parentButton.disabled = true;
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    if (roots.length === 0) {
      fileList.innerHTML = '<div class="empty">后台未配置允许访问路径。</div>';
      return;
    }
    fileList.innerHTML = "";
    for (const root of roots) {
      const button = document.createElement("button");
      button.className = "file-row";
      button.type = "button";
      button.title = root;
      button.innerHTML = `
        <span class="file-icon">${folderIcon()}</span>
        <span class="file-name">${escapeHTML(shortenPath(root))}</span>
        <span class="file-meta">Root</span>
      `;
      button.addEventListener("click", () => loadFiles(root));
      fileList.appendChild(button);
    }
  }

  function shortenPath(path) {
    if (!path) {
      return "";
    }
    const trimmed = path.replace(/\/+$/, "");
    if (trimmed === "" || trimmed === "/") {
      return "/";
    }
    const idx = trimmed.lastIndexOf("/");
    if (idx < 0) {
      return trimmed;
    }
    const name = trimmed.slice(idx + 1) || trimmed;
    return name;
  }

  function closePasswordForm() {
    passwordForm.hidden = true;
    togglePasswordButton.hidden = false;
    currentPasswordInput.value = "";
    newPasswordInput.value = "";
    profileMessage.textContent = "";
    profileMessage.className = "settings-message";
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
    if (tunnelUnavailableForUser()) {
      return;
    }
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
    if (tunnelUnavailableForUser()) {
      diffOutput.textContent = "本地穿透未开启，Diff 不可访问。";
      return;
    }
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

  function activateTab(tab) {
    if (tunnelUnavailableForUser() && tab !== "settings") {
      showTunnelBlocked();
      return;
    }
    if (!agentStarted && tab !== "terminal" && tab !== "settings") {
      return;
    }
    if (!agentStarted && tab === "terminal") {
      tunnelBlockedView.hidden = true;
      targetView.hidden = false;
      terminalPage.hidden = true;
      keybar.hidden = true;
      if (quickbar) quickbar.hidden = true;
      activeTab = "terminal";
      setActivePage("terminal");
      setActiveTabButton("terminal");
      return;
    }
    if (!agentStarted && tab === "settings") {
      tunnelBlockedView.hidden = true;
      targetView.hidden = true;
      terminalPage.hidden = true;
      keybar.hidden = true;
      if (quickbar) quickbar.hidden = true;
      settingsMainPage.hidden = false;
      settingsAccountPage.hidden = true;
      settingsArchivePage.hidden = true;
      activeTab = tab;
      setActivePage(tab);
      setActiveTabButton(tab);
      return;
    }
    activeTab = tab;
    setActivePage(tab);
    setActiveTabButton(tab);
    if (tab === "terminal") {
      fitTerminal();
      if (terminal) {
        terminal.focus();
        scrollTerminalToBottom();
      }
    }
    if (tab === "settings") {
      settingsMainPage.hidden = false;
      settingsAccountPage.hidden = true;
      settingsArchivePage.hidden = true;
    }
    if (tab === "files") {
      if (currentPath) {
        loadFiles(currentPath);
      } else {
        renderFileRoots();
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
    if (tunnelUnavailableForUser()) {
      return;
    }
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
    updateSessionControls();
  }

  function setActiveTabButton(tab) {
    document.querySelectorAll("[data-tab]").forEach((button) => {
      button.classList.toggle("active", button.dataset.tab === tab);
    });
  }

  function setActivePage(tab) {
    document.querySelectorAll("[data-page]").forEach((page) => {
      page.classList.toggle("active", page.dataset.page === tab);
    });
  }

  function setAuthBusy(busy) {
    usernameInput.disabled = busy;
    passwordInput.disabled = busy;
    authForm.querySelectorAll("button").forEach((button) => {
      button.disabled = busy;
    });
  }

  function setAuthMessage(value) {
    authMessage.textContent = value;
  }

  function setWorkbenchTabsEnabled(enabled) {
    workbenchTabsEnabled = enabled;
    document.querySelectorAll("[data-tab]").forEach((button) => {
      const tab = button.dataset.tab;
      // Terminal and Settings remain reachable even before a session has started,
      // so the user can switch back from Settings to the target picker.
      const alwaysEnabled = tab === "settings" || tab === "terminal";
      button.disabled = !enabled && !alwaysEnabled;
    });
    reconnectButton.disabled = !enabled || !sessionID;
    stopSessionButton.disabled = !enabled || !sessionID;
    newSessionButton.disabled = !enabled;
    processButton.disabled = !enabled;
    updateSessionControls();
  }

  function updateSessionControls() {
    const status = connectionState.textContent;
    const historical = currentSession()?.running === false;
    const hasSession = Boolean(sessionID);
    stopSessionButton.disabled = !hasSession || historical || status === "Finished" || status === "Detached" || status === "Choose target";
    reconnectButton.disabled = !workbenchTabsEnabled || !hasSession;
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
    localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
  }

  function setSelectedAgent(agent) {
    selectedAgent = normalizeAgentID(agent);
    localStorage.setItem(userKey(ACTIVE_AGENT_KEY), selectedAgent);
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

  function folderKey() {
    const user = currentAccount?.username || "default";
    return `${FOLDER_KEY}-${user}`;
  }

  function userKey(baseKey) {
    const user = currentAccount?.username || "default";
    return `${baseKey}-${user}`;
  }

  function readSavedTarget() {
    try {
      const value = JSON.parse(localStorage.getItem(userKey(TARGET_KEY)) || "null");
      return value && value.path && value.workDir ? value : null;
    } catch {
      return null;
    }
  }

  function reconcileSavedTargetWithState() {
    if (selectedTarget && pathAllowedByState(selectedTarget.workDir)) {
      return;
    }
    const firstRoot = (Array.isArray(state?.allow_paths) ? state.allow_paths : []).find((root) => !archivedFolders.has(root) && !forgottenFolders.has(root)) || "";
    if (firstRoot) {
      selectedTarget = { kind: "dir", path: firstRoot, workDir: firstRoot, label: firstRoot };
      localStorage.setItem(userKey(TARGET_KEY), JSON.stringify(selectedTarget));
      return;
    }
    selectedTarget = null;
    localStorage.removeItem(userKey(TARGET_KEY));
  }

  function pathAllowedByState(path) {
    path = String(path || "");
    const roots = Array.isArray(state?.allow_paths) ? state.allow_paths : [];
    return roots.some((root) => {
      root = String(root || "");
      if (!root) {
        return false;
      }
      const normalized = root.replace(/\/+$/, "") || "/";
      return path === root || path === normalized || (normalized !== "/" && path.startsWith(normalized + "/"));
    });
  }

  function readSavedFolders() {
    try {
      const value = JSON.parse(localStorage.getItem(folderKey()) || "[]");
      return Array.isArray(value) ? value.filter((item) => typeof item === "string" && item.trim()) : [];
    } catch {
      return [];
    }
  }

  function readStringSet(key) {
    try {
      const value = JSON.parse(localStorage.getItem(userKey(key)) || "[]");
      return new Set(Array.isArray(value) ? value.filter((item) => typeof item === "string" && item.trim()) : []);
    } catch {
      return new Set();
    }
  }

  function readObject(key) {
    try {
      const value = JSON.parse(localStorage.getItem(userKey(key)) || "{}");
      return value && typeof value === "object" && !Array.isArray(value) ? value : {};
    } catch {
      return {};
    }
  }

  function persistSet(key, value) {
    localStorage.setItem(userKey(key), JSON.stringify(Array.from(value)));
  }

  function persistObject(key, value) {
    localStorage.setItem(userKey(key), JSON.stringify(value));
  }

  function lines(values) {
    return Array.isArray(values) ? values.join("\n") : "";
  }

  function splitLines(value) {
    return String(value || "")
      .split(/\r?\n/)
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function avatarText(value) {
    const cleaned = String(value || "xm").trim();
    return cleaned.slice(0, 2).toLowerCase();
  }

  function cssEscape(value) {
    if (window.CSS && typeof window.CSS.escape === "function") {
      return window.CSS.escape(value);
    }
    return String(value).replace(/["\\]/g, "\\$&");
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

  function pencilIcon() {
    return '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4l10-10-4-4L4 16v4Z"/><path d="m14 6 4 4"/></svg>';
  }
})();
