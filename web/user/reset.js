const params = new URLSearchParams(window.location.search);
const token = params.get("token") || "";
const form = document.getElementById("resetForm");
const newPassword = document.getElementById("newPassword");
const confirmPassword = document.getElementById("confirmPassword");
const message = document.getElementById("resetMessage");
const hint = document.getElementById("resetHint");
const appPath = window.XMuxPath?.path || ((path) => path);

loadHint();

async function loadHint() {
  try {
    const r = await fetch(appPath("/cloud-terminal-api/auth/public-config"), { credentials: "same-origin" });
    if (!r.ok) return;
    const cfg = await r.json();
    const pwd = cfg.password_policy || {};
    const parts = [`至少 ${pwd.min_length || 10} 位`];
    if (pwd.require_upper) parts.push("含大写");
    if (pwd.require_lower) parts.push("含小写");
    if (pwd.require_digit) parts.push("含数字");
    if (pwd.require_symbol) parts.push("含特殊符号");
    hint.textContent = "新密码：" + parts.join("、");
  } catch {
    // noop
  }
}

if (!token) {
  message.textContent = "链接缺少 token，请重新点击邮件中的链接。";
  message.classList.add("error");
  form.querySelectorAll("input,button").forEach((el) => (el.disabled = true));
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (newPassword.value !== confirmPassword.value) {
    message.textContent = "两次输入的密码不一致";
    return;
  }
  setBusy(true);
  message.textContent = "提交中...";
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/reset-password"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ token, new_password: newPassword.value }),
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    message.classList.add("ok");
    message.textContent = "密码已重置，请用新密码登录。";
    form.querySelectorAll("input").forEach((el) => (el.disabled = true));
  } catch (error) {
    message.classList.add("error");
    message.textContent = String(error.message || "重置失败").replace(/^\d+\s*/, "");
  } finally {
    setBusy(false);
  }
});

function setBusy(busy) {
  form.querySelectorAll("input,button").forEach((el) => (el.disabled = busy));
}
