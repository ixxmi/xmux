const form = document.getElementById("loginForm");
const input = document.getElementById("adminToken");
const message = document.getElementById("loginMessage");

input.focus();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const token = input.value.trim();
  if (!token) {
    message.textContent = "Token is required.";
    return;
  }
  const response = await fetch("/cloud-terminal-api/admin/config", {
    headers: { Authorization: `Bearer ${token}` }
  });
  if (!response.ok) {
    message.textContent = "Admin token rejected.";
    return;
  }
  sessionStorage.setItem("cloud-terminal-admin-token", token);
  window.location.href = `/admin/?token=${encodeURIComponent(token)}`;
});
