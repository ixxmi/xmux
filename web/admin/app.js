const state = {
  config: null,
  currentPath: "",
  currentEntries: [],
  selectedCommand: "",
  account: null
};
const appPath = window.XMuxPath?.path || ((path) => path);

const els = {
  saveState: document.getElementById("saveState"),
  saveButton: document.getElementById("saveButton"),
  sectionEyebrow: document.getElementById("sectionEyebrow"),
  databasePath: document.getElementById("databasePath"),
  accountRegistrationEnabled: document.getElementById("accountRegistrationEnabled"),
  refreshAccounts: document.getElementById("refreshAccounts"),
  accountList: document.getElementById("accountList"),
  cloudTunnelEnabled: document.getElementById("cloudTunnelEnabled"),
  cloudDiscoveryURL: document.getElementById("cloudDiscoveryURL"),
  cloudGatewayURL: document.getElementById("cloudGatewayURL"),
  cloudTunnelAccount: document.getElementById("cloudTunnelAccount"),
  bindTunnelAccount: document.getElementById("bindTunnelAccount"),
  newAccountUsername: document.getElementById("newAccountUsername"),
  newAccountPassword: document.getElementById("newAccountPassword"),
  newAccountRole: document.getElementById("newAccountRole"),
  createAccount: document.getElementById("createAccount"),
  allowHosts: document.getElementById("allowHosts"),
  adminIPs: document.getElementById("adminIPs"),
  denyList: document.getElementById("denyList"),
  commandList: document.getElementById("commandList"),
  addCommand: document.getElementById("addCommand"),
  deleteCommand: document.getElementById("deleteCommand"),
  emptyCommand: document.getElementById("emptyCommand"),
  commandForm: document.getElementById("commandForm"),
  commandEditorTitle: document.getElementById("commandEditorTitle"),
  cmdName: document.getElementById("cmdName"),
  cmdEnabled: document.getElementById("cmdEnabled"),
  cmdInteractive: document.getElementById("cmdInteractive"),
  cmdBin: document.getElementById("cmdBin"),
  cmdMaxArgs: document.getElementById("cmdMaxArgs"),
  cmdSubcommands: document.getElementById("cmdSubcommands"),
  globalAllowPaths: document.getElementById("globalAllowPaths"),
  fileCount: document.getElementById("fileCount"),
  rootButton: document.getElementById("rootButton"),
  upButton: document.getElementById("upButton"),
  currentPath: document.getElementById("currentPath"),
  fileList: document.getElementById("fileList"),
  accountMenu: document.getElementById("accountMenu"),
  accountTrigger: document.getElementById("accountTrigger"),
  accountDropdown: document.getElementById("accountDropdown"),
  accountAvatar: document.getElementById("accountAvatar"),
  accountName: document.getElementById("accountName"),
  accountRole: document.getElementById("accountRole"),
  profileButton: document.getElementById("profileButton"),
  logoutButton: document.getElementById("logoutButton"),
  profileModal: document.getElementById("profileModal"),
  profileUsername: document.getElementById("profileUsername"),
  profileRoleInput: document.getElementById("profileRoleInput"),
  profileCurrentPassword: document.getElementById("profileCurrentPassword"),
  profileNewPassword: document.getElementById("profileNewPassword"),
  profileMessage: document.getElementById("profileMessage"),
  profileSaveButton: document.getElementById("profileSaveButton")
};

document.querySelectorAll(".nav-item").forEach((button) => {
  button.addEventListener("click", () => switchSection(button.dataset.section));
});
document.querySelectorAll("[data-command-tab]").forEach((button) => {
  button.addEventListener("click", () => switchCommandTab(button.dataset.commandTab));
});

els.saveButton.addEventListener("click", saveConfig);
els.refreshAccounts.addEventListener("click", loadAccounts);
els.bindTunnelAccount.addEventListener("click", () => {
  state.bindTunnelAccount = true;
  markDirty();
  els.cloudTunnelAccount.textContent = "保存后使用当前用户";
});
els.createAccount.addEventListener("click", createAccount);
els.addCommand.addEventListener("click", () => addCommand(`command_${Object.keys(state.config.commands).length + 1}`));
els.deleteCommand.addEventListener("click", deleteSelectedCommand);
els.rootButton.addEventListener("click", () => loadFS("/"));
els.upButton.addEventListener("click", () => {
  const parent = els.upButton.dataset.parent;
  if (parent) {
    loadFS(parent);
  }
});

els.accountTrigger.addEventListener("click", (event) => {
  event.stopPropagation();
  toggleAccountDropdown();
});
els.profileButton.addEventListener("click", () => {
  closeAccountDropdown();
  openProfileModal();
});
els.logoutButton.addEventListener("click", logout);
els.profileSaveButton.addEventListener("click", saveProfile);
document.querySelectorAll("[data-modal-close]").forEach((node) => {
  node.addEventListener("click", closeProfileModal);
});
document.addEventListener("click", (event) => {
  if (!els.accountMenu.contains(event.target)) {
    closeAccountDropdown();
  }
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    closeAccountDropdown();
    closeProfileModal();
  }
});

[
  els.accountRegistrationEnabled,
  els.cloudTunnelEnabled,
  els.cloudDiscoveryURL,
  els.cloudGatewayURL,
  els.allowHosts,
  els.adminIPs,
  els.denyList,
  els.cmdName,
  els.cmdEnabled,
  els.cmdInteractive,
  els.cmdBin,
  els.cmdMaxArgs,
  els.cmdSubcommands,
  els.globalAllowPaths
].forEach((input) => input.addEventListener("input", () => {
  syncEditorToState();
  if (input === els.globalAllowPaths) {
    syncAllowedButtons();
  }
  markDirty();
}));

loadConfig();
loadAccountIdentity();

async function loadAccountIdentity() {
  try {
    const response = await api("/cloud-terminal-api/accounts/me");
    const data = await response.json();
    setAccountIdentity(data);
  } catch (error) {
    // ignore, redirect handled by api()
  }
}

function setAccountIdentity(data) {
  state.account = data;
  const username = data?.username || "-";
  const role = data?.role || "user";
  els.accountName.textContent = username;
  els.accountRole.textContent = role;
  els.accountAvatar.textContent = buildAvatarInitials(username);
  els.accountTrigger.setAttribute("title", `${username} · ${role}`);
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

function toggleAccountDropdown() {
  const open = !els.accountDropdown.hidden;
  if (open) {
    closeAccountDropdown();
  } else {
    els.accountDropdown.hidden = false;
    els.accountTrigger.setAttribute("aria-expanded", "true");
  }
}

function closeAccountDropdown() {
  if (els.accountDropdown.hidden) {
    return;
  }
  els.accountDropdown.hidden = true;
  els.accountTrigger.setAttribute("aria-expanded", "false");
}

function openProfileModal() {
  els.profileUsername.value = state.account?.username || "";
  els.profileRoleInput.value = state.account?.role || "";
  els.profileCurrentPassword.value = "";
  els.profileNewPassword.value = "";
  setProfileMessage("", "");
  els.profileModal.hidden = false;
  setTimeout(() => els.profileCurrentPassword.focus(), 0);
}

function closeProfileModal() {
  els.profileModal.hidden = true;
}

async function saveProfile() {
  const currentPassword = els.profileCurrentPassword.value;
  const newPassword = els.profileNewPassword.value;
  if (!currentPassword || !newPassword) {
    setProfileMessage("请填写当前密码和新密码", "error");
    return;
  }
  if (newPassword.length < 8) {
    setProfileMessage("新密码至少需要 8 位", "error");
    return;
  }
  els.profileSaveButton.disabled = true;
  setProfileMessage("正在保存...", "");
  try {
    const response = await api("/cloud-terminal-api/accounts/me", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
    });
    const data = await response.json();
    setAccountIdentity(data);
    setProfileMessage("密码已更新", "ok");
    els.profileCurrentPassword.value = "";
    els.profileNewPassword.value = "";
    setTimeout(closeProfileModal, 800);
  } catch (error) {
    setProfileMessage(cleanProfileError(error.message), "error");
  } finally {
    els.profileSaveButton.disabled = false;
  }
}

function setProfileMessage(text, tone) {
  els.profileMessage.textContent = text;
  els.profileMessage.className = "modal-message";
  if (tone) {
    els.profileMessage.classList.add(tone);
  }
}

function cleanProfileError(value) {
  return String(value || "保存失败").replace(/^\d+\s*/, "").trim() || "保存失败";
}

async function logout() {
  closeAccountDropdown();
  try {
    await fetch(appPath("/cloud-terminal-api/accounts/logout"), { method: "POST", credentials: "same-origin" });
  } catch {
    // ignore network errors and continue to redirect
  }
  window.location.href = appPath("/admin/login.html");
}

async function loadConfig() {
  const response = await api("/cloud-terminal-api/admin/config");
  state.config = await response.json();
  renderConfig();
  await loadAccounts();
  await loadFS("/");
  await loadAuthSettings();
}

function switchSection(section) {
  document.querySelectorAll(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.section === section));
  document.querySelectorAll(".section").forEach((item) => item.classList.toggle("active", item.id === `section-${section}`));
  const active = document.querySelector(`.nav-item[data-section="${section}"]`);
  if (active) {
    els.sectionEyebrow.textContent = active.textContent;
  }
}

function renderConfig() {
  els.databasePath.value = state.config.database_path || "";
  els.accountRegistrationEnabled.checked = Boolean(state.config.account_registration_enabled);
  els.cloudTunnelEnabled.checked = Boolean(state.config.cloud_tunnel?.enabled);
  els.cloudDiscoveryURL.value = state.config.cloud_tunnel?.discovery_url || "";
  els.cloudGatewayURL.value = state.config.cloud_tunnel?.gateway_url || "";
  els.cloudTunnelAccount.textContent = state.config.cloud_tunnel?.bound ? `已绑定：${state.config.cloud_tunnel.account}` : "未绑定账号";
  els.allowHosts.value = lines(state.config.allow_hosts);
  els.adminIPs.value = lines(state.config.admin_ip_allowlist);
  els.denyList.value = lines(state.config.deny);
  els.globalAllowPaths.value = lines(state.config.allow_paths);
  renderCommandList();
  const first = state.selectedCommand || Object.keys(state.config.commands || {}).sort()[0] || "";
  selectCommand(first);
}

function renderCommandList() {
  els.commandList.innerHTML = "";
  for (const name of Object.keys(state.config.commands || {}).sort()) {
    const command = state.config.commands[name];
    const item = document.createElement("button");
    item.type = "button";
    item.className = "command-item";
    item.dataset.command = name;
    item.innerHTML = `<span>${escapeText(name)}</span><span class="badge">${command.enabled ? "on" : "off"}${command.interactive ? " · pty" : ""}</span>`;
    item.addEventListener("click", () => selectCommand(name));
    els.commandList.appendChild(item);
  }
}

function selectCommand(name) {
  syncEditorToState();
  state.selectedCommand = name;
  document.querySelectorAll(".command-item").forEach((item) => item.classList.toggle("active", item.dataset.command === name));
  if (!name || !state.config.commands[name]) {
    els.commandEditorTitle.textContent = "选择命令";
    els.commandForm.hidden = true;
    els.emptyCommand.hidden = false;
    els.deleteCommand.disabled = true;
    return;
  }

  const command = state.config.commands[name];
  els.commandEditorTitle.textContent = name;
  els.commandForm.hidden = false;
  els.emptyCommand.hidden = true;
  els.deleteCommand.disabled = false;
  els.cmdName.value = name;
  els.cmdEnabled.checked = Boolean(command.enabled);
  els.cmdInteractive.checked = Boolean(command.interactive);
  els.cmdBin.value = command.bin || "";
  els.cmdMaxArgs.value = command.max_args || 0;
  els.cmdSubcommands.value = lines(command.subcommands);
}

function syncEditorToState() {
  const oldName = state.selectedCommand;
  if (!oldName || !state.config || !state.config.commands || !state.config.commands[oldName] || els.commandForm.hidden) {
    return;
  }
  const newName = els.cmdName.value.trim();
  if (!newName) {
    return;
  }
  const command = {
    enabled: els.cmdEnabled.checked,
    interactive: els.cmdInteractive.checked,
    bin: els.cmdBin.value.trim(),
    max_args: Number(els.cmdMaxArgs.value || 0),
    subcommands: splitLines(els.cmdSubcommands.value)
  };
  if (newName !== oldName) {
    delete state.config.commands[oldName];
    state.selectedCommand = newName;
  }
  state.config.commands[newName] = command;
}

function collectConfig() {
  syncEditorToState();
  return {
    database_path: els.databasePath.value.trim(),
    account_registration_enabled: els.accountRegistrationEnabled.checked,
    cloud_tunnel: {
      enabled: els.cloudTunnelEnabled.checked,
      discovery_url: els.cloudDiscoveryURL.value.trim(),
      gateway_url: els.cloudGatewayURL.value.trim(),
      use_current_account: Boolean(state.bindTunnelAccount)
    },
    allow_hosts: splitLines(els.allowHosts.value),
    admin_ip_allowlist: splitLines(els.adminIPs.value),
    deny: splitLines(els.denyList.value),
    allow_paths: splitLines(els.globalAllowPaths.value),
    commands: state.config.commands || {}
  };
}

async function saveConfig() {
  try {
    els.saveState.textContent = "Saving...";
    const response = await api("/cloud-terminal-api/admin/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectConfig())
    });
    state.config = await response.json();
    state.bindTunnelAccount = false;
    renderConfig();
    els.saveState.textContent = "Saved, live";
    await loadAccounts();
  } catch (error) {
    els.saveState.textContent = error.message;
  }
}

async function createAccount() {
  const username = els.newAccountUsername.value.trim();
  const password = els.newAccountPassword.value;
  const role = els.newAccountRole.value;
  if (!username || !password) {
    els.saveState.textContent = "账号和密码不能为空";
    return;
  }
  try {
    els.saveState.textContent = "Creating account...";
    await api("/cloud-terminal-api/admin/accounts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password, role })
    });
    els.newAccountUsername.value = "";
    els.newAccountPassword.value = "";
    els.saveState.textContent = "Account created";
    await loadAccounts();
  } catch (error) {
    els.saveState.textContent = error.message;
  }
}

async function loadAccounts() {
  try {
    const response = await api("/cloud-terminal-api/admin/accounts");
    const data = await response.json();
    renderAccounts(data.accounts || []);
  } catch (error) {
    els.accountList.textContent = error.message;
  }
}

function renderAccounts(accounts) {
  els.accountList.innerHTML = "";
  if (!accounts.length) {
    els.accountList.innerHTML = `<div class="empty-state small">还没有云账号。</div>`;
    return;
  }
  const selfUsername = (state.account?.username || "").toLowerCase();
  for (const account of accounts) {
    const username = String(account.username || "").toLowerCase();
    const isSelf = username && username === selfUsername;
    const disabled = !!account.disabled;
    const row = document.createElement("div");
    row.className = "account-row";
    const statusBadge = disabled ? `<span class="badge danger">已停用</span>` : "";
    const lastSeen = escapeText(account.last_login_at || account.created_at || "");
    row.innerHTML = `
      <div class="account-row-main">
        <strong>${escapeText(account.username)}</strong>
        <span>${escapeText(account.role || "user")} · ${lastSeen}</span>
      </div>
      <div class="account-row-actions">
        ${statusBadge}
        <button type="button" class="ghost" data-action="reset">重置密码</button>
        <button type="button" class="ghost" data-action="${disabled ? "enable" : "disable"}" ${isSelf && !disabled ? "disabled title=\"不能停用自己\"" : ""}>${disabled ? "启用" : "停用"}</button>
      </div>
    `;
    row.querySelectorAll("button[data-action]").forEach((btn) => {
      btn.addEventListener("click", () => handleAccountAction(account, btn.dataset.action));
    });
    els.accountList.appendChild(row);
  }
}

async function handleAccountAction(account, action) {
  if (!account || !account.username) {
    return;
  }
  const username = account.username;
  let payload = { action, username };
  if (action === "disable") {
    const ok = await XDialog.confirm(`停用账号「${username}」？停用后该账号的所有会话会被立即失效。`, {
      title: "停用账号",
      okText: "停用",
      danger: true,
    });
    if (!ok) {
      return;
    }
  } else if (action === "enable") {
    const ok = await XDialog.confirm(`启用账号「${username}」？`, {
      title: "启用账号",
      okText: "启用",
    });
    if (!ok) {
      return;
    }
  } else if (action === "reset") {
    const next = await XDialog.prompt(`为账号「${username}」设置新密码（需符合密码策略）：`, "", {
      title: "重置密码",
      okText: "重置",
    });
    if (next === null) {
      return;
    }
    const trimmed = next.trim();
    if (!trimmed) {
      els.saveState.textContent = "密码不能为空";
      return;
    }
    payload = { action: "reset_password", username, password: trimmed };
  } else {
    return;
  }
  try {
    els.saveState.textContent = action === "reset" ? "重置密码中..." : "更新中...";
    const response = await api("/cloud-terminal-api/admin/accounts/manage", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    const data = await response.json();
    renderAccounts(data.accounts || []);
    if (action === "reset") {
      els.saveState.textContent = `已为 ${username} 重置密码，原会话已下线`;
    } else if (action === "disable") {
      els.saveState.textContent = `已停用 ${username}`;
    } else {
      els.saveState.textContent = `已启用 ${username}`;
    }
  } catch (error) {
    els.saveState.textContent = error.message;
  }
}

function addCommand(name) {
  syncEditorToState();
  if (!state.config.commands) {
    state.config.commands = {};
  }
  state.config.commands[name] = {
    enabled: true,
    interactive: false,
    bin: "",
    max_args: 8,
    subcommands: []
  };
  renderCommandList();
  selectCommand(name);
  markDirty();
}

function deleteSelectedCommand() {
  if (!state.selectedCommand) {
    return;
  }
  delete state.config.commands[state.selectedCommand];
  state.selectedCommand = "";
  renderCommandList();
  selectCommand(Object.keys(state.config.commands || {}).sort()[0] || "");
  markDirty();
}

async function loadFS(path) {
  const response = await api(`/cloud-terminal-api/admin/fs?path=${encodeURIComponent(path || "/")}`);
  const data = await response.json();
  state.currentPath = data.path;
  els.currentPath.textContent = data.path;
  els.upButton.dataset.parent = data.parent || "";
  els.upButton.disabled = !data.parent;
  renderFiles(data.entries || []);
}

function renderFiles(entries) {
  state.currentEntries = entries;
  els.fileList.innerHTML = "";
  els.fileCount.textContent = `${entries.length} 项`;
  for (const entry of entries) {
    const row = document.createElement("div");
    row.className = `file-row ${entry.is_dir ? "is-dir" : "is-file"}`;

    const name = document.createElement("div");
    name.className = "file-name";
    const icon = document.createElement("span");
    icon.className = `file-icon ${entry.is_dir ? "dir-icon" : "file-icon-doc"}`;
    icon.setAttribute("aria-hidden", "true");
    const text = document.createElement("div");
    text.className = "file-text";
    const label = document.createElement("span");
    label.className = "file-label";
    label.textContent = entry.name;
    const meta = document.createElement("span");
    meta.className = "file-meta";
    meta.textContent = entry.is_dir ? "可展开" : formatBytes(entry.size);
    text.append(label, meta);
    name.append(icon, text);
    row.appendChild(name);

    const open = document.createElement("button");
    open.type = "button";
    open.className = "file-action";
    open.textContent = entry.is_dir ? "Open" : "Select";
    open.addEventListener("click", () => {
      if (entry.is_dir) {
        loadFS(entry.path);
      } else {
        addGlobalAllowPath(entry.path);
      }
    });
    row.appendChild(open);

    const allow = document.createElement("button");
    allow.type = "button";
    allow.className = "file-action path-toggle";
    allow.dataset.path = entry.path;
    allow.addEventListener("click", () => toggleGlobalAllowPath(entry.path));
    row.appendChild(allow);
    els.fileList.appendChild(row);
  }
  syncAllowedButtons();
}

function addGlobalAllowPath(path) {
  const values = new Set(splitLines(els.globalAllowPaths.value));
  values.add(path);
  setGlobalAllowPaths(Array.from(values));
  markDirty();
}

function toggleGlobalAllowPath(path) {
  const values = new Set(splitLines(els.globalAllowPaths.value));
  if (values.has(path)) {
    values.delete(path);
  } else {
    values.add(path);
  }
  setGlobalAllowPaths(Array.from(values));
  markDirty();
}

function setGlobalAllowPaths(paths) {
  state.config.allow_paths = paths;
  els.globalAllowPaths.value = lines(paths);
  syncAllowedButtons();
}

function syncAllowedButtons() {
  const allowed = new Set(splitLines(els.globalAllowPaths.value));
  document.querySelectorAll(".path-toggle").forEach((button) => {
    const isAllowed = allowed.has(button.dataset.path);
    button.textContent = isAllowed ? "取消" : "Allow";
    button.classList.toggle("primary", !isAllowed);
    button.classList.toggle("cancel", isAllowed);
  });
}

function switchCommandTab(tab) {
  document.querySelectorAll("[data-command-tab]").forEach((item) => item.classList.toggle("active", item.dataset.commandTab === tab));
  document.querySelectorAll(".command-tab").forEach((item) => item.classList.toggle("active", item.id === `command-tab-${tab}`));
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  const response = await fetch(appPath(path), { ...options, headers });
  if (response.status === 401 || response.status === 403) {
    window.location.href = appPath("/admin/login.html");
    throw new Error("Unauthorized");
  }
  if (!response.ok) {
    throw new Error(await response.text());
  }
  return response;
}

function markDirty() {
  els.saveState.textContent = "Unsaved";
}

function lines(values) {
  return (values || []).join("\n");
}

function splitLines(value) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function escapeText(value) {
  return String(value).replace(/[&<>]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[char]);
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

// ---------------- Auth / SMTP / OAuth settings ----------------

const authEls = {
  pwdMinLength: document.getElementById("pwdMinLength"),
  pwdRequireUpper: document.getElementById("pwdRequireUpper"),
  pwdRequireLower: document.getElementById("pwdRequireLower"),
  pwdRequireDigit: document.getElementById("pwdRequireDigit"),
  pwdRequireSymbol: document.getElementById("pwdRequireSymbol"),
  pwdDenyCommon: document.getElementById("pwdDenyCommon"),
  authRequireEmailOnRegister: document.getElementById("authRequireEmailOnRegister"),
  authRequireEmailVerifiedToLogin: document.getElementById("authRequireEmailVerifiedToLogin"),
  authOAuthGoogleEnabled: document.getElementById("authOAuthGoogleEnabled"),
  authOAuthGoogleAutoRegister: document.getElementById("authOAuthGoogleAutoRegister"),
  authAppBaseURL: document.getElementById("authAppBaseURL"),
  saveAuthSettings: document.getElementById("saveAuthSettings"),
  authSaveState: document.getElementById("authSaveState"),
  smtpEnabled: document.getElementById("smtpEnabled"),
  smtpHost: document.getElementById("smtpHost"),
  smtpPort: document.getElementById("smtpPort"),
  smtpUsername: document.getElementById("smtpUsername"),
  smtpPassword: document.getElementById("smtpPassword"),
  smtpFromAddress: document.getElementById("smtpFromAddress"),
  smtpFromName: document.getElementById("smtpFromName"),
  smtpStartTLS: document.getElementById("smtpStartTLS"),
  smtpSMTPS: document.getElementById("smtpSMTPS"),
  smtpSkipVerify: document.getElementById("smtpSkipVerify"),
  saveSMTPSettings: document.getElementById("saveSMTPSettings"),
  smtpStatus: document.getElementById("smtpStatus"),
  oauthGoogleEnabled: document.getElementById("oauthGoogleEnabled"),
  oauthGoogleClientID: document.getElementById("oauthGoogleClientID"),
  oauthGoogleClientSecret: document.getElementById("oauthGoogleClientSecret"),
  oauthGoogleRedirectURL: document.getElementById("oauthGoogleRedirectURL"),
  oauthGoogleHostedDomain: document.getElementById("oauthGoogleHostedDomain"),
  oauthGoogleLinkExistingMode: document.getElementById("oauthGoogleLinkExistingMode"),
  saveOAuthGoogleSettings: document.getElementById("saveOAuthGoogleSettings"),
  oauthGoogleStatus: document.getElementById("oauthGoogleStatus"),
  smtpTestTo: document.getElementById("smtpTestTo"),
  testSMTP: document.getElementById("testSMTP"),
  smtpTestResult: document.getElementById("smtpTestResult"),
};

async function loadAuthSettings() {
  if (!authEls.saveAuthSettings) return;
  try {
    const [auth, smtp, oauth] = await Promise.all([
      api("/cloud-terminal-api/admin/auth-settings").then((r) => r.json()),
      api("/cloud-terminal-api/admin/smtp-settings").then((r) => r.json()),
      api("/cloud-terminal-api/admin/oauth-settings/google").then((r) => r.json()),
    ]);
    fillAuthForm(auth);
    fillSMTPForm(smtp);
    fillOAuthGoogleForm(oauth);
  } catch (err) {
    console.warn("loadAuthSettings", err);
  }
}

function fillAuthForm(auth) {
  if (!auth) return;
  const pwd = auth.password_policy || {};
  const policy = auth.auth_settings || {};
  authEls.pwdMinLength.value = pwd.min_length || 10;
  authEls.pwdRequireUpper.checked = !!pwd.require_upper;
  authEls.pwdRequireLower.checked = !!pwd.require_lower;
  authEls.pwdRequireDigit.checked = !!pwd.require_digit;
  authEls.pwdRequireSymbol.checked = !!pwd.require_symbol;
  authEls.pwdDenyCommon.checked = !!pwd.deny_common;
  authEls.authRequireEmailOnRegister.checked = !!policy.require_email_on_register;
  authEls.authRequireEmailVerifiedToLogin.checked = !!policy.require_email_verified_to_login;
  authEls.authOAuthGoogleEnabled.checked = !!policy.oauth_google_enabled;
  authEls.authOAuthGoogleAutoRegister.checked = !!policy.oauth_google_auto_register;
  authEls.authAppBaseURL.value = auth.app_base_url || "";
  authEls.authSaveState.textContent = "已加载";
}

function fillSMTPForm(smtp) {
  if (!smtp) return;
  authEls.smtpEnabled.checked = !!smtp.enabled;
  authEls.smtpHost.value = smtp.host || "";
  authEls.smtpPort.value = smtp.port || 587;
  authEls.smtpUsername.value = smtp.username || "";
  authEls.smtpPassword.value = "";
  authEls.smtpFromAddress.value = smtp.from_address || "";
  authEls.smtpFromName.value = smtp.from_name || "";
  authEls.smtpStartTLS.checked = !!smtp.use_starttls;
  authEls.smtpSMTPS.checked = !!smtp.use_smtps;
  authEls.smtpSkipVerify.checked = !!smtp.skip_tls_verify;
  authEls.smtpStatus.textContent = smtp.password_set ? "已配置" : "未配置密码";
}

function fillOAuthGoogleForm(oauth) {
  if (!oauth) return;
  authEls.oauthGoogleEnabled.checked = !!oauth.enabled;
  authEls.oauthGoogleClientID.value = oauth.client_id || "";
  authEls.oauthGoogleClientSecret.value = "";
  authEls.oauthGoogleRedirectURL.value = oauth.redirect_url || "";
  authEls.oauthGoogleHostedDomain.value = oauth.hosted_domain || "";
  authEls.oauthGoogleLinkExistingMode.value = oauth.link_existing_mode || "require_password";
  authEls.oauthGoogleStatus.textContent = oauth.client_secret_set ? "已配置" : "未配置 secret";
}

async function saveAuthSettings() {
  const payload = {
    password_policy: {
      min_length: parseInt(authEls.pwdMinLength.value, 10) || 10,
      require_upper: authEls.pwdRequireUpper.checked,
      require_lower: authEls.pwdRequireLower.checked,
      require_digit: authEls.pwdRequireDigit.checked,
      require_symbol: authEls.pwdRequireSymbol.checked,
      deny_common: authEls.pwdDenyCommon.checked,
      max_length: 128,
    },
    auth_settings: {
      require_email_on_register: authEls.authRequireEmailOnRegister.checked,
      require_email_verified_to_login: authEls.authRequireEmailVerifiedToLogin.checked,
      oauth_google_enabled: authEls.authOAuthGoogleEnabled.checked,
      oauth_google_auto_register: authEls.authOAuthGoogleAutoRegister.checked,
    },
    app_base_url: authEls.authAppBaseURL.value.trim(),
  };
  authEls.authSaveState.textContent = "保存中…";
  try {
    const resp = await api("/cloud-terminal-api/admin/auth-settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!resp.ok) {
      throw new Error(await resp.text());
    }
    fillAuthForm(await resp.json());
    authEls.authSaveState.textContent = "已保存";
  } catch (err) {
    authEls.authSaveState.textContent = "保存失败：" + err.message;
  }
}

async function saveSMTPSettings() {
  const payload = {
    enabled: authEls.smtpEnabled.checked,
    host: authEls.smtpHost.value.trim(),
    port: parseInt(authEls.smtpPort.value, 10) || 0,
    username: authEls.smtpUsername.value.trim(),
    password: authEls.smtpPassword.value,
    from_address: authEls.smtpFromAddress.value.trim(),
    from_name: authEls.smtpFromName.value.trim(),
    use_starttls: authEls.smtpStartTLS.checked,
    use_smtps: authEls.smtpSMTPS.checked,
    skip_tls_verify: authEls.smtpSkipVerify.checked,
  };
  authEls.smtpStatus.textContent = "保存中…";
  try {
    const resp = await api("/cloud-terminal-api/admin/smtp-settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!resp.ok) {
      throw new Error(await resp.text());
    }
    fillSMTPForm(await resp.json());
  } catch (err) {
    authEls.smtpStatus.textContent = "保存失败：" + err.message;
  }
}

async function saveOAuthGoogleSettings() {
  const payload = {
    enabled: authEls.oauthGoogleEnabled.checked,
    client_id: authEls.oauthGoogleClientID.value.trim(),
    client_secret: authEls.oauthGoogleClientSecret.value,
    redirect_url: authEls.oauthGoogleRedirectURL.value.trim(),
    hosted_domain: authEls.oauthGoogleHostedDomain.value.trim(),
    link_existing_mode: authEls.oauthGoogleLinkExistingMode.value,
  };
  authEls.oauthGoogleStatus.textContent = "保存中…";
  try {
    const resp = await api("/cloud-terminal-api/admin/oauth-settings/google", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!resp.ok) {
      throw new Error(await resp.text());
    }
    fillOAuthGoogleForm(await resp.json());
  } catch (err) {
    authEls.oauthGoogleStatus.textContent = "保存失败：" + err.message;
  }
}

if (authEls.saveAuthSettings) {
  authEls.saveAuthSettings.addEventListener("click", saveAuthSettings);
}
if (authEls.saveSMTPSettings) {
  authEls.saveSMTPSettings.addEventListener("click", saveSMTPSettings);
}
if (authEls.saveOAuthGoogleSettings) {
  authEls.saveOAuthGoogleSettings.addEventListener("click", saveOAuthGoogleSettings);
}
if (authEls.testSMTP) {
  authEls.testSMTP.addEventListener("click", async () => {
    const to = (authEls.smtpTestTo.value || "").trim();
    if (!to) {
      authEls.smtpTestResult.textContent = "请填写测试收件邮箱";
      authEls.smtpTestResult.classList.remove("ok", "fail");
      return;
    }
    authEls.smtpTestResult.textContent = "发送中...";
    authEls.smtpTestResult.classList.remove("ok", "fail");
    try {
      const resp = await api("/cloud-terminal-api/admin/smtp-settings/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ to }),
      });
      let data = null;
      try {
        data = await resp.json();
      } catch (_) {
        data = null;
      }
      if (!resp.ok && !data) {
        throw new Error(`HTTP ${resp.status}`);
      }
      const meta = data && data.sender_kind ? ` · sender=${data.sender_kind}` : "";
      const took = data && Number.isFinite(data.took_ms) ? ` · ${data.took_ms}ms` : "";
      if (data && data.ok) {
        authEls.smtpTestResult.textContent = `✓ ${data.message || "测试邮件已发送"}${meta}${took}`;
        authEls.smtpTestResult.classList.add("ok");
      } else {
        const reason = (data && data.error) || `HTTP ${resp.status}`;
        authEls.smtpTestResult.textContent = `✗ 发送失败：${reason}${meta}${took}`;
        authEls.smtpTestResult.classList.add("fail");
      }
    } catch (err) {
      authEls.smtpTestResult.textContent = "✗ 失败：" + (err.message || String(err));
      authEls.smtpTestResult.classList.add("fail");
    }
  });
}
