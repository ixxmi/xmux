window.addEventListener("error", (event) => {
  console.error("[OAUTH-CLIENT] uncaught error in login.js — script may have stopped", event.message, event.filename, event.lineno);
});
window.addEventListener("unhandledrejection", (event) => {
  console.error("[OAUTH-CLIENT] unhandled rejection in login.js", event.reason);
});
console.log("[OAUTH-CLIENT] login.js loaded, XMuxPath present?", !!window.XMuxPath);

const tabs = document.getElementById("loginTabs");
const panes = document.querySelectorAll(".login-pane");

const loginForm = document.getElementById("loginForm");
const usernameInput = document.getElementById("usernameInput");
const passwordInput = document.getElementById("passwordInput");
const loginMessage = document.getElementById("loginMessage");
const googleLoginButton = document.getElementById("googleLoginButton");
const appPath = (window.XMuxPath && window.XMuxPath.path) || ((path) => path);

const registerForm = document.getElementById("registerForm");
const regUsername = document.getElementById("regUsername");
const regEmail = document.getElementById("regEmail");
const regEmailLabel = document.getElementById("regEmailLabel");
const regPassword = document.getElementById("regPassword");
const regHint = document.getElementById("regHint");
const regMessage = document.getElementById("regMessage");
const regCode = document.getElementById("regCode");
const regSendCodeButton = document.getElementById("regSendCodeButton");

const forgotForm = document.getElementById("forgotForm");
const forgotEmail = document.getElementById("forgotEmail");
const forgotMessage = document.getElementById("forgotMessage");

const fixupPane = document.getElementById("emailFixupPane");
const fixupEmail = document.getElementById("fixupEmail");
const fixupSubmit = document.getElementById("fixupSubmit");
const fixupCancel = document.getElementById("fixupCancel");
const fixupMessage = document.getElementById("fixupMessage");

let publicConfig = { require_email_on_register: true, oauth_google_enabled: false, password_policy: { min_length: 10 } };
let fixupContext = null;

usernameInput.focus();

tabs.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-tab]");
  if (!button) return;
  switchPane(button.dataset.tab);
});

document.querySelectorAll(".login-link[data-tab]").forEach((link) => {
  link.addEventListener("click", (event) => {
    event.preventDefault();
    switchPane(link.dataset.tab);
  });
});

function switchPane(tab) {
  document.querySelectorAll(".login-tab").forEach((btn) => btn.classList.toggle("active", btn.dataset.tab === tab));
  panes.forEach((pane) => {
    const match = pane.dataset.pane === tab;
    pane.hidden = !match;
    pane.classList.toggle("active", match);
  });
}

loadPublicConfig();

async function loadPublicConfig() {
  try {
    const response = await fetch(appPath("/cloud-terminal-api/auth/public-config"), { credentials: "same-origin" });
    if (response.ok) {
      publicConfig = await response.json();
    }
  } catch (err) {
    console.warn("public-config", err);
  }
  applyPublicConfig();
}

function applyPublicConfig() {
  const pwd = publicConfig.password_policy || {};
  const parts = [`至少 ${pwd.min_length || 10} 位`];
  if (pwd.require_upper) parts.push("含大写");
  if (pwd.require_lower) parts.push("含小写");
  if (pwd.require_digit) parts.push("含数字");
  if (pwd.require_symbol) parts.push("含特殊符号");
  regHint.textContent = "密码要求：" + parts.join("、");
  regEmailLabel.style.display = publicConfig.require_email_on_register ? "" : "none";
  googleLoginButton.hidden = !publicConfig.oauth_google_enabled;
}

loginForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const username = usernameInput.value.trim();
  const password = passwordInput.value;
  if (!username || !password) {
    loginMessage.textContent = "账号和密码不能为空";
    return;
  }
  setBusy(loginForm, true);
  loginMessage.textContent = "验证中...";
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/login"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ username, password }),
    });
    if (response.status === 401 || response.status === 403) {
      let body = {};
      try {
        body = await response.json();
      } catch {
        body = {};
      }
      if (body.requires_email) {
        fixupContext = { username, password };
        switchPane("emailFixup");
        fixupMessage.textContent = body.message || "请补填邮箱";
        loginMessage.textContent = "";
        return;
      }
      if (body.requires_verify) {
        loginMessage.textContent = body.message || "请先完成邮箱验证（查看邮箱链接）";
        return;
      }
      throw new Error(body.error || "登录失败");
    }
    if (!response.ok) {
      throw new Error(await response.text());
    }
    window.location.href = appPath("/user/");
  } catch (error) {
    loginMessage.textContent = cleanError(error.message || "登录失败");
  } finally {
    setBusy(loginForm, false);
  }
});

googleLoginButton.innerHTML = googleButtonContent();
googleLoginButton.addEventListener("click", (event) => {
  event.preventDefault();
  let startURL;
  try {
    startURL = googleStartURL("/user/");
  } catch (err) {
    console.warn("[OAUTH-CLIENT] googleStartURL threw, falling back to literal URL", err);
    startURL = "/cloud-terminal-api/accounts/oauth/google/start";
  }
  console.log("[OAUTH-CLIENT] google button clicked", { start_url: startURL });
  window.location.href = startURL;
});
console.log("[OAUTH-CLIENT] google login click handler attached");

function googleButtonContent() {
  return `<svg class="google-icon" viewBox="0 0 24 24" aria-hidden="true">
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

registerForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const username = regUsername.value.trim();
  const password = regPassword.value;
  const email = regEmail.value.trim();
  const code = regCode ? regCode.value.trim() : "";
  if (!username || !password) {
    regMessage.textContent = "账号和密码不能为空";
    return;
  }
  if (publicConfig.require_email_on_register && !email) {
    regMessage.textContent = "请填写邮箱";
    return;
  }
  if (publicConfig.require_email_on_register && !code) {
    regMessage.textContent = "请填写邮箱验证码";
    return;
  }
  setBusy(registerForm, true);
  regMessage.textContent = "提交中...";
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/register"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ username, password, email, code }),
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    window.location.href = appPath("/user/");
  } catch (error) {
    regMessage.textContent = cleanError(error.message || "注册失败");
  } finally {
    setBusy(registerForm, false);
  }
});

if (regSendCodeButton) {
  let codeCooldownTimer = 0;
  regSendCodeButton.addEventListener("click", async () => {
    const email = regEmail.value.trim();
    if (!email) {
      regMessage.textContent = "请先填写邮箱";
      regEmail.focus();
      return;
    }
    regSendCodeButton.disabled = true;
    regMessage.textContent = "验证码发送中...";
    try {
      const response = await fetch(appPath("/cloud-terminal-api/accounts/register/send-code"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ email }),
      });
      if (!response.ok) {
        throw new Error(await response.text());
      }
      regMessage.textContent = "验证码已发送，请到邮箱查收（10 分钟内有效）";
      let remaining = 60;
      const original = "发送验证码";
      window.clearInterval(codeCooldownTimer);
      regSendCodeButton.textContent = `${remaining} 秒后可重发`;
      codeCooldownTimer = window.setInterval(() => {
        remaining -= 1;
        if (remaining <= 0) {
          window.clearInterval(codeCooldownTimer);
          regSendCodeButton.disabled = false;
          regSendCodeButton.textContent = original;
        } else {
          regSendCodeButton.textContent = `${remaining} 秒后可重发`;
        }
      }, 1000);
    } catch (error) {
      regMessage.textContent = cleanError(error.message || "验证码发送失败");
      regSendCodeButton.disabled = false;
    }
  });
}

forgotForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const email = forgotEmail.value.trim();
  if (!email) {
    forgotMessage.textContent = "请填写邮箱";
    return;
  }
  setBusy(forgotForm, true);
  forgotMessage.textContent = "提交中...";
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
    forgotMessage.textContent = "如该邮箱存在，已发送重置链接，请查收。";
  } catch (error) {
    forgotMessage.textContent = cleanError(error.message || "提交失败");
  } finally {
    setBusy(forgotForm, false);
  }
});

fixupSubmit.addEventListener("click", async () => {
  if (!fixupContext) return;
  const email = fixupEmail.value.trim();
  if (!email) {
    fixupMessage.textContent = "请填写邮箱";
    return;
  }
  fixupMessage.textContent = "提交中...";
  fixupSubmit.disabled = true;
  try {
    const response = await fetch(appPath("/cloud-terminal-api/accounts/set-email"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({
        username: fixupContext.username,
        password: fixupContext.password,
        email,
      }),
    });
    if (!response.ok) {
      throw new Error(await response.text());
    }
    fixupMessage.textContent = "邮箱已绑定，已发送验证邮件，请查收。";
  } catch (error) {
    fixupMessage.textContent = cleanError(error.message || "提交失败");
  } finally {
    fixupSubmit.disabled = false;
  }
});

fixupCancel.addEventListener("click", () => {
  fixupContext = null;
  switchPane("login");
});

function setBusy(form, busy) {
  form.querySelectorAll("input,button").forEach((el) => {
    el.disabled = busy;
  });
}

function cleanError(value) {
  return String(value).replace(/^\d+\s*/, "").trim();
}
