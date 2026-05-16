let token = new URLSearchParams(window.location.search).get("token") || sessionStorage.getItem("cloud-terminal-admin-token") || "";
if (!token) {
  window.location.href = "/admin/login.html";
}

const state = {
  config: null,
  currentPath: "",
  currentEntries: [],
  selectedCommand: ""
};

const els = {
  saveState: document.getElementById("saveState"),
  saveButton: document.getElementById("saveButton"),
  sectionEyebrow: document.getElementById("sectionEyebrow"),
  authToken: document.getElementById("authToken"),
  adminToken: document.getElementById("adminToken"),
  tunnelToken: document.getElementById("tunnelToken"),
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
  fileList: document.getElementById("fileList")
};

document.querySelectorAll(".nav-item").forEach((button) => {
  button.addEventListener("click", () => switchSection(button.dataset.section));
});
document.querySelectorAll("[data-command-tab]").forEach((button) => {
  button.addEventListener("click", () => switchCommandTab(button.dataset.commandTab));
});

els.saveButton.addEventListener("click", saveConfig);
els.addCommand.addEventListener("click", () => addCommand(`command_${Object.keys(state.config.commands).length + 1}`));
els.deleteCommand.addEventListener("click", deleteSelectedCommand);
els.rootButton.addEventListener("click", () => loadFS("/"));
els.upButton.addEventListener("click", () => {
  const parent = els.upButton.dataset.parent;
  if (parent) {
    loadFS(parent);
  }
});

[
  els.authToken,
  els.adminToken,
  els.tunnelToken,
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

async function loadConfig() {
  const response = await api("/cloud-terminal-api/admin/config");
  state.config = await response.json();
  renderConfig();
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
  els.authToken.value = state.config.auth_token || "";
  els.adminToken.value = state.config.admin_token || "";
  els.tunnelToken.value = state.config.tunnel_token || "";
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
    auth_token: els.authToken.value.trim(),
    admin_token: els.adminToken.value.trim(),
    tunnel_token: els.tunnelToken.value.trim(),
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
    sessionStorage.setItem("cloud-terminal-admin-token", state.config.admin_token);
    token = state.config.admin_token;
    renderConfig();
    els.saveState.textContent = "Saved, live";
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
  headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(path, { ...options, headers });
  if (response.status === 401 || response.status === 403) {
    sessionStorage.removeItem("cloud-terminal-admin-token");
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
