const state = {
  settings: null,
  currentPath: "",
  entries: []
};

const els = {
  saveState: document.getElementById("saveState"),
  saveButton: document.getElementById("saveButton"),
  sectionEyebrow: document.getElementById("sectionEyebrow"),
  accountAvatar: document.getElementById("accountAvatar"),
  accountName: document.getElementById("accountName"),
  accountRole: document.getElementById("accountRole"),
  logoutButton: document.getElementById("logoutButton"),
  cloudTunnelEnabled: document.getElementById("cloudTunnelEnabled"),
  policySummary: document.getElementById("policySummary"),
  commandStats: document.getElementById("commandStats"),
  commandList: document.getElementById("commandList"),
  enableAllCommands: document.getElementById("enableAllCommands"),
  rootsButton: document.getElementById("rootsButton"),
  upButton: document.getElementById("upButton"),
  currentPath: document.getElementById("currentPath"),
  fileCount: document.getElementById("fileCount"),
  fileList: document.getElementById("fileList"),
  allowPaths: document.getElementById("allowPaths"),
  profileUsername: document.getElementById("profileUsername"),
  profileRole: document.getElementById("profileRole"),
  currentPassword: document.getElementById("currentPassword"),
  newPassword: document.getElementById("newPassword"),
  saveProfileButton: document.getElementById("saveProfileButton"),
  profileMessage: document.getElementById("profileMessage")
};

document.querySelectorAll(".nav-item").forEach((button) => {
  button.addEventListener("click", () => switchSection(button.dataset.section));
});

els.saveButton.addEventListener("click", saveSettings);
els.logoutButton.addEventListener("click", logout);
els.enableAllCommands.addEventListener("click", enableAllCommands);
els.rootsButton.addEventListener("click", () => loadFS(""));
els.upButton.addEventListener("click", () => {
  const parent = els.upButton.dataset.parent;
  if (parent) {
    loadFS(parent);
  } else {
    loadFS("");
  }
});
els.cloudTunnelEnabled.addEventListener("input", markDirty);
els.allowPaths.addEventListener("input", () => {
  syncAllowedButtons();
  markDirty();
});
els.saveProfileButton.addEventListener("click", saveProfile);

loadSettings();

async function loadSettings() {
  try {
    state.settings = await fetchJSON("/cloud-terminal-api/user/settings");
    renderSettings();
    await loadFS("");
  } catch (error) {
    if (/unauthorized/i.test(error.message)) {
      window.location.href = "/user/login.html";
      return;
    }
    els.saveState.textContent = error.message || "加载失败";
  }
}

function renderSettings() {
  const settings = state.settings || {};
  const role = settings.role === "admin" ? "管理员" : "用户";
  els.accountAvatar.textContent = avatarText(settings.account);
  els.accountName.textContent = settings.account || "-";
  els.accountRole.textContent = role;
  els.profileUsername.value = settings.account || "";
  els.profileRole.value = role;
  els.cloudTunnelEnabled.checked = Boolean(settings.cloud_tunnel_enabled);
  els.allowPaths.value = lines(settings.allow_paths);
  renderPolicySummary();
  renderCommands();
  syncAllowedButtons();
  els.saveState.textContent = "Loaded";
}

function renderPolicySummary() {
  const limits = state.settings?.policy_limits || {};
  const commands = limits.commands || {};
  const enabledCommands = Object.values(commands).filter((item) => item.enabled !== false).length;
  const paths = Array.isArray(state.settings?.allow_paths) ? state.settings.allow_paths : [];
  els.policySummary.innerHTML = `
    <div><span>账号路径</span><strong>${paths.length}</strong></div>
    <div><span>可用命令</span><strong>${enabledCommands}</strong></div>
    <div><span>黑名单</span><strong>${(limits.deny || []).length}</strong></div>
  `;
}

function renderCommands() {
  els.commandList.innerHTML = "";
  const limits = state.settings?.policy_limits?.commands || {};
  const commands = state.settings?.commands || {};
  const names = Object.keys(limits).sort();
  const denied = state.settings?.policy_limits?.deny || [];
  const enabledCount = names.filter((name) => {
    const limit = limits[name] || {};
    const command = commands[name] || limit;
    const disabledByAdmin = limit.enabled === false || denied.includes(name);
    return !disabledByAdmin && command.enabled;
  }).length;
  const interactiveCount = names.filter((name) => limits[name]?.interactive).length;
  const adminDisabledCount = names.filter((name) => limits[name]?.enabled === false || denied.includes(name)).length;
  els.commandStats.innerHTML = `
    <div><span>已启用</span><strong>${enabledCount}</strong></div>
    <div><span>PTY</span><strong>${interactiveCount}</strong></div>
    <div><span>受限</span><strong>${adminDisabledCount}</strong></div>
  `;
  if (names.length === 0) {
    els.commandList.innerHTML = '<div class="empty-state">管理员未配置可用命令。</div>';
    return;
  }
  for (const name of names) {
    const limit = limits[name] || {};
    const command = commands[name] || limit;
    const disabledByAdmin = limit.enabled === false || denied.includes(name);
    const enabled = Boolean(command.enabled && !disabledByAdmin);
    const typeLabel = command.interactive ? "PTY" : "命令";
    const statusLabel = disabledByAdmin ? "全局禁用" : enabled ? "已启用" : "已关闭";
    const row = document.createElement("label");
    row.className = `command-row ${enabled ? "enabled" : "disabled"} ${disabledByAdmin ? "admin-disabled" : ""}`;
    row.innerHTML = `
      <span class="command-switch">
        <input type="checkbox" data-command="${escapeAttr(name)}" ${enabled ? "checked" : ""} ${disabledByAdmin ? "disabled" : ""} />
        <span aria-hidden="true"></span>
      </span>
      <span class="command-main">
        <strong>${escapeHTML(name)}</strong>
        <small>${typeLabel}${limit.max_args ? ` · ${limit.max_args} args` : ""}</small>
      </span>
      <span class="command-status">${statusLabel}</span>
    `;
    row.querySelector("input").addEventListener("input", (event) => {
      row.classList.toggle("enabled", event.target.checked);
      row.classList.toggle("disabled", !event.target.checked);
      const status = row.querySelector(".command-status");
      if (status && !disabledByAdmin) {
        status.textContent = event.target.checked ? "已启用" : "已关闭";
      }
      renderCommandStatsOnly();
      markDirty();
    });
    els.commandList.appendChild(row);
  }
}

function enableAllCommands() {
  els.commandList.querySelectorAll("input[type='checkbox']:not(:disabled)").forEach((input) => {
    input.checked = true;
    input.closest(".command-row")?.classList.add("enabled");
    input.closest(".command-row")?.classList.remove("disabled");
    const status = input.closest(".command-row")?.querySelector(".command-status");
    if (status) {
      status.textContent = "已启用";
    }
  });
  renderCommandStatsOnly();
  markDirty();
}

function renderCommandStatsOnly() {
  const rows = Array.from(els.commandList.querySelectorAll(".command-row"));
  if (rows.length === 0) {
    return;
  }
  const enabledCount = rows.filter((row) => row.querySelector("input")?.checked && !row.classList.contains("admin-disabled")).length;
  const interactiveCount = rows.filter((row) => row.querySelector(".command-main small")?.textContent.includes("PTY")).length;
  const adminDisabledCount = rows.filter((row) => row.classList.contains("admin-disabled")).length;
  els.commandStats.innerHTML = `
    <div><span>已启用</span><strong>${enabledCount}</strong></div>
    <div><span>PTY</span><strong>${interactiveCount}</strong></div>
    <div><span>受限</span><strong>${adminDisabledCount}</strong></div>
  `;
}

function collectCommands() {
  const next = {};
  const limits = state.settings?.policy_limits?.commands || {};
  const current = state.settings?.commands || {};
  for (const name of Object.keys(limits)) {
    const box = els.commandList.querySelector(`input[data-command="${cssEscape(name)}"]`);
    const limit = limits[name] || {};
    const base = current[name] || limit;
    next[name] = Object.assign({}, base, {
      enabled: limit.enabled !== false && Boolean(box && box.checked),
      interactive: Boolean(limit.interactive),
      max_args: Number(limit.max_args || 0)
    });
  }
  return next;
}

async function saveSettings() {
  els.saveButton.disabled = true;
  els.saveState.textContent = "Saving...";
  try {
    state.settings = await fetchJSON("/cloud-terminal-api/user/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        cloud_tunnel_enabled: els.cloudTunnelEnabled.checked,
        commands: collectCommands(),
        allow_paths: splitLines(els.allowPaths.value)
      })
    });
    renderSettings();
    els.saveState.textContent = "Saved, live";
  } catch (error) {
    els.saveState.textContent = error.message || "保存失败";
  } finally {
    els.saveButton.disabled = false;
  }
}

async function saveProfile() {
  const currentPassword = els.currentPassword.value;
  const newPassword = els.newPassword.value;
  if (!currentPassword || !newPassword) {
    els.profileMessage.textContent = "请输入当前密码和新密码";
    return;
  }
  els.saveProfileButton.disabled = true;
  els.profileMessage.textContent = "保存中...";
  try {
    await fetchJSON("/cloud-terminal-api/accounts/me", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
    });
    els.currentPassword.value = "";
    els.newPassword.value = "";
    els.profileMessage.textContent = "密码已更新";
  } catch (error) {
    els.profileMessage.textContent = error.message || "保存失败";
  } finally {
    els.saveProfileButton.disabled = false;
  }
}

async function logout() {
  await fetchJSON("/cloud-terminal-api/accounts/logout", { method: "POST" }).catch(() => null);
  window.location.href = "/user/login.html";
}

async function loadFS(path) {
  els.fileList.innerHTML = '<div class="empty-state">Loading...</div>';
  try {
    const data = await fetchJSON(`/cloud-terminal-api/user/fs?path=${encodeURIComponent(path || "")}`);
    state.currentPath = data.path || "";
    state.entries = data.entries || [];
    els.currentPath.textContent = data.path || "账号路径";
    els.upButton.dataset.parent = data.parent || "";
    els.upButton.disabled = !data.parent && Boolean(data.path);
    renderFiles();
  } catch (error) {
    els.fileList.innerHTML = `<div class="empty-state">${escapeHTML(error.message || "加载失败")}</div>`;
  }
}

function renderFiles() {
  els.fileList.innerHTML = "";
  els.fileCount.textContent = `${state.entries.length} 项`;
  if (state.entries.length === 0) {
    els.fileList.innerHTML = '<div class="empty-state">当前账号没有可访问路径。请先在已绑定客户端的文件路径页添加并保存。</div>';
    return;
  }
  for (const entry of state.entries) {
    const row = document.createElement("div");
    row.className = `file-row ${entry.is_dir ? "is-dir" : "is-file"}`;
    row.innerHTML = `
      <div class="file-name">
        <span class="file-icon ${entry.is_dir ? "dir-icon" : "file-icon-doc"}" aria-hidden="true"></span>
        <div class="file-text">
          <span class="file-label">${escapeHTML(entry.name || entry.path)}</span>
          <span class="file-meta">${escapeHTML(entry.path)}${entry.is_dir ? "" : ` · ${formatBytes(entry.size || 0)}`}</span>
        </div>
      </div>
      <button class="file-action open-action" type="button">${entry.is_dir ? "Open" : "Select"}</button>
      <button class="file-action path-toggle" type="button" data-path="${escapeAttr(entry.path)}"></button>
    `;
    row.querySelector(".open-action").addEventListener("click", () => {
      if (entry.is_dir) {
        loadFS(entry.path);
      } else {
        addAllowPath(entry.path);
      }
    });
    row.querySelector(".path-toggle").addEventListener("click", () => toggleAllowPath(entry.path));
    els.fileList.appendChild(row);
  }
  syncAllowedButtons();
}

function addAllowPath(path) {
  const values = new Set(splitLines(els.allowPaths.value));
  values.add(path);
  setAllowPaths(Array.from(values));
  markDirty();
}

function toggleAllowPath(path) {
  const values = new Set(splitLines(els.allowPaths.value));
  if (values.has(path)) {
    values.delete(path);
  } else {
    values.add(path);
  }
  setAllowPaths(Array.from(values));
  markDirty();
}

function setAllowPaths(paths) {
  els.allowPaths.value = lines(paths);
  syncAllowedButtons();
}

function syncAllowedButtons() {
  const allowed = new Set(splitLines(els.allowPaths.value));
  document.querySelectorAll(".path-toggle").forEach((button) => {
    const isAllowed = allowed.has(button.dataset.path);
    button.textContent = isAllowed ? "取消" : "允许";
    button.classList.toggle("primary", !isAllowed);
    button.classList.toggle("cancel", isAllowed);
  });
}

function switchSection(section) {
  document.querySelectorAll(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.section === section));
  document.querySelectorAll(".section").forEach((item) => item.classList.toggle("active", item.id === `section-${section}`));
  const active = document.querySelector(`.nav-item[data-section="${section}"]`);
  if (active) {
    els.sectionEyebrow.textContent = active.textContent;
  }
}

async function fetchJSON(path, options = {}) {
  const response = await fetch(path, Object.assign({ credentials: "same-origin" }, options));
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(cleanError(detail || `HTTP ${response.status}`));
  }
  return response.json();
}

function markDirty() {
  els.saveState.textContent = "Unsaved";
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

function cleanError(value) {
  return String(value).replace(/^\d+\s*/, "").trim();
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

function escapeAttr(value) {
  return escapeHTML(value).replace(/`/g, "&#096;");
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
