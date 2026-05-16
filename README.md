# Cloud Terminal + Edge Runtime

一个 Go 实现的受控远程终端 MVP。浏览器通过 WebSocket 连接 Gateway，Gateway 调用本地 Edge Runtime；Edge 不接收裸 shell，而是把输入解析为结构化命令，经过白名单策略后用 PTY 执行单次命令，并写入 JSONL 审计日志。

## 架构

```text
Browser -> Gateway -> Edge Runtime -> Local PTY
```

当前仓库为了便于开发把 Gateway 和 Edge Runtime 放在一个进程中，代码边界仍按未来拆分设计：

```text
cmd/cloud-terminal        # 启动入口
internal/gateway          # HTTP / WebSocket / Auth / Security headers
internal/edge             # 受控执行 runtime
internal/policy           # 白名单、deny、路径限制
internal/shellparse       # mini-shell 解析器，禁止 shell 运算符
internal/audit            # JSONL 审计
web                       # xterm.js Web UI，会编译进可执行文件
web/mobile                # 手机端 AI Remote Workbench
web/chat                  # 企业微信式 AI Chat
policy.yaml               # 运行时安全配置，首次启动自动生成
```

现在也支持公网云端 + 本地 Agent 反向穿透部署：公网服务器只运行网站和 Gateway，本地电脑运行 Agent 主动连出，并在本地启动 Codex / Claude Code / Gemini。完整部署文档见 [docs/reverse-tunnel.md](docs/reverse-tunnel.md)。

## 运行

开发运行：

```bash
go mod tidy
go run ./cmd/cloud-terminal
```

生产部署可以只部署一个可执行文件：

```bash
go build -o cloud-terminal ./cmd/cloud-terminal
./cloud-terminal
```

运行模式：

```bash
./cloud-terminal -mode local
./cloud-terminal -mode cloud
./cloud-terminal -mode agent -gateway https://cloud.example.com:5955
```

`local` 是单机模式；`cloud` 是公网 Gateway 模式；`agent` 是本地电脑主动连接公网 Gateway 的执行端模式。

Web 终端和后台管理页面都会被 `go:embed` 编译进二进制，xterm.js 依赖也已放在 `web/vendor` 本地资源中，运行时不需要额外部署 `web/` 目录，也不依赖 CDN。

首次启动时，如果当前工作目录没有 `policy.yaml`，程序会自动生成默认配置文件：

```text
./policy.yaml
```

也可以显式指定配置路径：

```bash
./cloud-terminal -config /etc/cloud-terminal/policy.yaml
```

打开：

```text
http://127.0.0.1:18001
```

默认入口会跳转到手机端 AI 工作台 `/mobile/`。

默认开发令牌：

```text
change-me-terminal-token
```

后台管理入口：

```text
http://127.0.0.1:18001/admin/
```

手机端 AI 工作台入口：

```text
http://127.0.0.1:18001/mobile/
```

聊天式 AI 入口：

```text
http://127.0.0.1:18001/chat/
```

默认后台管理令牌：

```text
change-me-admin-token
```

本地 Agent 连接公网 Gateway 使用独立隧道令牌：

```yaml
server:
  tunnel_token: change-me-tunnel-token
```

上线前必须修改 `policy.yaml` 里的 `server.auth_token` 和 `server.admin_token`，并通过反向代理提供 HTTPS/WSS。
后台管理使用独立的 `server.admin_token` 和 `server.admin_ip_allowlist`，建议只允许内网或本机 IP 访问。

如果通过局域网 IP、反向代理域名或非默认端口访问，把浏览器实际访问的 host 加到 `server.allow_hosts`。同源访问会自动通过，例如页面是 `http://127.0.0.1:18001` 时，WebSocket Origin 也是这个 host，不需要额外配置。

## 安全模型

这个项目刻意不实现“网页 SSH”。默认执行链路是：

```text
用户输入 -> parser -> policy -> PTY executor -> audit
```

关键约束：

- 禁止 `; && || | $ \` () > >> <` 等 shell 语法。
- 默认 deny `rm/sudo/su/bash/sh/zsh/python/node` 等逃逸或高危命令。
- 命令必须在 `policy.commands` 中启用。
- 只有显式配置 `interactive: true` 的命令可以进入长生命周期交互式 PTY，例如 `codex`、`claude`、`gemini`。
- `/mobile/` 会把 AI CLI PTY 会话保存在后端进程中，浏览器刷新、关闭或网络短断后可用同一个 `session_id` 重新 attach，并回放最近 4MB 终端输出。
- `cloud` + `agent` 模式下，云端只负责鉴权、页面、会话记录和 WSS 隧道；真实目录、文件读取、PTY、AI CLI 都在本地 Agent 侧执行。
- `policy.allow_paths` 是全局可访问路径，所有会读取绝对路径参数的命令都会先经过这个边界检查。
- 一个命令创建一个 PTY，会话结束后关闭，不共享长期 shell 状态。
- 执行超时和最大输出大小由配置限制。
- 每次允许或拒绝都会写入 `data/audit.jsonl`。

## 策略示例

```yaml
policy:
  allow_paths:
    - /var/log
    - /tmp

  deny:
    - rm
    - sudo
    - bash
    - sh

  commands:
    ls:
      enabled: true
      max_args: 8

    cat:
      enabled: true
      max_args: 8

    kubectl:
      enabled: true
      subcommands:
        - get
        - describe
        - logs
      max_args: 16
```

## API 协议

浏览器可发普通 mini-shell 行：

```json
{
  "type": "exec",
  "line": "ls -la"
}
```

交互式 TUI 命令使用：

```json
{
  "type": "interactive_start",
  "line": "codex"
}
```

进入交互式模式后，浏览器会把 xterm.js 的按键数据原样通过 `interactive_input` 转发给 PTY，并通过 `resize` 同步窗口尺寸。该模式只对策略里启用 `interactive: true` 的命令开放。

## 后台管理

`/admin/` 是独立管理页面，支持：

- 修改终端访问 token 和后台管理 token。
- 配置 Gateway Origin allowlist。
- 配置后台管理 IP allowlist，支持单 IP 和 CIDR。
- 管理命令白名单、黑名单、子命令、二进制路径、交互式开关和最大参数数。
- 浏览本机目录树，并把选择的目录或文件写入全局 `policy.allow_paths`。

保存后配置会写回启动时使用的配置文件，默认是当前工作目录的 `policy.yaml`，并立即更新内存配置；下一次终端鉴权、WebSocket Origin 检查、管理页 IP 检查和命令策略判断都会使用新配置，不需要重启服务。

## 手机端 AI Workbench

`/mobile/` 是独立页面，不影响现有 `/` 终端页。它面向手机远程接管电脑上的 Codex CLI、Claude Code 或 Gemini CLI：

- 首次进入输入终端鉴权 token，后端写入 HttpOnly cookie。
- 鉴权后会先选择 AI CLI 类型，再浏览 `policy.allow_paths` 允许范围内的目录或文件，选择目标后才启动。
- 选择目录时 AI CLI 在该目录启动；选择文件时在文件所在目录启动，并把文件名作为启动参数。
- 手机端把 `session_id` 保存在浏览器本地；刷新或重新打开页面后可通过 Reconnect attach 到原会话。
- WebSocket 断开只会 detach，不会杀掉 PTY；只有点 Stop 或 CLI 自己退出才会结束当前会话。
- New 会启动一个新的会话；旧会话如果还在运行，会继续保留，可通过已保存的 `session_id` 重新 attach。
- 同一个文件夹下可以分别开启 Codex、Claude Code、Gemini 会话，进程抽屉按 agent 标识区分。
- Terminal 页支持 xterm 原始交互，并提供 Esc、Tab、方向键、Ctrl+C、Ctrl+D 快捷键栏。
- Files 页可以浏览 `policy.allow_paths` 允许范围内的文件，并查看文件内容。
- Diff 页显示当前工作区的 `git status`、`git diff --stat` 和 `git diff`。
- Preview 页可以打开本机开发服务器，例如 `localhost:3000`、`localhost:5173`、`localhost:8080`。

可预览端口由配置控制：

```yaml
edge:
  preview_ports:
    - 3000
    - 5173
    - 8080
```

AI CLI 必须仍然在命令策略里启用交互式 PTY：

```yaml
policy:
  commands:
    codex:
      enabled: true
      bin: /opt/homebrew/bin/codex
      interactive: true
      max_args: 12
    claude:
      enabled: true
      bin: claude
      interactive: true
      max_args: 12
    gemini:
      enabled: true
      bin: gemini
      interactive: true
      max_args: 12
```

## 聊天式 AI

`/chat/` 是 Codex App 风格的轻量聊天入口，同样复用 `/cloud-terminal-api/ws/workbench` 的持久 AI CLI PTY 会话：

- 首次进入输入终端鉴权 token。
- 顶部显示连接状态、工作区、当前会话、agent、节点和历史会话数量。
- 中间是 Request / 当前 Agent / Status 信息块，空会话会展示常用任务入口。
- 底部输入框支持 Enter 发送，Shift+Enter 换行。
- 浏览器刷新后会重新 attach 原会话；聊天记录保存在当前浏览器 localStorage，AI CLI 会话保存在后端进程内存。
- Stop 会结束当前会话，New 会创建新会话。

这个页面适合手机上用自然语言持续驱动 AI CLI；需要文件树、diff、预览时使用 `/mobile/`。

也可以发结构化命令：

```json
{
  "type": "tool",
  "command": "kubectl",
  "args": ["get", "pods", "-A"]
}
```

返回：

```json
{
  "type": "result",
  "stdout": "...",
  "stderr": "...",
  "exit_code": 0,
  "duration": "12ms"
}
```

## 下一步建议

- Gateway 和 Edge 拆成两个二进制，用 mTLS/gRPC stream 连接。
- 增加用户体系、RBAC、组织/项目隔离。
- 审计日志落 PostgreSQL，并支持 session replay。
- 为 Kubernetes、Docker、文件读取等高频动作做真正 tool API，而不暴露通用命令。
- 对 Edge 执行层增加 Docker / user namespace / seccomp 沙箱。
