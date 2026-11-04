# opencode2api · Windows / WARP 基线版

> 一个用 Go 写的 **OpenCode Zen / Zen Go 协议网关**：对外暴露 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 三套协议，对内管理上游密钥、代理和 Cloudflare WARP 出口池，并自带管理 WebUI。
>
> 本仓库是 **Windows 二次开发基线**：在上游 `v1.4.6` 之上，加入用户态 WARP 出口池、会话粘性、429 分级处理、物理网卡绑定、启动降级与后台恢复、Windows 进程树约束。代码可直接 `CGO_ENABLED=0` 静态编译，无 Node.js、无数据库、无 TUN 驱动依赖。

- 快速运行：[`§2 30 秒跑起来`](#2-30-秒跑起来)
- 当前实测状态：[`BASELINE.md`](BASELINE.md)
- 中文部署文档：[`doc/README.md`](doc/README.md)
- 管理台说明：[`docs/management.zh-CN.md`](docs/management.zh-CN.md)

---

## 目录

1. [这是什么](#1-这是什么)
2. [30 秒跑起来](#2-30-秒跑起来)
3. [基线状态](#3-基线状态)
4. [总体架构](#4-总体架构)
5. [协议层](#5-协议层)
6. [模型目录与路由](#6-模型目录与路由)
7. [出口层 A：代理池](#7-出口层-a代理池)
8. [出口层 B：WARP 池](#8-出口层-bwarp-池)
9. [会话粘性与 429](#9-会话粘性与-429)
10. [配置参考](#10-配置参考)
11. [HTTP API](#11-http-api)
12. [构建](#12-构建)
13. [运行与进程管理](#13-运行与进程管理)
14. [可观测性](#14-可观测性)
15. [故障排查](#15-故障排查)
16. [安全与发布](#16-安全与发布)
17. [已知限制](#17-已知限制)
18. [给其他模型的复用指南](#18-给其他模型的复用指南)
19. [目录结构](#19-目录结构)
20. [相对上游的改动](#20-相对上游的改动)
21. [附录](#21-附录)

---

## 1. 这是什么

### 1.1 解决的问题

OpenCode 官方提供 Zen（按量）和 Zen Go（订阅）两条付费通道，以及一条不需要密钥的免费通道。免费通道有两个现实约束：

1. **额度按「会话 × 出口 IP」计量**。同一个出口 + 同一个会话很快会被 429；换会话或换出口可以继续。
2. **单机单 IP 很快耗尽**。需要一组可轮换的出口。

本项目把这三条通道统一成一个本地 API：

- 客户端只需要一个 `base_url` + 一个本地 key；
- 网关负责协议转换、密钥轮换、代理调度、WARP 出口池、重试和降级；
- 免费通道通过用户态 WireGuard 获得多个独立 IPv6 出口，不需要 TUN、管理员权限或系统路由改动。

### 1.2 三条车道

| 车道 | 上游 | 凭据 | 出口 | 典型用途 |
|---|---|---|---|---|
| **Zen** | `https://opencode.ai/zen` | `zen_keys` | 代理池（Key 绑定） | 按量付费模型 |
| **Go** | `https://opencode.ai/zen/go` | `go_keys` | 代理池（Key 绑定） | 订阅套餐模型、`sota`/`sweet` 轮换 |
| **Anonymous** | `https://opencode.ai/inference` | 无（OpenCode `public` 凭据） | **WARP 池优先**，失败回落代理池 | 免费模型 |

`prefer` 决定同时能服务某个模型时先试哪条付费车道；免费模型在付费车道失败后仍可回落到匿名车道。

### 1.3 关键能力

- 三套协议互转：Chat Completions、Responses、Anthropic Messages；JSON 与 SSE 都支持。
- 文本、图片、reasoning、function tools、tool calls、tool results；目标协议能表达时也支持文件内容。
- Zen / Go 独立密钥池，轮询 + 冷却 + 重绑。
- 免费通道可选 WARP 用户态出口池，每个出口独立 `/64`。
- 直连、HTTP、HTTPS、SOCKS5、SOCKS5H、代理文件、轮换代理池（`#pool`）。
- 模型目录动态发现、能力/价格/可用性缓存、磁盘缓存。
- 管理 WebUI：配置编辑、Playground、诊断、Token 统计、实时日志、密钥管理、配额面板。
- 配置热应用：校验通过后新请求切到新网关，旧请求自然结束。
- `sota` / `sweet` 模型组：固定顺序、失败切换、首输出保护、状态持久化。

---

## 2. 30 秒跑起来

### 2.1 前置

- Windows 10/11 x64
- Go 1.24+（仅构建需要；运行只要 exe）
- 能访问 `opencode.ai`；要用 WARP 还需能访问 `api.cloudflareclient.com` 和 UDP 2408

### 2.2 构建

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o opencode2api.exe ./cmd/opencode2api
```

> 本项目没有 cgo 依赖。`-trimpath` 让不同机器构建出的路径信息一致，便于比对哈希。

### 2.3 最小配置

复制 `config.example.json` 为 `config.json`，至少改三处：

```json
{
  "listen": "127.0.0.1:28080",
  "server_keys": ["sk-local-test"],
  "anonymous": true,
  "warp": { "enabled": true, "pool_size": 8, "stack": "ipv6" },
  "proxies": ["direct"]
}
```

WebUI 首次启动必须提供明文 `password`（≥10 字符），启动后会转成 Argon2id 哈希：

```json
"webui": {
  "enabled": true,
  "listen": "127.0.0.1:28081",
  "username": "admin",
  "password": "change-me-local"
}
```

### 2.4 启动

```powershell
.\start-background.ps1
```

或前台（关窗口即整体退出）：

```powershell
.\start-opencode2api.ps1
```

### 2.5 验证

```powershell
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
```

看到 `"ready": true` 即可发请求：

```powershell
$body = '{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"Reply with OK only."}],"max_tokens":16,"stream":false}'
[IO.File]::WriteAllText("$PWD\request.json", $body, (New-Object System.Text.UTF8Encoding($false)))

curl.exe --noproxy "*" `
  -H "Authorization: Bearer sk-local-test" `
  -H "Content-Type: application/json" `
  --data-binary "@$PWD\request.json" `
  http://127.0.0.1:28080/v1/chat/completions
```

OpenAI SDK 直接指向：

```text
OPENAI_BASE_URL=http://127.0.0.1:28080/v1
OPENAI_API_KEY=sk-local-test
```

---

## 3. 基线状态

> 快照时间：2026-11-03（运行进程 PID 21032）。完整证据见 [`BASELINE.md`](BASELINE.md)。

| 项目 | 值 |
|---|---|
| 二进制 SHA-256 | `3C9DC38526B5CB4EA1A4622455006C70FCA5BB2E1B56D326D366D6B7E466E81A` |
| 构建 | `CGO_ENABLED=0`、`-trimpath`、Go 1.24.0 windows/amd64 |
| 体积 | 约 19.6 MB，静态链接，无外部 DLL |
| 单测 | `go test ./...` 全绿；`go vet ./...` 通过 |
| 健康检查 | `status=ok`、`ready=true`、`models.stale=false` |
| WARP | `status=ready`、`stack=ipv6`、`size=12`、`preload=8` |
| 429 压测 | 连续 10 次 + 并发 6 次 + 6×15k token 长上下文，全部 200、`attempts=1`、0 次会话轮换、0 次出口退役 |

---

## 4. 总体架构

### 4.1 进程与端口

单个 `opencode2api.exe` 进程同时提供：

| 端口 | 用途 | 默认监听 |
|---|---|---|
| `28080` | 推理 API（`/v1/*`）与 `/healthz` | `127.0.0.1` |
| `28081` | 管理 API（`/api/*`）+ 内嵌 WebUI | `127.0.0.1` |

WARP 在进程内用 gVisor netstack 终止 TCP，**不创建网卡、不改路由、不改 DNS、不改防火墙**。进程退出后网络参数与启动前一致。

### 4.2 代码地图

| 包 | 职责 |
|---|---|
| `cmd/opencode2api` | `main`、CLI flag、信号处理、启动/关闭顺序 |
| `internal/admin` | 管理 API、登录/会话/CSRF、配置读写、诊断、日志流、静态 WebUI |
| `internal/buildinfo` | 版本号注入点 |
| `internal/config` | `config.json` 加载/校验/原子保存、代理条目解析、轮换配置 |
| `internal/gateway` | 核心：路由、三条车道、重试、代理池、WARP 接入、健康检查 |
| `internal/httpx` | 共享 HTTP 客户端/Transport、响应写入、uTLS 与 Bun 伪装传输 |
| `internal/identity` | 从请求推导稳定会话身份 |
| `internal/jsonutil` | 已解码 JSON 的取值工具 |
| `internal/models` | 模型目录发现、能力/价格/可用性、缓存 |
| `internal/protocol` | 三套 wire 协议的请求/响应/SSE 转换 |
| `internal/rotation` | `sota`/`sweet` 轮换状态（无网络调用） |
| `internal/telemetry` | 请求结果、指标、结构化日志环形缓冲 |
| `internal/warp` | 用户态 WireGuard、netstack、注册器、出口池、惩罚/冷却 |

### 4.3 一次请求的完整生命周期

```text
client
  │  POST /v1/chat/completions   Authorization: Bearer <server_key>
  ▼
authenticate ── 本地 key 校验（server_keys / API key 管理）
  │
  ▼
decode + normalize ── 解析模型、消息、工具、reasoning、stream
  │
  ▼
route ── 模型目录决定 tier 与上游协议
  │
  ├─ Zen/Go 有 key ──► keyed lane
  │                     ├─ nodeCursor 选 key + 绑定代理
  │                     ├─ 发送、重试、冷却、重绑
  │                     └─ sota/sweet 轮换（Go）
  │
  └─ 免费模型 ────────► anonymous lane
                        ├─ WARP 池 AcquireFor(会话)
                        │    ├─ 200 → 返回
                        │    ├─ 第一次 429 → 换会话、同出口重试
                        │    ├─ 第二次 429 → 退役出口、换出口重试
                        │    └─ 传输错误 → 标记出口失败
                        └─ WARP 不可用 → warp_proxy_fallback → 代理匿名池
  │
  ▼
translate ── 上游协议 → 客户端协议（JSON 或 SSE）
  │
  ▼
telemetry ── 记录 attempts、延迟、token、事件
```

### 4.4 三条车道的选择逻辑

```text
route.ProtocolFor(tier)
        │
        ├── tier == Zen  && zenKeys > 0        → keyed lane (Zen)
        ├── tier == Go   && goKeys  > 0        → keyed lane (Go)
        └── 免费模型 / 付费车道失败             → anonymous lane
                                                   │
                                                   ├── warp enabled && 有出口 → WARP
                                                   └── 否则 / WARP 不可用     → 代理匿名池
```

`models.free_only: true` 会把目录和路由限制在免费模型；免费模型仍可回落到付费 key。

---

## 5. 协议层

### 5.1 客户端入口

| 方法 | 路径 | 协议 |
|---|---|---|
| `GET` | `/v1/models` | OpenAI 模型列表 |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions |
| `POST` | `/v1/responses` | OpenAI Responses |
| `POST` | `/v1/messages` | Anthropic Messages |
| `POST` | `/v1/systemone` | 内部/特殊入口（保留） |
| `GET` | `/healthz` | 就绪与状态 |

所有 `/v1/*` 都要求 `Authorization: Bearer <server_key>`（或管理台创建的本地 API key）。

### 5.2 上游协议选择

上游基址决定请求形状（`models.IsV1Upstream`）：

- 以 `/zen` 结尾 → **v1 车道**，使用 OpenCode 1.x 客户端请求形状；
- 其他 → **v2 车道**，使用 OpenCode 2.x CLI 形状。

默认值：

```json
"upstream": {
  "zen": "https://opencode.ai/zen",
  "go": "https://opencode.ai/zen/go",
  "anonymous": "https://opencode.ai/inference"
}
```

`models.protocols` 可以按模型 ID 覆盖目录报告的上游协议。

### 5.3 转换与工具

`internal/protocol` 负责：

- 请求体字段映射（`messages`、`input`、`system`、`max_tokens`、`temperature` 等）；
- 工具定义与 tool call / tool result 的双向转换；
- 图片与文件内容的可表达性判断；
- 流式事件序列重写（Chat `delta` ↔ Responses `response.*` ↔ Anthropic `content_block_delta`）；
- 工具名重命名（上游对名称长度/字符集有限制时）；
- 错误体归一化。

### 5.4 SSE 与首事件

- 客户端要流式时，网关保持 SSE 直通，逐事件转换，不整体缓冲。
- `performance.first_event_timeout_seconds` 控制「首个有效事件」的等待上限；0 表示关闭。
- `rotation.first_output_seconds` 控制轮换组内单次尝试的首输出上限，默认 30 秒。
- 长流式响应不受单次 attempt 超时影响，仍受请求级 `retry.timeout_seconds` 约束。

### 5.5 Reasoning

`reasoning` 配置可以强制思考等级：

```json
"reasoning": {
  "effort": "medium",
  "effort_by_model": { "glm-5.3": "high" }
}
```

- 客户端显式传入的等级（Anthropic `output_config.effort` / 顶层 `effort`、Chat `reasoning_effort`、Responses `reasoning.effort`）优先。
- `"none"` 剥离 reasoning；`"auto"` 仅对 Zen 免费模型使用可用性探测得到的最高可接受等级。
- 接受等级：`minimal`、`low`、`medium`、`high`、`xhigh`、`max`。

---

## 6. 模型目录与路由

### 6.1 发现

- 启动后立即拉取，之后每 `models.refresh_seconds`（默认 300 秒）刷新。
- 目录包含模型 ID、tier、协议、能力、价格、可用性。
- 刷新失败保留上次结果；`/healthz` 会标记 `stale`。

### 6.2 磁盘缓存

运行目录下会出现：

| 文件 | 内容 |
|---|---|
| `config.json.models.catalog.json` | 模型目录缓存 |
| `config.json.models.dev.json` | 开发/调试用目录快照 |
| `config.json.<hash>.availability.json` | 模型可用性探测结果 |
| `config.json.rotation.json` | `sota`/`sweet` 轮换状态 |

这些文件都可删除，重启后会重建。**不要提交**。

### 6.3 `free_only` 与 `prefer`

```json
"models": { "free_only": true },
"prefer": "go"
```

- `free_only: true`：只暴露和路由免费模型。
- `prefer`：`"zen"` 或 `"go"`，两条付费车道都能服务时先试哪条。

### 6.4 `sota` / `sweet` 轮换

Go 车道支持两个粘性模型组：

```json
"rotation": {
  "sota": { "alias": "sota", "order": ["deepseek-v4.1-flash", "glm-5.3", "grok-4.6", "kimi-k3", "qwen3.8-max"] },
  "sweet": { "alias": "sweet", "order": [] },
  "first_output_seconds": 30
}
```

- 客户端请求别名（如 `sota`）即可使用整组。
- 组内按 `order` 失败切换；某次尝试在 `first_output_seconds` 内没有输出就换下一个。
- 当前选择持久化到 `config.json.rotation.json`，服务重启后保持。
- `sota` 组必须恰好包含内置的 5 个 SOTA 模型；`sweet` 只能包含其他模型。

---

## 7. 出口层 A：代理池

### 7.1 条目语法

`proxies` 是字符串数组，`proxyfile` 是每行一条的文件。支持：

| 写法 | 含义 |
|---|---|
| `direct` | 直连（不读环境变量代理） |
| `http://host:port` | HTTP CONNECT 代理 |
| `https://host:port` | HTTPS 代理 |
| `socks5://host:port` | SOCKS5（本地解析 DNS） |
| `socks5h://host:port` | SOCKS5（代理解析 DNS） |
| `pool+URL` 或 `URL#pool` | 轮换代理池（如 Resin），每次重试新建隧道 |
| 空字符串 | 等价于 `direct` |

`proxyfile` 规则：

- 相对路径按 `config.json` 所在目录解析；
- 忽略 UTF-8 BOM；
- `#`、`;`、`//` 开头的行视为注释；
- 单行上限 1 MiB；
- 与 `proxies` 合并后按「clean URL + pool 标记」去重；
- 最终列表为空 → `["direct"]`。

> `direct` 是池子里的一个普通成员，不是「关闭代理」的开关。`["direct","socks5://..."]` 表示两个出口轮换。

### 7.2 Transport 构建

每个条目生成一个 `proxyTransport`：

```go
type proxyTransport struct {
    index        int
    name         string
    pool         bool           // #pool 轮换池
    client       *http.Client   // 普通请求
    bun          *http.Client   // 伪装成 Bun 运行时
    healthy      atomic.Bool
    checking     atomic.Bool    // 同一时刻只允许一个健康检查
    blockedUntil atomic.Int64   // 免费层 IP 封禁
}
```

细节：

- `direct` → `transport.Proxy = nil`，并且不读取 `HTTP_PROXY` / `HTTPS_PROXY`。
- `#pool` → 强制 HTTP/1.1：清空 `TLSNextProto`、ALPN 只留 `http/1.1`、关闭 keep-alive。否则 Go 会把所有请求复用到一条 CONNECT 隧道上，轮换代理永远只出一个 IP。
- 静态代理保留连接复用和 HTTP/2。
- `bun` 客户端用于免费模型流量，模仿 Bun 运行时的 TLS/请求特征。

### 7.3 健康状态

**定时复查**（每 15 分钟）：

- 目标 `https://cloudflare.com/cdn-cgi/trace`，超时 10 秒；
- 只检查**已经被标记不健康**的静态代理；
- `#pool` 永远跳过；
- 同一代理同时只允许一个检查；
- 拿到任何 HTTP 响应（包括 404/500）→ 恢复健康；
- 网络错误 → 保持不健康。

**请求结果反哺**（`syncProxyResult`）：

| 结果 | 处理 |
|---|---|
| 任意 HTTP 状态码 | 标记健康；`restoreProxy`；清空绑在它上面的匿名节点冷却 |
| 超时 / 拒绝连接 | 标记不健康；把绑定的 Zen/Go key 重绑到其他出口；后台复验 |
| 其他错误 | 只做后台复验，不改健康位 |

### 7.4 Key 车道调度（`upstreamNodes`）

- 每个上游 key 是一个 `upstreamNode`，轮流绑定到代理条目；
- key 数量少于代理时，多余代理保持空闲，可随时接管失败 key；
- `nodeCursor.Next()` 优先级：

```text
健康 + 未冷却
  → 健康但冷却最早到期
    → 可用（不健康但未冷却）
      → 冷却最早到期
```

- `MarkFailure` 触发条件：传输错误、401、403、429、5xx；
- 冷却时间 = `failure_cooldown_seconds × 2^(连续失败-1)`，上限 ×8；
- 上游返回更长的 `Retry-After` **全额尊重**（付费车道不吝啬）。

### 7.5 匿名车道调度（`anonymousNodes`）

每个代理一个节点，规则更保守：

- `blockedUntil`：被免费层判定 IP 滥用的出口直接不返回，连冷却回退都不给；
- `CursorFor(affinity)`：用会话 ID 做 FNV-64a 哈希决定起点，同一会话固定从同一出口开始；affinity 为空才轮询；
- `Next()` 两段式：
  1. 第一遍：健康、未冷却的静态节点 + **所有** `#pool` 节点；
  2. 如果池里有 `#pool` → 之后只循环 `#pool`（每次重试换新 IP）；没有 → 退回未使用节点，健康优先、冷却最早优先；
- 单请求最多走 `max_anonymous_exits` 个静态出口（默认 8）；
- `MarkFailure` 冷却同样指数增长，但 `Retry-After` 最多放大到 8 倍（`maxAnonymousRetryAfterMultiplier`），防止一个数小时的 Retry-After 压死出口；
- `#pool` 节点永不冷却。

### 7.6 熔断器

连续失败达到阈值后：

```text
trip() → 暂停 base << (strike-1)，上限 backoff.max
窗口结束 → 只放一个半开探针
  ├─ 成功 → 清零
  └─ 失败 → 立即重新熔断，窗口加倍
```

`models.anonymous_backoff` 可配置阈值与窗口：

```json
"anonymous_backoff": { "threshold": 6, "base_hours": 1, "max_hours": 24 }
```

---

## 8. 出口层 B：WARP 池

### 8.1 为什么需要

免费额度按 **(会话, 出口)** 计量。单机单 IP 下，换会话能撑一会儿，但出口本身也有上限。WARP 提供一组互相独立的公网 IPv6 出口；配合 `source_ipv6` 的 `/64` 轮换，每个出口拿到不同的公网地址。

### 8.2 实现方式

`internal/warp` 使用：

- `golang.zx2c4.com/wireguard`：用户态 WireGuard；
- `gvisor.dev/gvisor`：用户态 TCP/IP 栈（netstack）；
- `golang.zx2c4.com/wintun`：仅在需要 TUN 时使用（本基线默认不用）。

数据路径：

```text
Go http.Client
  → 自定义 DialContext
    → gVisor netstack TCP
      → WireGuard 加密
        → UDP socket（绑定物理网卡或系统路由）
          → Cloudflare WARP endpoint :2408
```

**不需要管理员权限、不创建网卡、不改系统路由。**

### 8.3 注册

- 注册走 HTTPS：`api.cloudflareclient.com`；
- 注册可以走代理：`registryProxyURL(cfg)` 取 `proxies` 中**第一个非 `direct`** 的条目；
- 数据面是 UDP，CONNECT 代理转发不了，所以 WireGuard 永远从本机出去；
- 注册失败自动回落直连；
- 因此当 `proxies` 里有一个不可用的代理时，每个新出口会先吃一次连接超时，再直连成功。这就是「补池约 30 秒/个」的常见原因。

### 8.4 池生命周期

| 参数 | 默认/规则 |
|---|---|
| `preload` | `min(size, maxPreload)`，启动先同步注册 1 个决定 `ready`，其余后台补 |
| `desiredLocked` | `inflight + waiting + 1`，下限 `preload`、最小 2、上限 `size` |
| 空池等待 | 25 秒 |
| 缩容延迟 | 60 秒 |
| 补池退避 | 连续失败 3 次后 1 分钟起，翻倍，上限 5 分钟 |
| 重复出口 | 同一 `/64` 拒绝注册（`errDuplicateEgress`） |
| 配额冷却 | 固定 24 小时，`maxEgressCooldown` 封顶 |
| 冷却持久化 | `config.json.warp-state.json` |
| 出口退役 | 从 `active` 移除 + 立刻起一个新的补上 |

`pool_size` 范围 1–32。实际出口数受本机网络、Cloudflare 注册限流和 `/64` 可用性影响。

### 8.5 物理网卡绑定与 `tun`

默认（`warp.tun: false`）：

- WireGuard 外层 UDP **钉在物理网卡**上（按默认路由找到的物理接口）；
- 如果配置了 `source_ipv6`，外层源地址也从该前缀里选；
- 这样即使系统里有一个已经挂掉的 TUN 抢占默认路由，WARP 握手也不会被送进死管道。

`warp.tun: true`：

- 外层 UDP 跟随系统默认路由；
- 只在「物理网卡到不了 Cloudflare、只有本机 TUN 能到」时才需要；
- 如果 TUN 本身挂了，表现是日志里的 `handshake=never rx=0`。

环境变量 `OPENCODE2API_WARP_NO_PIN=1` 可以关闭物理网卡绑定。

### 8.6 冷却与状态文件

```json
// config.json.warp-state.json（示意）
{
  "cooling": {
    "2001:db8:1:2:.../64": "2026-11-04T12:00:00Z"
  }
}
```

- 键是出口的 `/64`；
- 值是冷却到期时间；
- 删除该文件或其中的条目会立即允许重新注册这些网段；
- 文件损坏不会阻止启动，最多丢失历史冷却记录。

### 8.7 降级与恢复

启动时最多等 60 秒：

```text
拿到出口 → warp.status = ready
拿不到   → 记 warp_startup_fallback，进程继续监听 28080/28081
         → 后台按 15s / 30s / 1m / 2m / 5m 重试
         → 成功后热切换回 WARP，无需重启
```

`/healthz` 在 WARP 启用但没有活跃出口时返回 `503`、`ready=false`、`issues: ["warp_unavailable"]`。这表示免费车道不可用，不代表进程没启动；管理台和带 Key 的 API 仍然可用。

---

## 9. 会话粘性与 429

> 这是本基线相对上游最重要的行为修复。

### 9.1 旧行为

旧实现每个请求都轮换 WARP 出口：

```text
请求 1 → 出口 A + 会话 X → 429
请求 2 → 出口 B + 会话 X → 429
请求 3 → 出口 C + 会话 X → 429
...
```

每次 429 还会把当前出口冷却 24 小时。结果是：

- 一个会话打满整个出口池；
- 池子越用越小；
- 日志里几乎每个请求都是 `anon:429` + `session_identity_rotated`。

### 9.2 根因

免费额度按 **(会话, 出口)** 计量。旧代码只换出口、不换会话，且把 429 当成出口故障，导致：

1. 会话额度早已耗尽，换多少出口都没用；
2. 健康出口被误判为坏出口，24 小时冷却；
3. 池子被自己的惩罚逻辑烧光。

### 9.3 修复三件套

**① 会话粘出口**

`Pool.AcquireFor(ctx, affinity)`：

- 以 `baseIDs.Session` 为键；
- 同一会话优先复用绑定的出口；
- 出口被退役才重绑；
- 亲和表上限 4096，超出后淘汰最旧条目。

**② 第一次 429：换会话、不换出口**

`doWARPAttempt(..., defer429=true, affinity)`：

- 不向池子报告 429；
- 会话 ID 升一代（`sessionGuard.Rotate`）；
- 在**同一个出口**上重试；
- 重试成功 → 证明出口是好的，不退役。

**③ 第二次 429：才退役出口**

- 同一出口 + 新会话仍然 429 → 出口真的坏了；
- `penalizeHeldEgress` 把该 `/64` 冷却 24 小时；
- 换下一个出口，并重新钉住会话。

### 9.4 状态机

```text
                   ┌──────────────────────────────┐
                   │  AcquireFor(session)          │
                   │  命中亲和 → 复用出口           │
                   │  未命中   → 选新出口并绑定      │
                   └──────────────┬───────────────┘
                                  │
                                  ▼
                         发送请求
                                  │
              ┌───────────────────┼───────────────────┐
              │                   │                   │
              ▼                   ▼                   ▼
            2xx              第一次 429          传输错误
              │                   │                   │
              ▼                   ▼                   ▼
           返回          Rotate(session)        Fail(exit)
                         同出口重试             换出口重试
                              │
                    ┌─────────┴─────────┐
                    ▼                   ▼
                  2xx             第二次 429
                    │                   │
                    ▼                   ▼
                  返回        penalizeHeldEgress
                              退役 /64（24h）
                              换出口 + 重绑会话
```

### 9.5 关键日志事件

| 事件 | 含义 |
|---|---|
| `session_identity_rotated` | 第一次 429，会话升代，同出口重试 |
| `held_egress_retired` | 第二次 429，出口被退役并冷却 24h |
| `free_tier_429_body` | 记录上游 429 响应体（排障用） |
| `free_tier_ip_blocked` | 免费层判定出口 IP 滥用 |
| `anonymous_attempt_succeeded` | 匿名车道单次尝试成功 |
| `anonymous_attempt_response_failed` | 匿名车道收到可重试的上游响应 |
| `anonymous_attempt_transport_failed` | 匿名车道传输失败 |
| `warp_proxy_fallback` | WARP 不可用，回落代理匿名池 |

### 9.6 验证数据

2026-11-03，WARP 12 出口：

| 场景 | 结果 |
|---|---|
| 连续 10 次免费请求 | 10/10 `200`，`attempts=1` |
| 并发 6 次 | 6/6 `200` |
| 6 次 × 15k token 长上下文 | 6/6 `200` |
| 重启后 8 + 4 次 | 12/12 `200` |
| 3 会话 × 8 轮混合压测 | 24/24 `200` |
| 会话轮换 | 0 |
| 配额退役 | 0 |

唯一一次 `attempts=2` 是 `anon:transport_dial:10001ms`，出口传输错误冷却 1m30s，符合预期。

---

## 10. 配置参考

### 10.1 顶层字段

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `rotation` | object | 见 10.6 | `sota`/`sweet` 轮换组 |
| `listen` | string | `127.0.0.1:8080` | 推理 API 监听地址 |
| `server_keys` | []string | 必填 | 客户端使用的本地 key |
| `server_key_metadata` | map | 可选 | 每个本地 key 的名称/启用状态 |
| `zen_keys` | []string | `[]` | Zen 上游密钥 |
| `go_keys` | []string | `[]` | Go 上游密钥 |
| `anonymous` | bool | `false` | 启用无密钥免费车道 |
| `warp` | object | 见 10.3 | WARP 出口池 |
| `proxies` | []string | `[]` | 代理条目 |
| `proxyfile` | string | `""` | 代理文件路径 |
| `upstream` | object | 见 10.4 | 上游基址 |
| `retry` | object | 见 10.5 | 重试与请求超时 |
| `models` | object | 见 10.7 | 模型目录 |
| `performance` | object | 见 10.8 | 传输与失败处理 |
| `logging` | object | 见 10.9 | 日志 |
| `webui` | object | 见 10.10 | 管理台 |
| `prefer` | string | `"go"` | 付费车道优先：`zen` / `go` |
| `reasoning` | object | 可选 | 思考等级 |

### 10.2 校验规则（节选）

- `server_keys` 至少一个，且至少一个保持启用；
- 关闭 `anonymous` 时，`zen_keys` + `go_keys` 至少一个；
- `retry.max_attempts ≥ 1`、`retry.timeout_seconds ≥ 1`；
- `models.refresh_seconds ≥ 1`；
- `performance` 各连接数/超时为正（`max_conns_per_host` 可为 0 表示无限）；
- `logging.level` ∈ `debug|info|warn|error`；
- `logging.ring_size` ∈ `[100, 50000]`；
- `webui.password` 至少 10 字符；
- `webui.session_ttl_minutes` ∈ `[5, 10080]`；
- `warp.pool_size` ∈ `[1, 32]`；
- `warp.source_ipv6` 必须是 `/64` 或更短，且 `stack` 必须为 `ipv6`；
- 代理 scheme 只允许 `http`、`https`、`socks5`、`socks5h`；
- 未知字段会导致启动失败（`DisallowUnknownFields`）。

### 10.3 `warp`

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | 启用 WARP 出口池 |
| `pool_size` | int | `2` | 出口数量上限 |
| `stack` | string | `ipv6` | 隧道地址族 |
| `source_ipv6` | string | `""` | 外层源 IPv6 前缀（通常一个 `/64`），用于轮换出口 |
| `tun` | bool | `false` | 跟随系统路由（仅在需要 TUN 时开启） |

### 10.4 `upstream`

| 字段 | 默认 |
|---|---|
| `zen` | `https://opencode.ai/zen` |
| `go` | `https://opencode.ai/zen/go` |
| `anonymous` | `https://opencode.ai/inference` |

### 10.5 `retry`

| 字段 | 默认 | 说明 |
|---|---|---|
| `max_attempts` | `3` | 重试上限；匿名车道同时作为轮换代理的抽取下限 |
| `timeout_seconds` | `300` | 整个客户端请求（含重试）的超时 |

### 10.6 `rotation`

| 字段 | 默认 | 说明 |
|---|---|---|
| `sota.alias` | `"sota"` | SOTA 组别名 |
| `sota.order` | 内置 5 个模型 | 失败切换顺序 |
| `sweet.alias` | `"sweet"` | Sweet 组别名 |
| `sweet.order` | `[]` | 默认由目录动态补充 |
| `first_output_seconds` | `30` | 单次尝试首输出上限（1–300） |

### 10.7 `models`

| 字段 | 默认 | 说明 |
|---|---|---|
| `refresh_seconds` | `300` | 目录刷新间隔 |
| `protocols` | `{}` | 模型 ID → 上游协议覆盖 |
| `free_only` | `false` | 只暴露/路由免费模型 |
| `anonymous_backoff` | 可选 | 匿名车道熔断器 |

`anonymous_backoff`：

| 字段 | 默认 |
|---|---|
| `threshold` | `6` |
| `base_hours` | `1` |
| `max_hours` | `24` |

### 10.8 `performance`

| 字段 | 默认 | 说明 |
|---|---|---|
| `max_idle_conns` | `2048` | 全局空闲连接上限 |
| `max_idle_conns_per_host` | `256` | 每 host 空闲连接上限 |
| `max_conns_per_host` | `0` | 每 host 连接上限，0 = 无限 |
| `idle_conn_timeout_seconds` | `120` | 空闲连接超时 |
| `connect_timeout_seconds` | `5` | 拨号超时 |
| `failure_cooldown_seconds` | `15` | 失败冷却基数 |
| `attempt_timeout_seconds` | `0` | 单次尝试超时，0 = 只用请求级超时 |
| `first_event_timeout_seconds` | `0` | 首事件超时，0 = 关闭 |
| `max_anonymous_exits` | `8` | 单请求最多走几个静态出口，0 = 内置值 |
| `free_tier_block_seconds` | `900` | 免费层 IP 封禁冷却，0 = 内置值 |

### 10.9 `logging`

| 字段 | 默认 | 说明 |
|---|---|---|
| `level` | `"info"` | `debug|info|warn|error` |
| `ring_size` | `2000` | WebUI 内存日志条数 |
| `dump_request_bodies` | `false` | 记录发往上游的请求体（脱敏、截断） |

### 10.10 `webui`

| 字段 | 默认 | 说明 |
|---|---|---|
| `enabled` | `false` | 启用管理台 |
| `listen` | `0.0.0.0:8081` | 监听地址 |
| `username` | `""` | 登录用户名 |
| `password` | `""` | 仅首次设置用；保存时转为哈希并清空 |
| `password_hash` | `""` | Argon2id 哈希 |
| `session_ttl_minutes` | `720` | 登录会话时长（5–10080） |

> `enabled` 和 `listen` 的修改需要重启；其余配置保存后热应用。

### 10.11 完整示例

```json
{
  "listen": "127.0.0.1:28080",
  "server_keys": ["sk-local-test"],
  "zen_keys": [],
  "go_keys": [],
  "anonymous": true,
  "warp": {
    "enabled": true,
    "pool_size": 12,
    "stack": "ipv6",
    "source_ipv6": "2001:db8:1:2::/64",
    "tun": false
  },
  "proxies": ["direct"],
  "proxyfile": "",
  "upstream": {
    "zen": "https://opencode.ai/zen",
    "go": "https://opencode.ai/zen/go",
    "anonymous": "https://opencode.ai/inference"
  },
  "retry": { "max_attempts": 6, "timeout_seconds": 300 },
  "models": { "refresh_seconds": 300, "free_only": false },
  "performance": {
    "max_idle_conns": 2048,
    "max_idle_conns_per_host": 256,
    "max_conns_per_host": 0,
    "idle_conn_timeout_seconds": 120,
    "connect_timeout_seconds": 5,
    "failure_cooldown_seconds": 15,
    "attempt_timeout_seconds": 0,
    "first_event_timeout_seconds": 0,
    "max_anonymous_exits": 8,
    "free_tier_block_seconds": 900
  },
  "logging": { "level": "info", "ring_size": 2000 },
  "webui": {
    "enabled": true,
    "listen": "127.0.0.1:28081",
    "username": "admin",
    "password": "change-me-local",
    "session_ttl_minutes": 720
  },
  "prefer": "go",
  "reasoning": { "effort": "medium" }
}
```

---

## 11. HTTP API

### 11.1 客户端 API

鉴权：

```http
Authorization: Bearer <server_key>
```

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/v1/models` | 模型列表（OpenAI 形状） |
| `POST` | `/v1/chat/completions` | Chat Completions |
| `POST` | `/v1/responses` | Responses |
| `POST` | `/v1/messages` | Anthropic Messages |
| `POST` | `/v1/systemone` | 保留入口 |
| `GET` | `/healthz` | 无需鉴权 |

### 11.2 管理 API

管理台使用 Cookie 会话 + CSRF：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/auth/login` | 登录 |
| `GET` | `/api/auth/session` | 当前会话 |
| `POST` | `/api/auth/logout` | 登出（CSRF） |
| `GET` | `/api/config` | 读取配置（密钥脱敏） |
| `PUT` | `/api/config` | 保存并热应用（CSRF） |
| `POST` | `/api/config/reload` | 从磁盘重载（CSRF） |
| `POST` | `/api/config/reveal` | 查看真实密钥（CSRF） |
| `PUT` | `/api/account` | 修改账号/密码（CSRF） |
| `GET` | `/api/monitor` | 运行监控 |
| `GET` | `/api/debug/models` | 模型调试 |
| `GET` | `/api/models/availability` | 可用性 |
| `POST` | `/api/models/restore` | 恢复模型（CSRF） |
| `POST` | `/api/models/manual` | 手动添加模型（CSRF） |
| `POST` | `/api/models/probe` | 立即探测（CSRF） |
| `GET` | `/api/rotation` | 读取轮换 |
| `PUT` | `/api/rotation` | 保存轮换（CSRF） |
| `POST` | `/api/rotation/current` | 选择当前模型（CSRF） |
| `GET` | `/api/model-quotas` | 配额面板 |
| `GET` | `/api/api-keys` | 本地 key 列表 |
| `POST` | `/api/api-keys` | 创建本地 key（CSRF） |
| `PATCH` | `/api/api-keys/{id}` | 更新（CSRF） |
| `DELETE` | `/api/api-keys/{id}` | 删除（CSRF） |
| `POST` | `/api/debug/inference` | 调试推理（CSRF） |
| `GET` | `/api/logs` | 日志查询 |
| `GET` | `/api/logs/stream` | 日志 SSE |
| `GET` | `/` | 内嵌 WebUI |

### 11.3 鉴权与 CSRF

- 登录成功后下发 HttpOnly Cookie；
- 所有写操作要求 CSRF token（`X-CSRF-Token` 或表单字段）；
- 配置读取默认脱敏，只有 `reveal` 接口返回真实值；
- WebUI 密码使用 Argon2id；
- 登录会话默认 720 分钟。

---

## 12. 构建

### 12.1 命令

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o opencode2api.exe ./cmd/opencode2api
```

### 12.2 依赖

```text
github.com/andybalholm/brotli          Brotli 解压
github.com/klauspost/compress          zstd/gzip
github.com/refraction-networking/utls  自定义 TLS 指纹
golang.org/x/crypto                    Argon2id 等
golang.zx2c4.com/wireguard             用户态 WireGuard
golang.zx2c4.com/wintun                Windows TUN（可选）
gvisor.dev/gvisor                      用户态网络栈
```

### 12.3 测试

```powershell
go test ./...
go vet ./...
```

重点回归：

- `internal/gateway/sessions_test.go`：会话代数、轮换、ID 解析；
- `internal/warp/affinity_test.go`：出口亲和、退役后重绑、轮询；
- `internal/warp/penalty_test.go`：配额退役、未知 key 忽略。

### 12.4 版本注入

```powershell
go build -trimpath -ldflags "-X main.version=v1.0.0" -o opencode2api.exe ./cmd/opencode2api
```

未注入时 `/healthz` 的 `version` 为 `dev`。

---

## 13. 运行与进程管理

### 13.1 脚本

| 脚本 | 用途 |
|---|---|
| `start-background.ps1` | 后台启动，写 `logs/pid.txt`，日志重定向到 `logs/stdout.log` / `stderr.log` |
| `start-opencode2api.ps1` | 前台启动，调用 `start-supervised.ps1` |
| `start-opencode2api.bat` | 双击入口，同上 |
| `start-supervised.ps1` | 把网关和可选的 `tools/edgeproxy.exe` 放进同一 Job Object，关窗口即整体回收 |
| `stop-opencode2api.ps1` | 按 PID 和路径停止网关与边缘代理 |

`start-supervised.ps1` 使用 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`：关闭控制台、Ctrl+C、结束 PowerShell，都会让整棵进程树退出，不会留下监听端口。

> `tools/edgeproxy.exe` 是可选的本地边缘代理，未包含在本仓库；脚本检测不到时会跳过并打印提示。

### 13.2 启动注意

- `start-background.ps1` 会**截断** `logs/stdout.log`；
- 它的就绪轮询最多 15 秒，WARP 补池可能更久，脚本可能先报 `not ready`；
- 以 `/healthz` 为准，不要只看脚本退出码。

### 13.3 停止

```powershell
.\stop-opencode2api.ps1
```

### 13.4 端口冲突

同一份 `config.json` 不要启动两个进程：缓存、轮换状态、WARP 状态和端口都会冲突。

---

## 14. 可观测性

### 14.1 `/healthz`

```json
{
  "status": "ok",
  "ready": true,
  "version": "dev",
  "models": {
    "status": "ready",
    "total": 80,
    "exposed": 10,
    "zen": 80,
    "go": 0,
    "last_refresh": "2026-11-04T05:24:11Z",
    "stale_after_seconds": 600,
    "cache_source": "live",
    "stale": false
  },
  "keys": { "zen": 0, "go": 0, "total": 0, "anonymous": true },
  "proxies": { "total": 1, "healthy": 1, "unhealthy": 0 },
  "warp": {
    "status": "ready",
    "stack": "ipv6",
    "size": 12,
    "preload": 8,
    "active": 8,
    "standby": 7,
    "cooling": 3
  },
  "issues": []
}
```

状态语义：

| 字段 | 含义 |
|---|---|
| `status` | `ok` / `degraded` / `starting` |
| `ready` | 是否可以接请求（503 时为 false） |
| `models.status` | `ready` / `pending` / `empty` / `stale` |
| `proxies.healthy` | 健康静态代理数（`#pool` 不计） |
| `warp.active` | 当前活跃出口数 |
| `warp.standby` | 热备用出口数 |
| `warp.cooling` | 冷却记录数（历史 `/64`，不代表当前不可用） |
| `issues` | `model_catalog_pending`、`model_catalog_empty`、`model_catalog_stale`、`no_upstream_keys`、`warp_unavailable`、`no_healthy_proxies` |

### 14.2 关键日志事件

| 事件 | 含义 |
|---|---|
| `config_applied` | 配置热应用成功 |
| `catalog_refreshed` | 模型目录刷新成功 |
| `catalog_cache_load_failed` | 目录缓存读取失败 |
| `attempt_succeeded` | 单次上游尝试成功 |
| `attempt_transport_failed` | 单次尝试传输失败 |
| `attempt_retryable_response` | 收到可重试状态码 |
| `attempt_rejected` | 上游确定性拒绝 |
| `deterministic_rejection` | 不重试的客户端错误 |
| `fallback_tier_skipped` | 跳过回退车道 |
| `anonymous_fallback` | 匿名车道回退 |
| `anonymous_breaker_open` | 匿名熔断打开 |
| `anonymous_refresh_paused` | 匿名刷新暂停 |
| `free_tier_ip_blocked` | 免费层 IP 封禁 |
| `free_tier_429_body` | 429 响应体记录 |
| `session_identity_rotated` | 会话升代 |
| `held_egress_retired` | 出口退役 |
| `warp_proxy_fallback` | WARP 回落代理 |
| `warp_startup_fallback` | 启动时 WARP 降级 |
| `warp_retry_failed` / `warp_retry_succeeded` | 后台 WARP 重试 |
| `proxy_unavailable` / `proxy_restored` | 代理健康变化 |
| `health_check_passed` / `health_check_failed` | 代理健康检查 |
| `rotation_attempt` | 轮换组尝试 |
| `request_failed` / `stream_failed` | 请求/流失败 |

### 14.3 WebUI 页面

- **总览**：模型、代理、WARP、请求量、Token 统计；
- **配置**：所有字段可视化编辑，Proxy 标签支持多行代理、清空确认；
- **Playground**：直接发对话请求；
- **诊断**：模型探测、请求调试、日志流；
- **密钥**：本地 API key 创建/禁用/删除；
- **配额**：Go 订阅剩余额度与重置时间；
- **轮换**：`sota`/`sweet` 当前模型与顺序。

---

## 15. 故障排查

### 15.1 症状 → 原因 → 处理

| 症状 | 常见原因 | 处理 |
|---|---|---|
| `ready:false` + `warp_unavailable` | WARP 没出口 | 看 `logs/stderr.log` 的 `warp_startup_fallback`；确认能到 `api.cloudflareclient.com` 和 UDP 2408；把 `proxies` 清空为 `["direct"]` |
| `ready:false` + `no_healthy_proxies` | 所有静态代理不健康，且 WARP 关闭/无 Go key | 修代理或启用 WARP |
| `ready:false` + `model_catalog_pending` | 目录首次刷新未完成 | 等几秒；检查上游连通性 |
| 每个新出口补池 ~30 秒 | `proxies` 里有不可用代理，注册先超时再直连 | 清空 Proxy 列表或换成可用代理 |
| `handshake=never rx=0` | `warp.tun:true` 且 TUN 已死 | 改 `warp.tun:false` |
| 429 不断，日志全是 `session_identity_rotated` | 会话额度耗尽 | 正常；第二次 429 才退役出口 |
| 429 不断，日志全是 `held_egress_retired` | 出口真的被限 | 增大 `pool_size`，检查 `source_ipv6` 是否给了足够 `/64` |
| `attempts=2` 且 `anon:transport_dial:10001ms` | 出口传输错误 | 出口冷却 1m30s，等下一轮 |
| 免费模型全失败但 Key 正常 | WARP 和代理都不可用 | 开 WARP 或配置代理 |
| WebUI 打不开 | `webui.enabled=false` 或端口冲突 | 检查配置和监听 |
| 配置保存失败 | 校验不通过 | 看响应里的 `error` 字段 |

### 15.2 常用命令

```powershell
# 健康
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz

# 模型
curl.exe --noproxy "*" -H "Authorization: Bearer sk-local-test" http://127.0.0.1:28080/v1/models

# 日志尾部
Get-Content .\logs\stdout.log -Tail 50

# 当前 PID
Get-Content .\logs\pid.txt

# 端口占用
Get-NetTCPConnection -LocalPort 28080,28081 -State Listen
```

### 15.3 免费额度判断

- 429 不一定是配置错误；免费额度按 (会话, 出口) 计量；
- 先看日志里是 `session_identity_rotated` 还是 `held_egress_retired`；
- 前者是会话问题，后者才是出口问题；
- `cooling` 是历史记录，不代表当前所有出口都不可用。

---

## 16. 安全与发布

### 16.1 绝不能提交

| 文件 | 原因 |
|---|---|
| `config.json` | 含 `server_keys`、上游 key、WebUI 哈希、`source_ipv6` |
| `config.json.*` | WARP 状态、目录缓存、可用性、轮换状态 |
| `logs/` | 请求内容、错误体、会话 ID |
| `*.exe` | 构建产物 |
| `tools/`、`usque/` | 本地二进制与 WARP 私钥 |
| `config-backups/` | 历史配置 |

### 16.2 公开仓库前检查

```powershell
git status --short
git grep -n -I -E "sk-|password|BEGIN (RSA|OPENSSH|PRIVATE)" -- . ':!go.sum'
```

### 16.3 运行安全

- 默认只监听 `127.0.0.1`；
- 不要把 WebUI 暴露到公网；
- 局域网访问时改 `listen` 并在防火墙放行，同时使用强 `server_keys`；
- WebUI 密码至少 10 字符，建议更长；
- 管理 API 的 `reveal` 会返回真实密钥，注意浏览器/代理缓存。

---

## 17. 已知限制

1. **WARP 出口数受 Cloudflare 限制**：`pool_size` 只是上限，实际能注册多少取决于本机网络、注册限流和 `/64` 可用性。
2. **`source_ipv6` 必须是可路由前缀**：没有这个前缀时，所有出口共享一个公网地址，出口轮换失效。
3. **CONNECT 代理不能承载 WireGuard**：数据面永远从本机 UDP 出去；代理只影响注册。
4. **免费层行为可能变化**：额度、429 语义、封禁时长由上游决定，网关只能按观测到的行为处理。
5. **`cooling` 会累积**：历史 `/64` 记录不会自动清理，数量大时只影响状态文件大小，不影响调度。
6. **单进程单配置**：不支持同一份 `config.json` 多实例；需要多实例请复制目录和端口。
7. **WebUI 的 `enabled`/`listen` 需要重启**：其余字段热应用。
8. **没有内置 TLS**：对外暴露请放在反向代理后面。
9. **`/v1/systemone` 是保留入口**：不建议普通客户端使用。
10. **Windows 脚本依赖 PowerShell 5.1+**：Job Object 代码通过 `Add-Type` 编译，首次启动会慢一点。

---

## 18. 给其他模型的复用指南

### 18.1 可独立复用的模块

| 模块 | 复用方式 |
|---|---|
| `internal/warp` | 用户态 WireGuard + gVisor netstack + 出口池，可单独用于任何需要多 IP 出口的 Go 程序 |
| `internal/httpx` | uTLS/Bun 伪装 Transport、共享响应写入 |
| `internal/config` | 严格 JSON 配置加载、注释剥离、原子保存、密钥脱敏 |
| `internal/protocol` | 三套 LLM 协议互转，可抽出来做独立代理 |
| `internal/identity` | 从请求头/首条消息推导稳定会话 ID |
| `internal/rotation` | 通用粘性轮换组 |
| `internal/telemetry` | 环形日志 + 指标 |

### 18.2 关键接口契约

**出口池（WARP）**

```go
// 获取一个绑定到会话的出口
exit, err := pool.AcquireFor(ctx, affinity)

// 报告成功/失败
pool.ReportSuccess(exit)
pool.ReportFailure(exit)

// 退役指定 /64
pool.PenalizeEgress(prefixKey)
```

**代理池**

```go
// 选下一个 key/代理
cursor := nodes.CursorFor(affinity)
node := cursor.Next()

// 结果反哺
nodes.MarkSuccess(node)
nodes.MarkFailure(node, resp, err)
```

**会话守卫**

```go
guard := newSessionGuard(ids)
guard.Resolve(req)        // 当前会话 ID
guard.Rotate()            // 升代
guard.Generation()        // 当前代数
```

### 18.3 组合建议

- **只想要出口池**：复制 `internal/warp`，实现自己的 `DialContext`，忽略网关部分。
- **只想要协议转换**：复制 `internal/protocol`，它不依赖网关状态。
- **想要完整网关**：从 `internal/gateway.Gateway` 的构造函数和 `handleInference` 入手。
- **想要管理台**：`internal/admin` 与 `webui/` 是自包含的，静态资源编译时嵌入。
- **想要配置系统**：`internal/config` 的 `Load`/`Normalize`/`SaveAtomic` 可以直接复用。

### 18.4 扩展点

| 需求 | 改哪里 |
|---|---|
| 新增上游协议 | `internal/protocol` + `wire` 枚举 + `handleInference` 分支 |
| 新增出口类型 | 实现 `DialContext`，注册到 `transportPool` |
| 新增免费车道策略 | `internal/gateway/upstream.go` 的 `doAnonymousUpstream` |
| 新增健康指标 | `internal/gateway/health.go` 的 `healthResponse` |
| 新增管理 API | `internal/admin/server.go` 的 mux 注册 |
| 新增 WebUI 页面 | `webui/` + `internal/admin` 静态嵌入 |

### 18.5 数据结构速查

```go
// 代理条目
type proxyTransport struct {
    index   int
    name    string
    pool    bool
    client  *http.Client
    bun     *http.Client
    healthy atomic.Bool
}

// 上游 key
type upstreamNode struct {
    key            string
    index          int
    preferredProxy int
    proxyIndex     atomic.Int64
    failures       atomic.Uint32
    cooldownUntil  atomic.Int64
}

// WARP 出口
type exit struct {
    // 隧道、netstack、/64、注册信息
}
```

---

## 19. 目录结构

```text
.
├── cmd/
│   └── opencode2api/          main、flag、信号处理
├── internal/
│   ├── admin/                 管理 API + 内嵌 WebUI
│   ├── buildinfo/             版本注入
│   ├── config/                配置加载/校验/保存、代理解析
│   ├── gateway/               核心路由、车道、池、重试、健康
│   ├── httpx/                 HTTP 客户端/传输/响应
│   ├── identity/              会话身份
│   ├── jsonutil/              JSON 取值
│   ├── models/                模型目录
│   ├── protocol/              协议转换
│   ├── rotation/              轮换状态
│   ├── telemetry/             日志与指标
│   └── warp/                  用户态 WireGuard + netstack + 出口池
├── webui/                     管理台前端（编译时嵌入）
├── assets/                    静态资源
├── doc/                       中文部署文档
├── docs/                      管理台说明
├── config.example.json        配置模板
├── start-*.ps1 / *.bat        Windows 启停脚本
├── go.mod / go.sum
└── LICENSE
```

---

## 20. 相对上游的改动

基于上游 `v1.4.6`（`58f5667`），本基线包含：

### 20.1 出口与 WARP

- 用户态 WARP 出口池（`internal/warp`）；
- `source_ipv6` `/64` 轮换；
- 物理网卡绑定（`warp.tun:false` 默认）；
- 启动降级 + 后台重试 + 热恢复；
- 出口冷却持久化；
- 重复 `/64` 拒绝。

### 20.2 会话与 429

- `internal/gateway/sessions.go`：会话代数守卫；
- `Pool.AcquireFor` 出口亲和；
- 第一次 429 换会话、同出口重试；
- 第二次 429 才退役出口；
- `penalizeHeldEgress`；
- 回归测试三件套。

### 20.3 代理池

- `#pool` 强制 HTTP/1.1、禁用 keep-alive；
- 代理健康检查与失败反哺；
- Zen/Go key 重绑；
- 匿名车道熔断器；
- 免费层 IP 封禁冷却。

### 20.4 Windows 运行

- Job Object 进程树约束；
- 后台启动/停止脚本；
- 物理网卡选择；
- 启动就绪轮询。

### 20.5 管理台

- 代理 chip 折叠；
- 清空代理确认；
- 模型探测；
- 日志流；
- 配额面板。

---

## 21. 附录

### 21.1 术语

| 术语 | 含义 |
|---|---|
| **车道 (lane)** | 一条上游通道：Zen、Go、Anonymous |
| **出口 (exit)** | 一个公网 IP/网段，来自代理或 WARP |
| **亲和 (affinity)** | 会话与出口的绑定关系 |
| **退役 (retire)** | 把出口从活跃池移除并冷却 |
| **冷却 (cooling)** | 出口暂时不可用 |
| **熔断 (breaker)** | 整条车道暂停 |
| **`/64`** | IPv6 前缀，WARP 出口的去重与冷却单位 |
| **`#pool`** | 轮换代理池标记 |
| **`sota` / `sweet`** | Go 车道的两个粘性模型组 |

### 21.2 关键文件索引

| 文件 | 内容 |
|---|---|
| `internal/gateway/sessions.go` | 会话代数守卫 |
| `internal/gateway/warp.go` | WARP 尝试、429 分级、回落 |
| `internal/warp/pool.go` | 出口池、亲和、惩罚 |
| `internal/warp/registrar.go` | 注册 |
| `internal/gateway/pool.go` | 代理 transport、匿名节点、熔断 |
| `internal/gateway/refresh.go` | 代理健康检查 |
| `internal/gateway/health.go` | `/healthz` |
| `internal/gateway/upstream.go` | 匿名车道主循环 |
| `internal/config/config.go` | 配置结构与默认值 |
| `internal/config/proxy.go` | 代理条目解析 |
| `internal/admin/server.go` | 管理 API 路由 |
| `cmd/opencode2api/main.go` | 启动流程 |

### 21.3 参考

- 上游项目：`opencode2api`（Go gateway for OpenCode Zen / Zen Go）
- 中文部署文档：[`doc/README.md`](doc/README.md)
- 管理台说明：[`docs/management.zh-CN.md`](docs/management.zh-CN.md)
- 当前运行基线：[`BASELINE.md`](BASELINE.md)

---

## License

见 [`LICENSE`](LICENSE)。
