# 运行基线（BASELINE）

> 本文件是 **2026-11-21** 这台 Windows 机器上 `oc2-win` 的实际运行基线，用于后续升级、回归和换机对照。
>
> 上一份基线是 2026-11-17（v1.4.8-win.2，已保留在 git 历史中）。本次对应版本 **v1.5.1-win.1**，完整发布说明见 [`doc/12-版本基线-v1.5.1-win.1.md`](doc/12-版本基线-v1.5.1-win.1.md)。
>
> 密钥、真实 IPv6 前缀、WebUI 密码不写入本文件；运行数据来自真实进程与真实请求。

---

## 1. 环境

| 项目 | 值 |
|---|---|
| 操作系统 | Windows 11 x64 |
| Go（构建） | `go1.24.0 windows/amd64` |
| 构建模式 | `CGO_ENABLED=0`，`-trimpath -ldflags "-s -w"` |
| 二进制 | `opencode2api.exe`，14,328,832 字节，静态链接 |
| 版本 | `v1.5.1-win.1` |
| 上游基线 | v1.5.1 / `6575c62` |
| 合并提交 | `18b6a15`（merge v1.5.1），清理 `e092353` |
| 进程 | PID 24188，启动于 2026-11-21 02:37:47Z |
| API 监听 | `0.0.0.0:28080` |
| WebUI 监听 | `127.0.0.1:28081` |
| 配置文件 | `D:\Project\Openchamber\Token\opencode\oc2-win\config.json` |
| 日志 | `logs\win3-out.log`、`logs\win3-err.log` |
| 本机代理 | `socks5://127.0.0.1:10808`（Clash Verge / mihomo）；HTTP 10809 |

> 当前 `listen` 是 `0.0.0.0`，局域网可访问；如不需要，改回 `127.0.0.1` 并重启。

---

## 2. 二进制

```text
SHA-256  6D614EFC858E791EAFC8B4C3163CB8423730A19C1A2376818BC0AA1DC8F44812
大小     14,328,832 bytes
```

构建命令：

```powershell
$env:PATH = 'C:\Go\go\bin;' + $env:PATH
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags "-s -w -X main.version=v1.5.1-win.1" `
  -o opencode2api.exe ./cmd/opencode2api
```

校验：

```powershell
(Get-FileHash .\opencode2api.exe -Algorithm SHA256).Hash
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
```

| 文件 | 说明 |
|---|---|
| `opencode2api.exe` | 当前运行，哈希同上 |
| `release/v1.5.1-win.1/opencode2api.exe` | 本次构建产物，与运行文件同哈希 |
| `opencode2api.exe.bak-20261121-pre-v1.5.1-win.1` | 替换前二进制（win.2，13,925,376 字节，`CDFD9834…`） |
| `opencode2api.exe.bak-20261117-035256-pre-v1.4.8-win.2` | 更早的 win.1 二进制（19,871,232 字节） |
| `config.json.bak-20261116-222128` | win.1 调参前配置 |
| `config.json.warp-state.json.bak-20261116-223051` | win.1 重置前 WARP 状态 |

---

## 3. 运行状态（实测）

```json
{"status":"ok","ready":true,"version":"v1.5.1-win.1",
 "models":{"status":"ready","total":103,"exposed":11,"zen":103,"go":0},
 "keys":{"zen":0,"go":0,"total":0,"anonymous":true},
 "proxies":{"total":1,"healthy":1,"unhealthy":0},
 "warp":{"status":"ready","stack":"ipv6","size":32,"preload":12,
         "active":5,"standby":5,"cooling":62}}
```

判读：
- `ready:true` 表示 API 可以接请求；
- `warp.active > 0` 表示当前有可用出口；
- `cooling` 是历史 `/64` 冷却记录，不代表当前出口全不可用；
- `keys.zen` / `keys.go` 为 0，表示当前只跑匿名免费车道；
- `models.exposed=11` 是 `free_only=true` 过滤后的可路由模型数。
- catalog 缓存已升到 schema v4；启动时旧 v3 缓存被拒绝并重新拉取（live 73 个），属正常升级。

---

## 4. 运行配置（脱敏）

```json
{
  "listen": "0.0.0.0:28080",
  "anonymous": true,
  "zen_keys": [],
  "go_keys": [],
  "models": { "refresh_seconds": 300, "free_only": true, "active_probes": false },
  "warp": {
    "enabled": true,
    "pool_size": 32,
    "stack": "ipv6",
    "socks_udp": "socks5://127.0.0.1:10808"
  },
  "proxies": ["socks5://127.0.0.1:10808"],
  "retry": { "max_attempts": 60, "timeout_seconds": 300 },
  "performance": { "max_anonymous_exits": 32, "free_tier_block_seconds": 120 }
}
```

说明：
- 上游 v1.5.0 模板把 `anonymous` 默认值改为 `false`；本机显式保留 `true`，否则免费道不可用。
- `go_oauth`/`zen_oauth` 未配置；`go_subscription` 已从配置和代码中移除（旧值仍可迁移读取）。
- `warp.socks_udp` 与 `warp.source_ipv6` 互斥；本机用前者。
- 旧版 `warpool#pool` 前缀为历史值，当前 `proxies` 是单条 10808。

---

## 5. 验证矩阵（2026-11-21）

### 5.1 构建与测试

| 项 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | `exit 0` |
| 静态检查 | `go vet ./...` | `exit 0` |
| 单元测试 | `go test ./... -count=1` | `gateway 0.127s`、`protocol 0.037s`、`warp 12.216s`，全部 `ok` |
| 版本注入 | `/healthz` | `version=v1.5.1-win.1` |

### 5.2 免费道请求

| 场景 | 结果 | 说明 |
|---|---|---|
| `deepseek-v4.1-flash-free` 非流式 | `429` / 40.8s | 池内旧出口全部 429；session/project 已轮换 |
| `big-pickle` 非流式 | `429` / 26.9s | 新出口预检 `FreeUsageLimitError`，该 `/64` 被标记 spent |
| `hy4-flash-free` 非流式 | `200` / 16.84s / `PONG` | usage: prompt 5576 / completion 16 / reasoning 12 |
| `hy4-flash-free` 流式 + `tool_choice=required` | `200` / 49.99s | 首出口 `transport_headers` 43.0s 后换出口，4.8s 成功；`get_weather {"city":"Paris"}`、`finish_reason=tool_calls`、`[DONE]` |

结论：同一时刻 `hy4-flash-free` 能 200，说明免费道和二进制正常；`deepseek`/`big-pickle` 的 429 是出口状态，不是版本故障。

### 5.3 上游修复验证

| 修复 | 验证方式 | 结果 |
|---|---|---|
| #188 NBSP 工具描述 | 源码 `strings.ReplaceAll(description, " ", "\u00a0")`；流式工具调用带空格描述仍 200 | 通过 |
| #190 客户端环境隔离 | `client_environment.go` 与上游逐字节一致；请求路径不再 `os.Getwd()`/`runtime.GOOS` | 通过 |
| e2b2a7c 流式硬化 | 16 MiB 帧上限、首帧前 502、64 MiB 非流式上限、choiceIndex | 源码与测试通过 |
| 代理重定向拒绝 | `CheckRedirect` 返回 `ErrUseLastResponse` | 源码与测试通过 |
| OAuth 重构 | `internal/oauth` 编译、配置迁移、catalog v4 | 通过（未启用账号 OAuth） |
| 批量代理管理 | `POST /api/proxies/check`、WebUI 代理页 | 编译/静态通过；本机单代理未批量使用 |

---

## 6. WARP 状态

```text
继承 spent 块          56（config.json.warp-state.json）
池上限                32
预热                  12
就绪                  5
备份 standby          5
冷却                  62
外层 UDP               socks5://127.0.0.1:10808（UDP ASSOCIATE）
网卡钉扎              WLAN（ifIndex 22）
```

- 本仓库保留 fork 的 WARP 实现：24h 额度黑名单、注册预检、会话亲和、SOCKS5 UDP、Windows 网卡钉扎。
- 上游 v1.4.8..v1.5.1 没有修改 `internal/warp`，因此本版不存在“漏掉上游 WARP 修复”的问题。
- 当前 `cooling=62` 偏多但仍有 `active=5`；遇到 429 先等 2–4 分钟，不要立即重启进程。

---

## 7. 发布与画布

| 项 | 值 |
|---|---|
| 发布目录 | `release/v1.5.1-win.1/` |
| 发布包 | `release/opencode2api-v1.5.1-win.1.zip`，SHA-256 `41018C0E2801E5BFEE8F9B9B5A6C461D47C179F57C674B007A87252F91C120C1` |
| 画布（本版） | Canvas `06GZR0P1MCHNSNZT4RXFX97XJV-opencode2api-win-v1-5-1-win-1` |
| 画布（win.2） | Canvas `06GYK5J08WVHTJQ0RWYNZM0EMS-opencode2api-win-2026-11-17-v1-4-8-win-2` |
| GitHub Release | 待发布：`v1.5.1-win.1` |

---

## 8. 回滚

```powershell
Get-Process opencode2api -ErrorAction SilentlyContinue | Stop-Process -Force
Copy-Item .\opencode2api.exe.bak-20261121-pre-v1.5.1-win.1 .\opencode2api.exe -Force
.\start-background.ps1
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
```

回滚后 `version` 应为 `v1.4.8-win.2`，`models` 会重新加载旧 catalog 缓存（schema v3 在本版仍可写），WARP 状态文件不变。

---

## 9. 遗留

- `#167` Playground relay 固定仍未移植（只影响诊断页）。
- `internal/goauth` 已删除；OAuth 相关代码完整但未接入账号。
- Forgejo 镜像 `origin` 仍不可达（`172.30.196.173:3005` 在 WSL 网段，WSL 已停用）；GitHub 为权威发布源。
- 上游 v1.5.1 之后如有新提交，按 `doc/12` 第 9 节的普通 merge 流程处理。
