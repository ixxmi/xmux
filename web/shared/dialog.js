(function () {
  if (window.XDialog) {
    return;
  }

  const ROOT_ID = "x-dialog-root";

  function ensureRoot() {
    let root = document.getElementById(ROOT_ID);
    if (!root) {
      root = document.createElement("div");
      root.id = ROOT_ID;
      document.body.appendChild(root);
    }
    return root;
  }

  function buildModal({ title, message, kind, defaultValue, okText, cancelText, danger }) {
    const overlay = document.createElement("div");
    overlay.className = "x-dialog-overlay";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");

    const box = document.createElement("div");
    box.className = "x-dialog";

    if (title) {
      const head = document.createElement("div");
      head.className = "x-dialog-head";
      head.textContent = title;
      box.appendChild(head);
    }

    const body = document.createElement("div");
    body.className = "x-dialog-body";
    const text = document.createElement("p");
    text.className = "x-dialog-text";
    text.textContent = message || "";
    body.appendChild(text);

    let input = null;
    if (kind === "prompt") {
      input = document.createElement("input");
      input.type = "text";
      input.className = "x-dialog-input";
      input.value = defaultValue != null ? String(defaultValue) : "";
      body.appendChild(input);
    }
    box.appendChild(body);

    const footer = document.createElement("div");
    footer.className = "x-dialog-actions";

    let cancelBtn = null;
    if (kind !== "alert") {
      cancelBtn = document.createElement("button");
      cancelBtn.type = "button";
      cancelBtn.className = "x-dialog-button x-dialog-cancel";
      cancelBtn.textContent = cancelText || "取消";
      footer.appendChild(cancelBtn);
    }

    const okBtn = document.createElement("button");
    okBtn.type = "button";
    okBtn.className = "x-dialog-button x-dialog-ok" + (danger ? " x-dialog-danger" : "");
    okBtn.textContent = okText || "确定";
    footer.appendChild(okBtn);
    box.appendChild(footer);

    overlay.appendChild(box);
    return { overlay, okBtn, cancelBtn, input };
  }

  function open(opts) {
    const root = ensureRoot();
    const { overlay, okBtn, cancelBtn, input } = buildModal(opts);
    root.appendChild(overlay);

    return new Promise((resolve) => {
      let settled = false;
      const finish = (value) => {
        if (settled) return;
        settled = true;
        document.removeEventListener("keydown", onKey, true);
        overlay.classList.add("x-dialog-closing");
        window.setTimeout(() => overlay.remove(), 120);
        resolve(value);
      };

      const cancel = () => finish(opts.kind === "confirm" ? false : opts.kind === "prompt" ? null : undefined);
      const accept = () => {
        if (opts.kind === "prompt") {
          finish(input ? input.value : "");
        } else if (opts.kind === "confirm") {
          finish(true);
        } else {
          finish(undefined);
        }
      };

      okBtn.addEventListener("click", accept);
      if (cancelBtn) cancelBtn.addEventListener("click", cancel);
      overlay.addEventListener("mousedown", (event) => {
        if (event.target === overlay) cancel();
      });

      const onKey = (event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          cancel();
        } else if (event.key === "Enter") {
          if (input && event.target !== input) return;
          event.preventDefault();
          accept();
        }
      };
      document.addEventListener("keydown", onKey, true);

      window.requestAnimationFrame(() => {
        overlay.classList.add("x-dialog-open");
        if (input) {
          input.focus();
          input.select();
        } else {
          okBtn.focus();
        }
      });
    });
  }

  window.XDialog = {
    alert(message, options = {}) {
      return open({ kind: "alert", message, ...options });
    },
    confirm(message, options = {}) {
      return open({ kind: "confirm", message, ...options });
    },
    prompt(message, defaultValue = "", options = {}) {
      return open({ kind: "prompt", message, defaultValue, ...options });
    },
  };
})();
