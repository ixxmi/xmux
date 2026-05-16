# 公网云端 + 本地 Agent 反向穿透使用文档

这个模式用于“公网服务器只部署网站和网关，本地电脑部署 Agent，手机通过公网访问并远程操控本地 Codex / Claude Code / Gemini CLI”。

核心链路：

```text
Mobile Browser
  -> HTTPS/WSS
  -> Public Cloud Gateway
  -> Reverse WebSocket Tunnel
  -> Local Edge Agent
  -> Local PTY
  -> codex / claude / gemini
```

公网服务器不需要主动访问用户电脑。本地 Agent 主动连出到公网 Gateway，并在这条 WSS 长连接上接收启动会话、输入、resize、文件浏览、diff、预览代理等请求。

## 一、运行模式

同一个可执行文件支持三种模式：

```bash
./cloud-terminal -mode local
./cloud-terminal -mode cloud
./cloud-terminal -mode agent
```

| 模式 | 部署位置 | 作用 |
| --- | --- | --- |
| `local` | 单机或内网 | Gateway 和本地 Runtime 在一个进程里 |
| `cloud` | 公网服务器 | 只提供 Web 页面、账号登录、WebSocket 网关、反向隧道服务端 |
| `agent` | 用户本地电脑 | 使用管理员在后台统一配置的网关地址和绑定会话主动连接公网 Gateway，真正启动本地 PTY 和 AI CLI |

## 二、账号模型

后台管理、手机工作台、聊天页、根终端页都统一使用云账号密码。浏览器登录后由后端写入 HttpOnly 会话 cookie；本地 Agent 使用管理员在后台访问控制里统一保存的网关地址和“使用当前用户”绑定会话连接云端隧道入口，不再单独填写用户名密码或网关地址。

账号保存在 `server.account_store_path`：

```yaml
server:
  admin_username: "admin"
  admin_password: "admin123456"
  account_store_path: "data/accounts.json"
  account_registration_enabled: true
```

建议部署流程：

1. 首次启动云端，用默认管理员 `admin` / `admin123456` 登录 `/admin/`。
2. 立刻修改 `server.admin_password` 并重启，或改配置后重新部署。
3. 管理员在后台创建/管理云账号，普通用户在手机端、聊天页和根终端页登录使用。
4. 在后台访问控制里配置云端网关地址、开启本地穿透，并点击“使用当前用户”绑定当前管理员会话。
5. 生产环境创建完账号后，把 `server.account_registration_enabled` 改为 `false`，避免开放注册。

即使关闭注册，当账号库为空时仍允许创建第一个账号，避免全新部署把自己锁在外面。

## 三、公网云端部署

公网服务器只需要运行 Gateway：

```bash
./cloud-terminal -mode cloud -config cloud.yaml
```

推荐云端 `cloud.yaml`：

```yaml
server:
  addr: "127.0.0.1:18001"
  admin_username: "admin"
  admin_password: "admin123456"
  account_store_path: "data/accounts.json"
  account_registration_enabled: true
  audit_log_path: "data/audit.jsonl"
  workbench_state_path: "data/workbench_sessions.json"
  allow_hosts:
    - "ess-ds.com:5955"
    - "cems.ess-ds.com:5955"
    - "cloud.ess-ds.com:5955"
  admin_ip_allowlist:
    - "127.0.0.1"
    - "你的管理端公网IP"

cloud_tunnel:
  enabled: false

edge:
  id: "cloud-gateway"
  name: "Cloud Gateway"
  work_dir: "."
  env:
    LANG: "C.UTF-8"
    TERM: "xterm-256color"
  command_timeout: "20s"
  max_output_bytes: 1048576
  preview_ports:
    - 3000
    - 5173
    - 8080

policy:
  allow_paths:
    - "."
  deny:
    - rm
    - sudo
    - su
    - bash
    - sh
    - zsh
  commands:
    codex:
      enabled: true
      interactive: true
      max_args: 12
    claude:
      enabled: true
      interactive: true
      max_args: 12
    gemini:
      enabled: true
      interactive: true
      max_args: 12
```

云端 `policy.allow_paths` 在 `cloud` 模式下不代表用户电脑真实路径；真实路径由本地 Agent 上报并在本地 Agent 侧强制校验。云端保留最小配置即可。

## 四、本地 Agent 部署

用户电脑运行：

```bash
./cloud-terminal -mode agent -config agent.yaml
```

Agent 只读取管理员在后台访问控制里保存到 `cloud_tunnel.discovery_url`（或兼容字段 `cloud_tunnel.gateway_url`）的地址；其他账号直接复用这份配置。Agent 启动时先 `GET ${discovery_url}/cloud-terminal-api/discovery/gateway` 拿到真正的网关地址，再建立 WSS 反向隧道；当 discovery 不可达时回退到本地配置的 `gateway_url`。

Agent 模式同时会在 `server.addr`（默认 `127.0.0.1:18001`）启动本地管理页面，浏览器访问 `http://127.0.0.1:18001/admin/` 即可用 `server.admin_username/admin_password` 登录，修改 `discovery_url`、策略、允许路径等配置无须重启进程；运行时配置会被反向隧道实时使用。

推荐本地 `agent.yaml`：

```yaml
server:
  addr: "127.0.0.1:18001"
  admin_username: "admin"
  admin_password: "admin123456"
  account_store_path: "data/accounts.json"
  account_registration_enabled: false
  audit_log_path: "data/audit.jsonl"
  workbench_state_path: "data/workbench_sessions.json"
  allow_hosts:
    - "127.0.0.1:18001"
  admin_ip_allowlist:
    - "127.0.0.1"
    - "::1"

cloud_tunnel:
  enabled: true
  discovery_url: "https://ess-ds.com:5955"

edge:
  id: "macbook-pro"
  name: "MacBook Pro"
  work_dir: "/Users/你的用户名/Documents/company/code"
  env:
    LANG: "C.UTF-8"
    TERM: "xterm-256color"
  command_timeout: "20s"
  max_output_bytes: 1048576
  preview_ports:
    - 3000
    - 5173
    - 8080

policy:
  allow_paths:
    - "/Users/你的用户名/Documents/company/code"
    - "/Users/你的用户名/Desktop"
  deny:
    - rm
    - reboot
    - shutdown
    - mkfs
    - dd
    - chmod
    - chown
    - sudo
    - su
    - bash
    - sh
    - zsh
    - fish
  commands:
    codex:
      enabled: true
      bin: "/opt/homebrew/bin/codex"
      interactive: true
      max_args: 12
    claude:
      enabled: true
      bin: "/opt/homebrew/bin/claude"
      interactive: true
      max_args: 12
    gemini:
      enabled: true
      bin: "/opt/homebrew/bin/gemini"
      interactive: true
      max_args: 12
    git:
      enabled: true
      bin: "git"
      subcommands:
        - status
        - diff
      max_args: 16
```

`bin` 必须是本地电脑上真实可执行路径。可以用下面命令查看：

```bash
which codex
which claude
which gemini
```

## 五、Nginx 反向代理

公网 Nginx 代理到云端 Gateway，例如 Gateway 监听 `127.0.0.1:18001`，公网端口是 `5955`：

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
}

upstream cloud_terminal_gateway {
    server 127.0.0.1:18001;
    keepalive 32;
}

server {
    listen 5955 ssl http2;
    server_name ess-ds.com cems.ess-ds.com cloud.ess-ds.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    client_max_body_size 16M;

    location / {
        proxy_pass http://cloud_terminal_gateway;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;

        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;

        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 24h;
        proxy_send_timeout 24h;
        proxy_set_header X-Accel-Buffering no;
    }
}
```

如果公网访问地址带端口，例如 `https://ess-ds.com:5955/mobile/`，需要把 `ess-ds.com:5955` 加入云端配置的 `server.allow_hosts`。否则 WebSocket Origin 会被拒绝。

## 六、手机端使用流程

1. 公网服务器启动云端：

   ```bash
   ./cloud-terminal -mode cloud -config cloud.yaml
   ```

2. 浏览器打开 `/admin/login.html`，使用管理员账号登录后台。

3. 在后台访问控制里配置云端网关地址、开启本地穿透，并点击“使用当前用户”后保存配置。

4. 本地电脑启动 Agent：

   ```bash
   ./cloud-terminal -mode agent -config agent.yaml
   ```

5. 手机打开：

   ```text
   https://ess-ds.com:5955/mobile/
   ```

6. 使用云账号登录。

7. 选择 Codex / Claude Code / Gemini。

8. 在允许路径范围内选择目录或文件。

9. 点击 Start，系统通过本地 Agent 启动真实的本地 CLI。

## 七、会话保持能力

已实现：

- 浏览器刷新、关闭再打开后，可通过保存的 `session_id` 重新 attach 云端会话。
- 浏览器 WebSocket 断开不会杀掉本地 PTY。
- 本地 Agent 和公网 Gateway 短暂断开后，Agent 会自动重连。
- Agent 重连时会把仍在运行的本地 PTY 会话重新上报给云端。
- 云端再次请求同一个 `session_id` 时，Agent 会复用已有 PTY，不会新开一个 CLI。
- 云端会持久化会话记录和最近终端回放到 `server.workbench_state_path`。

限制：

- 如果本地 Agent 进程退出，本地 PTY 会被系统结束，无法继续原 CLI。
- 如果公网云端进程重启，历史记录会从 `workbench_state_path` 恢复，但运行中的 WebSocket attach 状态需要等本地 Agent 重连后恢复。
- 当前会按账号隔离 Agent 连接和工作台会话；同一账号同时连接多个 Agent 时保留最近连接的一个。

## 八、后台管理

后台入口：

```text
https://ess-ds.com:5955/admin/
```

后台支持实时修改：

- 云账号数据库路径、注册开关和账号创建
- 本地穿透开关、云端网关地址，以及“使用当前用户”绑定当前会话
- WebSocket Origin allowlist
- 后台管理 IP allowlist
- 命令白名单和黑名单
- 全局可访问路径

保存后会写回当前启动使用的 YAML 配置，并立即更新内存配置，不需要重启服务。

云账号只有管理员可以管理；普通用户可以进入 `/user/` 管理自己账号的本地穿透开关、命令策略和允许文件路径，网关地址仍只由管理员统一配置。

在 `cloud` 模式下，管理员后台文件路径浏览的是公网服务器文件系统，不是用户本地电脑。普通用户后台会通过本地 Agent 在管理员全局允许路径内浏览和选择自己的可访问路径；手机端文件菜单只显示当前账号保存后的可访问路径。

## 九、安全建议

- 公网服务器只运行 `cloud` 模式，不在公网服务器上启用真实执行 Runtime。
- 本地 Agent 使用普通用户运行，不要用 root。
- `policy.allow_paths` 只配置需要远程操作的项目目录，不要配置 `/`。
- 生产环境创建账号后关闭 `server.account_registration_enabled`。
- `server.admin_ip_allowlist` 只允许固定管理 IP 或内网段。
- Nginx 必须启用 HTTPS，浏览器和 Agent 都走 WSS。
- AI CLI 命令只配置 `codex`、`claude`、`gemini` 这类交互式命令，不要把 `bash/sh/zsh` 放进白名单。
- Agent 所在机器需要自己安装并登录 Codex CLI、Claude Code、Gemini CLI。

## 十、故障排查

### 1. 手机页面一直 Connecting

检查本地 Agent 是否已经连接：

```text
level=INFO msg="agent tunnel connected"
level=INFO msg="edge agent tunnel connected"
```

如果云端没有 `edge agent tunnel connected`，说明本地 Agent 没连上公网 Gateway。确认后台访问控制里已经点击“使用当前用户”并保存配置。

### 2. Origin 不允许

日志：

```text
websocket: request origin not allowed by Upgrader.CheckOrigin
```

把浏览器实际访问的 host 加到云端 `server.allow_hosts`，例如：

```yaml
server:
  allow_hosts:
    - "ess-ds.com:5955"
    - "cloud.ess-ds.com:5955"
```

如果 Nginx 后面还有代理，确保传递：

```nginx
proxy_set_header Host $host;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-Proto $scheme;
```

### 3. Agent 返回 unauthorized

重新登录后台后点击“使用当前用户”并保存配置，确保本地配置里的 `cloud_tunnel.account` 和 `cloud_tunnel.session_id` 是最新会话。

### 4. 能进页面但没有目录

检查本地 `agent.yaml`：

```yaml
policy:
  allow_paths:
    - "/Users/你的用户名/Documents/company/code"
```

`/mobile/` 的目录列表来自本地 Agent，只会显示允许路径范围内的目录。

### 5. Codex / Claude / Gemini 启动失败

检查本地 CLI 路径和策略：

```bash
which codex
which claude
which gemini
```

配置示例：

```yaml
policy:
  commands:
    codex:
      enabled: true
      bin: "/opt/homebrew/bin/codex"
      interactive: true
      max_args: 12
```

### 6. 预览打不开本地页面

本地 Agent 只允许代理 `edge.preview_ports` 配置的端口：

```yaml
edge:
  preview_ports:
    - 3000
    - 5173
    - 8080
```

本地开发服务必须监听本机对应端口。手机端 Preview 页选择端口后，云端会通过本地 Agent 反向代理访问本地页面。

## 十一、构建发布

Web 页面使用 `go:embed` 编译进二进制。修改 `web/` 后重新构建即可：

```bash
go build -o cloud-terminal ./cmd/cloud-terminal
```

部署时只需要上传：

```text
cloud-terminal
cloud.yaml 或 agent.yaml
```

首次启动如果没有配置文件，会在当前目录自动生成默认 `policy.yaml`。
