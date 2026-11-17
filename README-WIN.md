# opencode2api Windows 独立测试版

这个目录是 Windows 二次开发的**独立 Git 仓库**：以主仓库 `2bf547c` 为基线，2026-11-01 合并上游 `v1.4.6`（`58f5667`），2026-11-16/17 对齐上游 `v1.4.8`（`159d7fdf`），Windows 版本号为 **`v1.4.8-win.1`**。源码、WebUI、文档和 Windows 运行脚本都在这里，修改不会影响上一级目录。

完整文档在 [`doc/README.md`](doc/README.md)，当前版本的完整基线、验证矩阵和回滚步骤见 [`doc/12-版本基线-v1.4.8-win.1.md`](doc/12-版本基线-v1.4.8-win.1.md)。仓库同时备份在 Forgejo `xiaoming/opencode2api-win`，换机恢复和 Release 资产见 [`doc/08-备份与恢复.md`](doc/08-备份与恢复.md)。

- 构建环境：Windows 11 x64，Go 1.24.0，`CGO_ENABLED=0`，`-trimpath`
- 二进制：`opencode2api.exe`（约 19.9 MB，静态构建，不需要额外 DLL）
- 配置：`config.json`，首次启动已把 WebUI 密码转成 Argon2id 哈希
- 默认只监听本机回环地址；当前测试配置为 `0.0.0.0:28080`，请自行确认防火墙范围

## 当前版本（2026-11-17）

| 项目 | 值 |
|---|---|
| 版本 | `v1.4.8-win.1` |
| 上游基线 | `v1.4.8`（`159d7fdf`，2026-11-16T21:34Z） |
| 构建命令 | `go build -trimpath -ldflags "-X main.version=v1.4.8-win.1" -o opencode2api.exe ./cmd/opencode2api` |
| SHA-256 | `BAB8F334330E226CD1CF31A4AEA7FCFCDBB4F3EFEDC533EE4F1946E75F9BC90B` |
| 运行进程（基线记录） | PID 25236，2026-11-16 23:09:32 启动 |
| 上游 Key | 无（`zen_keys=[]`、`go_keys=[]`），只跑匿名免费道 |
| 模型目录 | 86 个，暴露 12 个 |
| 免费道验证 | 极简 `200/29.1s`；带 tools 非流式 `200/35.1s`；带 tools 流式 `200/8.0s` |

本次版本的核心修复是上游 `#182` 的工具名前缀：旧二进制仍发送 `clint_`，免费层会直接返回 `429 Free usage limit`；新版本统一改为 `mcp_`。另外补上了 v1.4.8 的重试硬化、title 请求回退、`prompt_environment`、相邻 assistant 消息合并、Playground 固定中转和 Go 订阅占位。完整清单见 `CHANGELOG.md` 和 `doc/12`。

## 当前测试地址

| 项目 | 值 |
|---|---|
| API Base | `http://127.0.0.1:28080/v1` |
| WebUI | `http://127.0.0.1:28081` |
| API Key | `sk-win-local-test` |
| WebUI 用户名 | `admin` |
| WebUI 密码 | `win-admin-2026` |

WebUI 密码是重置过的，`win-admin-test` 已经失效。

启动后先看健康检查：

```powershell
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
```

`ready: true` 表示 API 已经可以接请求；`status: degraded` 通常只是模型目录还在刷新，等几秒会变成 `ok`。重点看 `warp` 字段：

```json
{"status":"ready","stack":"ipv6","size":32,"preload":12,"active":8,"standby":7,"cooling":4}
```

`active=0` 且 `cooling` 很多时，免费道暂时没有可用出口，等 2–4 分钟池子回血，或按 `doc/12` 的步骤重置 `config.json.warp-state.json`。

## 上游同步

- 本目录是独立仓库，`upstream` 指向主仓库。Windows 侧 WARP 改动（物理网卡绑定、启动失败后台重试、SOCKS5 UDP、池/注册器加固）全部保留。
- 2026-11-01 合并上游 `v1.4.6`（`58f5667`，合并提交 `a93d805`）：暗色主题、请求/上游尝试分页、`POST /api/models/probe`、配置页代理 chip 折叠。
- 2026-11-16/17 对齐上游 `v1.4.8`（`159d7fdf`）：`mcp_` 工具前缀（#182）、重试与响应所有权硬化（Refs #173/175/176/178/179/181）、title 单次尝试与本地回退、`prompt_environment`、legacy `functions` 一次改名、相邻 assistant 合并、Playground 固定中转（#167）、Go 订阅占位（#168）。
- v1.4.8 之后的上游提交（例如未发版的 `0d05fe93`：匿名默认关闭 + 批量代理管理）**尚未移植**；升级前先确认上游是否已发版。
- 以后同步：`git fetch upstream main && git merge upstream/main`，合并前确认工作区状态可回滚。工作区故意保留本地改动时，先做补丁或备份。

## 启动和停止

前台运行（关闭窗口即停止）：

```powershell
.\start-opencode2api.ps1
```

或者双击 `start-opencode2api.bat`。两个入口都调用 `start-supervised.ps1`，把网关、`tools\edgeproxy.exe` 和控制台放进同一个 Windows Job Object（`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）：关闭窗口、Ctrl+C 或结束 PowerShell 时，网关和边缘代理会一起退出，不残留 28080/28081/29090 监听。WARP 在进程内用 netstack 完成，不建网卡、不改路由/DNS/防火墙。

后台运行（日志写到 `logs\`，PID 写到 `logs\pid.txt`）：

```powershell
.\start-background.ps1
```

停止（两种模式都适用）：

```powershell
.\stop-opencode2api.ps1
```

## 测试 API

列出模型（需要 API Key）：

```powershell
curl.exe --noproxy "*" `
  -H "Authorization: Bearer sk-win-local-test" `
  http://127.0.0.1:28080/v1/models
```

发一个对话请求。Windows PowerShell 直接传带双引号的 JSON 容易把引号吃掉，建议先写文件再 `--data-binary`：

```powershell
$body = '{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"Reply with OK only."}],"max_tokens":16,"stream":false}'
[IO.File]::WriteAllText("$PWD\request.json", $body, (New-Object System.Text.UTF8Encoding($false)))

curl.exe --noproxy "*" -H "Authorization: Bearer sk-win-local-test" `
  -H "Content-Type: application/json" `
  --data-binary "@$PWD\request.json" `
  http://127.0.0.1:28080/v1/chat/completions
```

也可以用 OpenAI SDK：

```text
OPENAI_BASE_URL=http://127.0.0.1:28080/v1
OPENAI_API_KEY=sk-win-local-test
```

## 当前免费道配置

当前配置是 **WARP 开启 + 本机 Clash SOCKS5**：

```json
"warp": {
  "enabled": true,
  "pool_size": 32,
  "stack": "ipv6",
  "socks_udp": "socks5://127.0.0.1:7897"
},
"proxies": ["socks5://127.0.0.1:7897"],
"performance": {
  "max_anonymous_exits": 32,
  "free_tier_block_seconds": 120
}
```

- 注册 HTTPS 走 `proxies[0]`；WireGuard 外层 UDP 走 `socks_udp` 的 UDP ASSOCIATE。目标站看到的是 Cloudflare 分配的 WARP IPv6，不是 7897 节点的公网 IP。
- `pool_size` 上限 32，`preload` 上限 12；池子按需扩容。一个成功请求会消耗一个出口，补充速度约每 30–40 秒 1 个。
- 免费配额按 **(project, 出口 /64)** 计。`429` 时网关会先换会话身份，再退役出口；换出口但复用同一个 session 仍会 429。
- WARP 起不来时，启动先降级继续监听，后台按 15s→30s→1m→2m→5m 重试，成功后热切换。判断依赖：`api.cloudflareclient.com` 可注册 + 到 WARP endpoint 的 UDP 2408 可通；`1.1.1.1` 只用于隧道内验证，本机直连超时不影响 WARP。
- 详细机制、实测和排障见 [`doc/03-网络-WARP-与出口.md`](doc/03-网络-WARP-与出口.md)、[`doc/09-WARP-SOCKS5-UDP-基线.md`](doc/09-WARP-SOCKS5-UDP-基线.md) 和 `doc/12`。

## 修改配置

`config.json` 常用字段：

```json
{
  "listen": "0.0.0.0:28080",
  "server_keys": ["sk-win-local-test"],
  "anonymous": true,
  "warp": {
    "enabled": true,
    "pool_size": 32,
    "stack": "ipv6",
    "socks_udp": "socks5://127.0.0.1:7897",
    "source_ipv6": ""
  },
  "proxies": ["socks5://127.0.0.1:7897"],
  "models": { "free_only": true },
  "retry": { "max_attempts": 60, "timeout_seconds": 300 },
  "webui": {
    "enabled": true,
    "listen": "127.0.0.1:28081",
    "username": "admin",
    "password_hash": "..."
  }
}
```

- 想让局域网访问，把 `listen` 改成 `0.0.0.0:28080`，并在 Windows 防火墙放行端口；WebUI 不建议暴露到公网。
- 换 API Key：改 `server_keys` 数组，重启。
- 换 WebUI 密码：登录 WebUI 后在账号设置里改；也可以把 `password_hash` 换成 `password` 字段，首次启动会重新哈希。密码至少 10 个字符。
- 不要把 `config.json`、`config.json.warp-state.json` 和 `logs/` 提交到公开仓库。

## 重新构建

```powershell
$env:PATH = 'C:\Go\go\bin;' + $env:PATH
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-X main.version=v1.4.8-win.1" -o opencode2api.exe ./cmd/opencode2api
```

只需要 Go 1.24+；项目没有 cgo 依赖。构建完成后重启进程即可。发布前至少执行：

```powershell
go vet ./...
go test ./... -count=1
```

## 目录

```text
oc2-win/
  cmd/                 入口（opencode2api、warpchat、warpprobe）
  internal/            网关、协议、模型、WARP 等
  webui/               管理界面（编译时嵌入）
  assets/              静态资源
  config.json          当前运行配置
  config.example.json  原始模板
  opencode2api.exe     已构建的 Windows 二进制
  start-opencode2api.ps1 / .bat
  start-supervised.ps1
  start-background.ps1
  stop-opencode2api.ps1
  doc/                 完整文档（含 12-版本基线-v1.4.8-win.1.md）
  docs/                管理 API 与 Linux WARP 农场
  logs/                运行日志和 PID
```

## 注意

- 这是独立的 Git 仓库，不是主仓库的工作树；不要在主仓库根目录和 `oc2-win/` 里同时修改同一份源码，上游同步统一走 `git fetch upstream main && git merge upstream/main`。
- 同一份 `config.json` 不要同时启动两个进程，缓存、轮换状态和端口会冲突。
- 免费模型额度按 `(project, 出口 /64)` 计算；`429` 不一定是配置错误。先看 `/healthz` 的 `warp.active`、`cooling`，再决定等冷却还是换出口。
- 这个仓库的工作区可能包含调试脚本和未跟踪源码；提交前按 `doc/12` 的变更文件清单筛选，不要 `git add -A`。
