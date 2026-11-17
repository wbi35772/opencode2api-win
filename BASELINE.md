# 运行基线（BASELINE）

> 本文件是 **2026-11-17** 这台 Windows 机器上 `oc2-win` 的实际运行基线，用于后续升级、回归和换机对照。
>
> 上一份基线是 2026-11-03（已保留在 git 历史中）。本次对应版本 **v1.4.8-win.1**，完整发布说明见 [`doc/12-版本基线-v1.4.8-win.1.md`](doc/12-版本基线-v1.4.8-win.1.md)。
>
> 密钥、真实 IPv6 前缀和 WebUI 密码不写入本文件；运行数据来自真实进程与真实请求。

---

## 1. 环境

| 项目 | 值 |
|---|---|
| 操作系统 | Windows 11 x64 |
| Go（构建） | `go1.24.0 windows/amd64` |
| 构建模式 | `CGO_ENABLED=0`，`-trimpath` |
| 二进制 | `opencode2api.exe`，19,871,232 字节，静态链接 |
| 版本 | `v1.4.8-win.1` |
| 上游基线 | v1.4.8 / `159d7fdf` |
| 进程 | PID 25236，启动于 2026-11-16 23:09:32（本地） |
| API 监听 | `0.0.0.0:28080` |
| WebUI 监听 | `127.0.0.1:28081` |
| 配置文件 | `D:\Project\Openchamber\Token\opencode\oc2-win\config.json` |
| 日志 | `logs\run-v1.4.8-win.1-*.log` |
| 本机代理 | `socks5://127.0.0.1:7897`（Clash Verge / mihomo） |

> 当前 `listen` 是 `0.0.0.0`，局域网可访问；如不需要，改回 `127.0.0.1` 并重启。

---

## 2. 二进制

```text
SHA-256  BAB8F334330E226CD1CF31A4AEA7FCFCDBB4F3EFEDC533EE4F1946E75F9BC90B
大小     19,871,232 bytes
```

构建命令：

```powershell
$env:PATH = 'C:\Go\go\bin;' + $env:PATH
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags "-X main.version=v1.4.8-win.1" `
  -o opencode2api-v1.4.8-win.1.exe ./cmd/opencode2api
```

校验：

```powershell
(Get-FileHash .\opencode2api.exe -Algorithm SHA256).Hash
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
```

| 文件 | 说明 |
|---|---|
| `opencode2api.exe` | 当前运行，哈希同上 |
| `opencode2api-v1.4.8-win.1.exe` | 本次构建产物，与运行文件同哈希 |
| `opencode2api.exe.bak-20261116-230522-pre-v1.4.8-win.1` | 替换前旧二进制 |
| `config.json.bak-20261116-222128` | 调参前配置 |
| `config.json.warp-state.json.bak-20261116-223051` | 重置前 WARP 状态 |

---

## 3. 运行状态（实测）

```json
{"status":"ok","ready":true,"version":"v1.4.8-win.1",
 "models":{"status":"ready","total":86,"exposed":12,"zen":86,"go":0},
 "keys":{"zen":0,"go":0,"total":0,"anonymous":true},
 "proxies":{"total":1,"healthy":1,"unhealthy":0},
 "warp":{"status":"ready","stack":"ipv6","size":32,"preload":12,
         "active":1,"standby":1,"cooling":4}}
```

判读：

- `ready:true` 表示 API 可以接请求；
- `warp.active > 0` 表示当前有可用出口；
- `cooling` 是历史 `/64` 冷却记录，不代表当前出口全不可用；
- `keys.zen` / `keys.go` 为 0，表示当前只跑匿名免费车道；
- `models.exposed=12` 是 `free_only=true` 过滤后的可路由模型数。

---

## 4. 运行配置（脱敏）

```json
{
  "listen": "0.0.0.0:28080",
  "anonymous": true,
  "zen_keys": [],
  "go_keys": [],
  "models": { "refresh_seconds": 300, "free_only": true },
  "warp": {
    "enabled": true,
    "pool_size": 32,
    "stack": "ipv6",
    "socks_udp": "socks5://127.0.0.1:7897"
  },
  "proxies": ["socks5://127.0.0.1:7897"],
  "retry": { "max_attempts": 60, "timeout_seconds": 300 },
  "performance": {
    "max_idle_conns": 256,
    "max_idle_conns_per_host": 32,
    "max_conns_per_host": 0,
    "idle_conn_timeout_seconds": 120,
    "connect_timeout_seconds": 10,
    "failure_cooldown_seconds": 15,
    "attempt_timeout_seconds": 120,
    "first_event_timeout_seconds": 0,
    "max_anonymous_exits": 32,
    "free_tier_block_seconds": 120
  },
  "rotation": {
    "sota": {
      "order": ["deepseek-v4.1-flash", "glm-5.3", "grok-4.6", "kimi-k3", "qwen3.8-max"]
    },
    "first_output_seconds": 30
  },
  "reasoning": { "effort": "max" }
}
```

### 4.1 v1.4.8 新增配置

| 字段 | 当前值 | 说明 |
|---|---|---|
| `proxy_bindings` | 未设置 | 把出口固定到代理；失败不迁移 |
| `upstream.prompt_environment` | `{}` | 覆盖工作目录/平台/git 标记 |
| `models.active_probes` | `false` | 关闭主动探测，避免额外消耗免费额度 |
| `go_subscription` | 未设置 | Go Device OAuth 占位；没有 `go_keys` 时不用 |
| `performance.max_anonymous_exits` | `32` | 一次请求最多抽多少个出口 |
| `performance.free_tier_block_seconds` | `120` | 免费层标记后的出口冷却 |

---

## 5. 免费道验证

### 5.1 部署后实测

| 场景 | 结果 |
|---|---|
| 极简请求（无 tools） | `200` / 29.1s / `PONG` |
| 带 tools 非流式（部署前同代码） | `200` / 35.1s / `PONG` |
| 带 tools 流式 SSE（部署前同代码） | `200` / 8.0s / `role → PONG → stop` |
| 池子里只有 1 个出口时 | `429` / 96s（对照） |
| 池子 4+ 个新出口时 | `200` / 8.0s |

### 5.2 池回血曲线

| t | active | cooling |
|---:|---:|---:|
| 0s | 0 | 4 |
| 40s | 0 | 3 |
| 80s | 1 | 2 |
| 120s | 2 | 2 |
| 160s | 4 | 2 |
| 200s | 4 | 2 |

### 5.3 三层原因

1. 旧 exe 仍发 `clint_` 工具名 → 上游免费层直接 429；
2. `warp-state.json` 继承 13–14 个已耗尽出口 → 池子启动即冷却；
3. `pool_size` / `max_anonymous_exits` / `free_tier_block_seconds` 偏小 → 出口补充慢。

处理与证据见 [`doc/12`](doc/12-版本基线-v1.4.8-win.1.md) 第 3 节。

---

## 6. 单元测试与构建验证

```text
go build ./...                 PASS
go vet ./...                   exit 0
go test ./... -count=1         PASS (gateway / protocol / warp)
GOOS=linux   go build          PASS
GOOS=darwin  go build          PASS
GOOS=windows go build          PASS
```

---

## 7. 已知限制

| 限制 | 说明 |
|---|---|
| WARP 依赖本机 Clash 7897 | 7897 的 TCP 或 UDP 不通时，WARP 会 `handshake=never rx=0`；后台自动重试，恢复后无需重启 |
| 免费额度按 project × 出口 | 连续请求会快速消耗出口；等池子回血或重置 `warp-state.json` |
| `free_only=true` | `sota` 组返回 `503 rotation_unavailable`；付费模型返回 `400 not free` |
| Go OAuth 未完整实现 | `go_subscription` 只是配置占位 |
| 当前发布未提交 | HEAD `3d85208` + 工作区 v1.4.8 移植改动；下个版本前先提交/打 tag |
| 未移植 `0d05fe93` | v1.4.8 后的未发版提交，含匿名默认关闭与批量代理管理 |

---

## 8. 下次从这里开始

1. 打开网关：`.\start-background.ps1`。
2. 看健康：`curl.exe --noproxy "*" http://127.0.0.1:28080/healthz`。
3. 若 `warp.active=0` 且 `cooling` 持续增长：
   - 先确认 Clash 7897 可用；
   - 再按 `doc/12` 7.3 重置 `warp-state.json`；
   - 重启后等 2–4 分钟让池子回血。
4. 更新版本时按 `doc/12` 第 9 节清单执行，并同步更新本文件与 Canvas。
