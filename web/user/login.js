const form = document.getElementById("loginForm");
const usernameInput = document.getElementById("usernameInput");
const passwordInput = document.getElementById("passwordInput");
const registerButton = document.getElementById("registerButton");
const message = document.getElementById("loginMessage");

usernameInput.focus();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  await submitAccount("/cloud-terminal-api/accounts/login");
});

registerButton.addEventListener("click", async () => {
  await submitAccount("/cloud-terminal-api/accounts/register");
});

async function submitAccount(path) {
  const username = usernameInput.value.trim();
  const password = passwordInput.value;
  if (!username || !password) {
    message.textContent = "账号和密码不能为空";
    return;
  }
  setBusy(true);
  message.textContent = "验证中...";
  try {
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ username, password })
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    window.location.href = "/user/";
  } catch (error) {
    message.textContent = cleanError(error.message || "登录失败");
  } finally {
    setBusy(false);
  }
}

function setBusy(busy) {
  usernameInput.disabled = busy;
  passwordInput.disabled = busy;
  form.querySelectorAll("button").forEach((button) => {
    button.disabled = busy;
  });
}

function cleanError(value) {
  return String(value).replace(/^\d+\s*/, "").trim();
}
