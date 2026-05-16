const state = {
  config: null,
  currentPath: "",
  currentEntries: [],
  selectedCommand: "",
  account: null
};

const els = {
  saveState: document.getElementById("saveState"),
  saveButton: document.getElementById("saveButton"),
  sectionEyebrow: document.getElementById("sectionEyebrow"),
  databasePath: document.getElementById("databasePath"),
  accountRegistrationEnabled: document.getElementById("accountRegistrationEnabled"),
  refreshAccounts: document.getElementById("refreshAccounts"),
  accountList: document.getElementById("accountList"),
  cloudTunnelEnabled: document.getElementById("cloudTunnelEnabled"),
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
    await fetch("/cloud-terminal-api/accounts/logout", { method: "POST", credentials: "same-origin" });
  } catch {
    // ignore network errors and continue to redirect
  }
  window.location.href = "/admin/login.html";
}

async function loadConfig() {
  const response = await api("/cloud-terminal-api/admin/config");
  state.config = await response.json();
  renderConfig();
  await loadAccounts();
  await loadFS("/");
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
  for (const account of accounts) {
    const row = document.createElement("div");
    row.className = "account-row";
    row.innerHTML = `<strong>${escapeText(account.username)}</strong><span>${escapeText(account.role || "user")} · ${escapeText(account.last_login_at || account.created_at || "")}</span>`;
    els.accountList.appendChild(row);
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
  const response = await fetch(path, { ...options, headers });
  if (response.status === 401 || response.status === 403) {
    window.location.href = "/admin/login.html";
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
