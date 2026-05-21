const form = document.getElementById("loginForm");
const usernameInput = document.getElementById("usernameInput");
const passwordInput = document.getElementById("passwordInput");
const message = document.getElementById("loginMessage");
const cloudHint = document.getElementById("cloudHint");
const appPath = window.XMuxPath?.path || ((path) => path);

usernameInput.focus();

loadAgentConfig();

async function loadAgentConfig() {
  try {
    const cfg = await (await fetch(appPath("/cloud-terminal-api/agent/config"))).json();
    const cloud = cfg.discovery_url || cfg.gateway_url || "";
    if (cloud) {
      cloudHint.textContent = "云端网关：" + cloud;
    }
  } catch {
    // ignore
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const username = usernameInput.value.trim();
  const password = passwordInput.value;
  if (!username || !password) {
    message.textContent = "请填写账号和密码";
    return;
  }
  setBusy(true);
  message.textContent = "验证中...";
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/login"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
      credentials: "include"
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    window.location.href = appPath("/agent/");
  } catch (error) {
    message.textContent = cleanError(error.message || "登录失败");
  } finally {
    setBusy(false);
  }
});

function setBusy(busy) {
  usernameInput.disabled = busy;
  passwordInput.disabled = busy;
  form.querySelectorAll("button").forEach((b) => { b.disabled = busy; });
}

function cleanError(value) {
  return String(value).replace(/^\d+\s*/, "").trim() || "登录失败";
}
