const state = {
  cloudSettings: null,
  agentConfig: null,
  currentPath: "",
  currentEntries: []
};

const els = {
  saveState: document.getElementById("saveState"),
  sectionEyebrow: document.getElementById("sectionEyebrow"),
  accountMenu: document.getElementById("accountMenu"),
  accountTrigger: document.getElementById("accountTrigger"),
  accountDropdown: document.getElementById("accountDropdown"),
  accountAvatar: document.getElementById("accountAvatar"),
  accountName: document.getElementById("accountName"),
  accountRole: document.getElementById("accountRole"),
  logoutButton: document.getElementById("logoutButton"),
  tunnelEnabled: document.getElementById("tunnelEnabled"),
  discoveryURL: document.getElementById("discoveryURL"),
  gatewayURL: document.getElementById("gatewayURL"),
  edgeID: document.getElementById("edgeID"),
  edgeName: document.getElementById("edgeName"),
  workDir: document.getElementById("workDir"),
  saveTunnelButton: document.getElementById("saveTunnelButton"),
  tunnelMessage: document.getElementById("tunnelMessage"),
  bindDot: document.getElementById("bindDot"),
  bindStatus: document.getElementById("bindStatus"),
  bindAccount: document.getElementById("bindAccount"),
  bindButton: document.getElementById("bindButton"),
  unbindButton: document.getElementById("unbindButton"),
  bindMessage: document.getElementById("bindMessage"),
  commandList: document.getElementById("commandList"),
  enableAllCommands: document.getElementById("enableAllCommands"),
  policySummary: document.getElementById("policySummary"),
  rootButton: document.getElementById("rootButton"),
  upButton: document.getElementById("upButton"),
  currentPath: document.getElementById("currentPath"),
  fileCount: document.getElementById("fileCount"),
  fileList: document.getElementById("fileList"),
  globalAllowPaths: document.getElementById("globalAllowPaths"),
  saveCommandsButton: document.getElementById("saveCommandsButton"),
  commandsMessage: document.getElementById("commandsMessage"),
  savePathsButton: document.getElementById("savePathsButton"),
  pathsMessage: document.getElementById("pathsMessage"),
  profileUsername: document.getElementById("profileUsername"),
  profileRole: document.getElementById("profileRole"),
  currentPassword: document.getElementById("currentPassword"),
  newPassword: document.getElementById("newPassword"),
  saveProfileButton: document.getElementById("saveProfileButton"),
  profileMessage: document.getElementById("profileMessage")
};

document.querySelectorAll(".nav-item").forEach((btn) => {
  btn.addEventListener("click", () => switchSection(btn.dataset.section));
});

els.accountTrigger.addEventListener("click", (e) => {
  e.stopPropagation();
  toggleDropdown();
});
document.addEventListener("click", () => closeDropdown());
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDropdown(); });

els.logoutButton.addEventListener("click", logout);
els.saveTunnelButton.addEventListener("click", saveTunnelConfig);
els.bindButton.addEventListener("click", bindLocalClient);
els.unbindButton.addEventListener("click", unbindLocalClient);
els.enableAllCommands.addEventListener("click", () => {
  els.commandList.querySelectorAll("input[type='checkbox']:not(:disabled)").forEach((cb) => { cb.checked = true; });
  setCommandsMessage("有未保存的修改", "");
});
[els.globalAllowPaths].forEach((el) => el.addEventListener("input", () => {
  syncAllowedButtons();
  setPathsMessage("有未保存的修改", "");
}));
els.saveCommandsButton.addEventListener("click", saveCommands);
els.savePathsButton.addEventListener("click", savePaths);
els.saveProfileButton.addEventListener("click", saveProfile);
els.rootButton.addEventListener("click", () => loadFS("/"));
els.upButton.addEventListener("click", () => {
  const parent = els.upButton.dataset.parent;
  if (parent) loadFS(parent);
});

init();

async function init() {
  try {
    const me = await api("/cloud-terminal-api/accounts/me");
    const data = await me.json();
    renderAccount(data);
  } catch {
    window.location.href = "/agent/login.html";
    return;
  }
  await Promise.all([loadAgentConfig(), loadCloudSettings(), loadFS("/")]);
}

async function loadAgentConfig() {
  try {
    const res = await api("/cloud-terminal-api/agent/config");
    const data = await res.json();
    state.agentConfig = data;
    renderAgentConfig(data);
  } catch (error) {
    els.saveState.textContent = error.message || "加载本地配置失败";
  }
}

function renderAgentConfig(cfg) {
  els.tunnelEnabled.checked = Boolean(cfg.tunnel_enabled);
  els.discoveryURL.value = cfg.discovery_url || "";
  els.gatewayURL.value = cfg.gateway_url || "";
  els.edgeID.value = cfg.edge_id || "";
  els.edgeName.value = cfg.edge_name || "";
  els.workDir.value = cfg.work_dir || "";
  renderBindState(cfg);
}

function renderBindState(cfg) {
  const bound = Boolean(cfg && cfg.bound);
  els.bindDot.dataset.state = bound ? "bound" : "unbound";
  els.bindStatus.textContent = bound ? "已绑定" : "未绑定";
  els.bindAccount.textContent = bound ? `账号：${cfg.bound_account || "-"}` : "客户端进程暂时无法连接云端反向穿透";
  els.bindButton.textContent = bound ? "重新绑定" : "绑定当前账号到本地客户端";
  els.unbindButton.hidden = !bound;
}

async function bindLocalClient() {
  els.bindButton.disabled = true;
  els.unbindButton.disabled = true;
  setBindMessage("申请云端会话...", "");
  try {
    const issued = await api("/cloud-terminal-api/accounts/tunnel-session", { method: "POST" });
    const creds = await issued.json();
    if (!creds.username || !creds.session_id) {
      throw new Error("云端未返回有效凭证");
    }
    const saved = await api("/cloud-terminal-api/agent/bind", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account: creds.username, session_id: creds.session_id })
    });
    state.agentConfig = await saved.json();
    renderAgentConfig(state.agentConfig);
    setBindMessage("已保存到本地 agent.yaml，客户端将自动重连", "ok");
    setTimeout(() => setBindMessage("", ""), 3000);
  } catch (error) {
    setBindMessage(error.message || "绑定失败", "error");
  } finally {
    els.bindButton.disabled = false;
    els.unbindButton.disabled = false;
  }
}

async function unbindLocalClient() {
  if (!confirm("解绑后客户端将断开反向穿透，确认继续？")) {
    return;
  }
  els.bindButton.disabled = true;
  els.unbindButton.disabled = true;
  setBindMessage("解绑中...", "");
  try {
    const res = await api("/cloud-terminal-api/agent/bind", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ clear: true })
    });
    state.agentConfig = await res.json();
    renderAgentConfig(state.agentConfig);
    setBindMessage("已解绑", "ok");
    setTimeout(() => setBindMessage("", ""), 2000);
  } catch (error) {
    setBindMessage(error.message || "解绑失败", "error");
  } finally {
    els.bindButton.disabled = false;
    els.unbindButton.disabled = false;
  }
}

function setBindMessage(text, tone) {
  els.bindMessage.textContent = text;
  els.bindMessage.className = "field-message" + (tone ? ` ${tone}` : "");
}

async function saveTunnelConfig() {
  els.saveTunnelButton.disabled = true;
  setTunnelMessage("保存中...", "");
  try {
    const res = await api("/cloud-terminal-api/agent/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        tunnel_enabled: els.tunnelEnabled.checked,
        discovery_url: els.discoveryURL.value.trim(),
        gateway_url: els.gatewayURL.value.trim(),
        edge_id: els.edgeID.value.trim(),
        edge_name: els.edgeName.value.trim()
      })
    });
    const data = await res.json();
    state.agentConfig = data;
    renderAgentConfig(data);
    setTunnelMessage("已保存到本地配置", "ok");
    setTimeout(() => setTunnelMessage("", ""), 2000);
  } catch (error) {
    setTunnelMessage(error.message || "保存失败", "error");
  } finally {
    els.saveTunnelButton.disabled = false;
  }
}

async function loadCloudSettings() {
  try {
    const res = await api("/cloud-terminal-api/user/settings");
    state.cloudSettings = await res.json();
    renderCloudSettings();
  } catch (error) {
    els.saveState.textContent = error.message || "加载云端设置失败";
  }
}

function renderCloudSettings() {
  const settings = state.cloudSettings || {};
  els.globalAllowPaths.value = lines(settings.allow_paths);
  renderPolicySummary();
  renderCommands();
  syncAllowedButtons();
  els.saveState.textContent = "Loaded";
}

function renderPolicySummary() {
  const limits = state.cloudSettings?.policy_limits || {};
  const commands = limits.commands || {};
  const enabled = Object.values(commands).filter((c) => c.enabled !== false).length;
  els.policySummary.innerHTML = `
    <div><span>可用命令</span><strong>${enabled}</strong></div>
    <div><span>命令黑名单</span><strong>${(limits.deny || []).length}</strong></div>
  `;
}

function renderCommands() {
  els.commandList.innerHTML = "";
  const limits = state.cloudSettings?.policy_limits?.commands || {};
  const commands = state.cloudSettings?.commands || {};
  const names = Object.keys(limits).sort();
  if (names.length === 0) {
    els.commandList.innerHTML = '<div class="empty-state small">管理员未配置可用命令。</div>';
    return;
  }
  for (const name of names) {
    const limit = limits[name] || {};
    const command = commands[name] || limit;
    const denied = (state.cloudSettings?.policy_limits?.deny || []).includes(name);
    const row = document.createElement("label");
    row.className = "command-row";
    row.innerHTML = `
      <input type="checkbox" data-command="${escapeAttr(name)}"
        ${command.enabled && !denied ? "checked" : ""}
        ${limit.enabled === false || denied ? "disabled" : ""} />
      <span>
        <strong>${escapeHTML(name)}</strong>
        <small>${denied ? "管理员已禁用" : command.interactive ? "PTY" : "命令"}${limit.max_args ? ` · ${limit.max_args} args` : ""}</small>
      </span>
    `;
    row.querySelector("input").addEventListener("input", () => setCommandsMessage("有未保存的修改", ""));
    els.commandList.appendChild(row);
  }
}

function collectCommandsPayload() {
  const limits = state.cloudSettings?.policy_limits?.commands || {};
  const current = state.cloudSettings?.commands || {};
  const commands = {};
  for (const name of Object.keys(limits)) {
    const box = els.commandList.querySelector(`input[data-command="${cssSafeAttr(name)}"]`);
    const limit = limits[name] || {};
    const base = current[name] || limit;
    commands[name] = Object.assign({}, base, {
      enabled: limit.enabled !== false && Boolean(box && box.checked),
      interactive: Boolean(limit.interactive),
      max_args: Number(limit.max_args || 0)
    });
  }
  return {
    cloud_tunnel_enabled: Boolean(state.cloudSettings?.cloud_tunnel_enabled),
    allow_paths: Array.isArray(state.cloudSettings?.allow_paths) ? state.cloudSettings.allow_paths : [],
    commands
  };
}

function collectPathsPayload() {
  return {
    cloud_tunnel_enabled: Boolean(state.cloudSettings?.cloud_tunnel_enabled),
    allow_paths: splitLines(els.globalAllowPaths.value),
    commands: state.cloudSettings?.commands || {}
  };
}

async function saveCommands() {
  els.saveCommandsButton.disabled = true;
  setCommandsMessage("保存中...", "");
  try {
    const res = await api("/cloud-terminal-api/user/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectCommandsPayload())
    });
    state.cloudSettings = await res.json();
    renderCloudSettings();
    setCommandsMessage("已保存", "ok");
    setTimeout(() => setCommandsMessage("", ""), 1500);
  } catch (error) {
    setCommandsMessage(error.message || "保存失败", "error");
  } finally {
    els.saveCommandsButton.disabled = false;
  }
}

async function savePaths() {
  els.savePathsButton.disabled = true;
  setPathsMessage("保存中...", "");
  try {
    const res = await api("/cloud-terminal-api/user/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(collectPathsPayload())
    });
    state.cloudSettings = await res.json();
    renderCloudSettings();
    setPathsMessage("已保存", "ok");
    setTimeout(() => setPathsMessage("", ""), 1500);
  } catch (error) {
    setPathsMessage(error.message || "保存失败", "error");
  } finally {
    els.savePathsButton.disabled = false;
  }
}

async function loadFS(path) {
  try {
    const res = await api(`/cloud-terminal-api/admin/fs?path=${encodeURIComponent(path || "/")}`);
    const data = await res.json();
    state.currentPath = data.path;
    els.currentPath.textContent = data.path;
    els.upButton.dataset.parent = data.parent || "";
    els.upButton.disabled = !data.parent;
    renderFiles(data.entries || []);
  } catch (error) {
    els.fileList.textContent = error.message || "加载失败";
  }
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
    meta.textContent = entry.is_dir ? "目录" : formatBytes(entry.size);
    text.append(label, meta);
    name.append(icon, text);
    row.appendChild(name);

    const open = document.createElement("button");
    open.type = "button";
    open.className = "file-action";
    open.textContent = entry.is_dir ? "Open" : "Select";
    open.addEventListener("click", () => {
      if (entry.is_dir) loadFS(entry.path);
      else addAllowPath(entry.path);
    });
    row.appendChild(open);

    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "file-action path-toggle";
    toggle.dataset.path = entry.path;
    toggle.addEventListener("click", () => toggleAllowPath(entry.path));
    row.appendChild(toggle);
    els.fileList.appendChild(row);
  }
  syncAllowedButtons();
}

function addAllowPath(path) {
  const values = new Set(splitLines(els.globalAllowPaths.value));
  values.add(path);
  els.globalAllowPaths.value = lines(Array.from(values));
  syncAllowedButtons();
  setPathsMessage("有未保存的修改", "");
}

function toggleAllowPath(path) {
  const values = new Set(splitLines(els.globalAllowPaths.value));
  if (values.has(path)) values.delete(path);
  else values.add(path);
  els.globalAllowPaths.value = lines(Array.from(values));
  syncAllowedButtons();
  setPathsMessage("有未保存的修改", "");
}

function syncAllowedButtons() {
  const allowed = new Set(splitLines(els.globalAllowPaths.value));
  document.querySelectorAll(".path-toggle").forEach((btn) => {
    const isAllowed = allowed.has(btn.dataset.path);
    btn.textContent = isAllowed ? "取消" : "Allow";
    btn.classList.toggle("primary", !isAllowed);
    btn.classList.toggle("cancel", isAllowed);
  });
}

async function saveProfile() {
  const currentPwd = els.currentPassword.value;
  const newPwd = els.newPassword.value;
  setProfileMessage("", "");
  if (!currentPwd || !newPwd) {
    setProfileMessage("请填写当前密码和新密码", "error");
    return;
  }
  els.saveProfileButton.disabled = true;
  setProfileMessage("保存中...", "");
  try {
    await api("/cloud-terminal-api/accounts/me", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ current_password: currentPwd, new_password: newPwd })
    });
    els.currentPassword.value = "";
    els.newPassword.value = "";
    setProfileMessage("密码已更新", "ok");
  } catch (error) {
    setProfileMessage(error.message || "保存失败", "error");
  } finally {
    els.saveProfileButton.disabled = false;
  }
}

async function logout() {
  closeDropdown();
  try { await fetch("/cloud-terminal-api/accounts/logout", { method: "POST", credentials: "include" }); } catch {}
  window.location.href = "/agent/login.html";
}

function renderAccount(data) {
  const username = data?.username || "-";
  const role = data?.role || "user";
  els.accountName.textContent = username;
  els.accountRole.textContent = role;
  els.accountAvatar.textContent = buildInitials(username);
  els.profileUsername.value = username;
  els.profileRole.value = role === "admin" ? "管理员" : "用户";
}

function buildInitials(name) {
  const parts = String(name || "").trim().split(/[\s@._-]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return (name || "--").slice(0, 2).toUpperCase();
}

function switchSection(section) {
  document.querySelectorAll(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.section === section));
  document.querySelectorAll(".section").forEach((item) => item.classList.toggle("active", item.id === `section-${section}`));
  const active = document.querySelector(`.nav-item[data-section="${section}"]`);
  if (active) els.sectionEyebrow.textContent = active.textContent;
}

function toggleDropdown() {
  const open = !els.accountDropdown.hidden;
  els.accountDropdown.hidden = open;
  els.accountTrigger.setAttribute("aria-expanded", !open ? "true" : "false");
}

function closeDropdown() {
  els.accountDropdown.hidden = true;
  els.accountTrigger.setAttribute("aria-expanded", "false");
}

function setTunnelMessage(text, tone) {
  els.tunnelMessage.textContent = text;
  els.tunnelMessage.className = "field-message" + (tone ? ` ${tone}` : "");
}

function setCommandsMessage(text, tone) {
  els.commandsMessage.textContent = text;
  els.commandsMessage.className = "field-message" + (tone ? ` ${tone}` : "");
}

function setPathsMessage(text, tone) {
  els.pathsMessage.textContent = text;
  els.pathsMessage.className = "field-message" + (tone ? ` ${tone}` : "");
}

function setProfileMessage(text, tone) {
  els.profileMessage.textContent = text;
  els.profileMessage.className = "field-message" + (tone ? ` ${tone}` : "");
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  const res = await fetch(path, { ...options, headers, credentials: "include" });
  if (res.status === 401 || res.status === 403) {
    window.location.href = "/agent/login.html";
    throw new Error("Unauthorized");
  }
  if (!res.ok) throw new Error(await res.text());
  return res;
}

function lines(values) { return (values || []).join("\n"); }
function splitLines(value) { return value.split(/\r?\n/).map((s) => s.trim()).filter(Boolean); }
function escapeHTML(v) { return String(v).replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" })[c]); }
function escapeAttr(v) { return String(v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]); }
function cssSafeAttr(v) { return v.replace(/[!"#$%&'()*+,.\/:;<=>?@[\\\]^`{|}~]/g, "\\$&"); }
function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let v = bytes, u = 0;
  while (v >= 1024 && u < units.length - 1) { v /= 1024; u++; }
  return `${v >= 10 || u === 0 ? Math.round(v) : v.toFixed(1)} ${units[u]}`;
}
