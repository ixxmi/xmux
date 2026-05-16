# Architecture

## Target Topology

```text
Browser -> Gateway -> Edge Agent -> Local PTY
```

This repository currently runs Gateway and Edge Runtime in one process to keep the MVP simple. The interfaces are intentionally separated so `internal/edge` can later become a standalone agent connected by WSS or gRPC streams.

## Execution Contract

The runtime never passes browser input to `bash -c`.

```text
line input
  -> shellparse.ParseLine
  -> policy.Engine.Decide
  -> exec.CommandContext(bin, args...)
  -> pty.StartWithSize
  -> audit.JSONLWriter
```

Shell operators and expansion characters are rejected at parse time:

```text
; & | $ ` ( ) > <
```

## Production Hardening Checklist

- Replace the development token with a secret from a secret manager.
- Terminate TLS before the Gateway or serve HTTPS directly.
- Split Edge into a separate binary and require mTLS registration.
- Run Edge as a non-root user.
- Add per-user RBAC and per-edge ACL profiles.
- Persist audit logs to PostgreSQL or an append-only log backend.
- Sandbox execution with containers, seccomp, user namespaces, or platform-native profiles.
