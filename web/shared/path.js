(function () {
  if (window.XMuxPath) {
    return;
  }

  const markers = [
    "/cloud-terminal-api",
    "/admin",
    "/user",
    "/mobile",
    "/chat",
    "/agent",
    "/preview",
    "/shared",
    "/vendor",
    "/index.html",
    "/app.js",
    "/styles.css",
  ];

  function externalBasePath(pathname) {
    const value = String(pathname || window.location.pathname || "");
    let best = -1;
    for (const marker of markers) {
      const index = value.indexOf(marker);
      if (index < 0) {
        continue;
      }
      const after = index + marker.length;
      if (after !== value.length && value[after] !== "/") {
        continue;
      }
      if (best === -1 || index < best) {
        best = index;
      }
    }
    if (best <= 0) {
      return "";
    }
    return cleanPrefix(value.slice(0, best));
  }

  function cleanPrefix(prefix) {
    let value = String(prefix || "").trim();
    if (!value || value === "/" || !value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) {
      return "";
    }
    value = value.replace(/\/+/g, "/").replace(/\/$/, "");
    return value === "/" ? "" : value;
  }

  function normalizeTarget(target) {
    let value = String(target || "/");
    if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith("//")) {
      return value;
    }
    if (!value.startsWith("/")) {
      value = "/" + value;
    }
    return value;
  }

  function path(target) {
    const value = normalizeTarget(target);
    if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith("//")) {
      return value;
    }
    if (value === "/cloud-terminal-api" || value.startsWith("/cloud-terminal-api/")) {
      return value;
    }
    return pagePath(value);
  }

  function pagePath(target) {
    const value = normalizeTarget(target);
    if (/^[a-z][a-z0-9+.-]*:/i.test(value) || value.startsWith("//")) {
      return value;
    }
    const base = externalBasePath();
    if (!base) {
      return value;
    }
    const pathEnd = value.search(/[?#]/);
    const pathPart = pathEnd === -1 ? value : value.slice(0, pathEnd);
    if (pathPart === base || pathPart.startsWith(base + "/")) {
      return value;
    }
    return base + value;
  }

  function websocketURL(target) {
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${protocol}//${window.location.host}${normalizeTarget(target)}`;
  }

  window.XMuxPath = {
    basePath: externalBasePath,
    path,
    pagePath,
    websocketURL,
  };
})();
