const params = new URLSearchParams(window.location.search);
const token = params.get("token") || "";
const provider = params.get("provider") || "";
const form = document.getElementById("bindForm");
const bindPassword = document.getElementById("bindPassword");
const message = document.getElementById("bindMessage");
const appPath = window.XMuxPath?.path || ((path) => path);

if (!token || provider !== "google") {
  message.textContent = "链接无效，请重新发起 Google 登录。";
  message.classList.add("error");
  form.querySelectorAll("input,button").forEach((el) => (el.disabled = true));
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const password = bindPassword.value;
  if (!password) {
    message.textContent = "请填写密码";
    return;
  }
  setBusy(true);
  message.textContent = "绑定中...";
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/oauth/google/bind"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ token, password }),
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    window.location.href = appPath("/user/");
  } catch (error) {
    message.classList.add("error");
    message.textContent = String(error.message || "绑定失败").replace(/^\d+\s*/, "");
  } finally {
    setBusy(false);
  }
});

function setBusy(busy) {
  form.querySelectorAll("input,button").forEach((el) => (el.disabled = busy));
}
