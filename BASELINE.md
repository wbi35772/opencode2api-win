# 运行基线（BASELINE）

> 本文件记录 **2026-11-03** 这台 Windows 机器上实际跑通的状态，作为后续对比、回归和组合其他项目的参照。
>
> 配置中的密钥、真实 IPv6 前缀和 WebUI 密码已替换为占位值；运行数据来自真实进程与真实请求。

---

## 1. 环境

| 项目 | 值 |
|---|---|
| 操作系统 | Windows 11 x64 |
| Go（构建） | `go1.24.0 windows/amd64` |
| 构建模式 | `CGO_ENABLED=0`，`-trimpath` |
| 二进制 | `opencode2api.exe`，约 19.6 MB，静态链接 |
| 进程 | PID 21032，启动于 2026-11-03 20:33:53 |
| API 监听 | `127.0.0.1:28080` |
| WebUI 监听 | `127.0.0.1:28081` |
| 物理网卡 | 以太网，`192.0.2.10` |
| 本机代理 | `socks5://127.0.0.1:10808`（上游已失效） |
| 系统 TUN | `xray_tun` 占用默认路由（已失效） |

---

## 2. 二进制

```text
SHA-256  3C9DC38526B5CB4EA1A4622455006C70FCA5BB2E1B56D326D366D6B7E466E81A
```

构建命令：

```powershell
$env:CGO_ENABLED = "0"
& 'C:\Go\go\bin\go.exe' build -trimpath -o opencode2api.exe ./cmd/opencode2api
```

校验：

```powershell
(Get-FileHash .\opencode2api.exe -Algorithm SHA256).Hash
```

> 运行中的进程与源码树一致：用同一棵树重新构建出的二进制哈希与运行文件相同。

---

## 3. 运行配置（脱敏）

```json
{
  "listen": "127.0.0.1:28080",
  "server_keys": ["sk-local-test"],
  "anonymous": true,
  "warp": {
    "enabled": true,
    "pool_size": 12,
    "stack": "ipv6",
    "source_ipv6": "2001:db8:1:2::/64",
    "tun": false
  },
  "proxies": ["socks5://127.0.0.1:10808"],
  "retry": { "max_attempts": 6, "timeout_seconds": 300 },
  "models": { "refresh_seconds": 300, "free_only": false },
  "webui": {
    "enabled": true,
    "listen": "127.0.0.1:28081",
    "username": "admin",
    "password_hash": "<argon2id>",
    "session_ttl_minutes": 720
  },
  "prefer": "go"
}
```

关键点：

- `warp.tun: false`：外层 UDP 钉在物理网卡，不跟随已失效的 `xray_tun`；
- `proxies` 里的 `10808` 已失效，因此每个新 WARP 出口注册先吃约 10 秒超时，再直连成功；
- `source_ipv6` 使用真实 `/64` 时每个出口获得不同公网 IPv6；此处已替换为文档前缀。

---

## 4. 健康检查快照

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
    "last_refresh": "2026-11-04T05:24:11.4429676Z",
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

说明：

- `active` 会随负载和缩容延迟波动；空闲时可能低于 `size`；
- `cooling` 是历史 `/64` 冷却记录数，不代表当前出口不可用；
- `keys.zen` / `keys.go` 为 0，表示当前只跑匿名免费车道。

---

## 5. 429 修复验证

### 5.1 修复前

| 场景 | 结果 |
|---|---|
| 连续 10 次免费请求 | 几乎每次都出现 `anon:429` + `session_identity_rotated` |
| 出口池 | 被 24h 冷却迅速烧光 |

### 5.2 修复后

| 场景 | 请求数 | 结果 |
|---|---|---|
| 连续请求 | 10 | 10/10 `200`，`attempts=1` |
| 并发请求 | 6 | 6/6 `200` |
| 长上下文（每次 15k token） | 6 | 6/6 `200` |
| 重启后 | 8 + 4 | 12/12 `200` |
| 混合压测（3 会话 × 8 轮） | 24 | 24/24 `200` |
| 会话轮换 | — | 0 |
| 配额退役 | — | 0 |

唯一一次 `attempts=2`：

```text
anon:transport_dial:10001ms
```

对应出口传输错误冷却 1m30s，属于预期行为。

### 5.3 判定方法

```powershell
# 统计最近日志中的关键事件
Select-String -Path .\logs\stdout.log -Pattern 'session_identity_rotated|held_egress_retired|attempts=2' |
  Group-Object { ($_ -split '"event","')[1] } |
  Select-Object Name,Count
```

健康状态里关注：

- `warp.active > 0`；
- `warp.cooling` 不持续快速增长；
- `issues` 为空。

---

## 6. 单元测试

```powershell
$env:CGO_ENABLED = "0"
& 'C:\Go\go\bin\go.exe' test ./...
& 'C:\Go\go\bin\go.exe' vet ./...
```

结果：

| 包 | 结果 |
|---|---|
| `internal/gateway` | `ok` |
| `internal/warp` | `ok` |
| 其他包 | `ok` / 无测试 |

重点用例：

| 测试 | 覆盖 |
|---|---|
| `TestSessionGuardResolve` | 会话 ID 解析 |
| `TestSessionGuardRotate` | 会话升代 |
| `TestSessionGuardGeneration` | 代数读取 |
| `TestAcquireForPinsAffinity` | 同会话复用出口 |
| `TestAcquireForRebindsAfterRetirement` | 出口退役后重绑 |
| `TestAcquireForRoundRobinWithoutAffinity` | 无亲和轮询 |
| `TestPenalizeEgressRetiresHeldExit` | 退役指定 `/64` |
| `TestPenalizeEgressIgnoresUnknownKey` | 未知 `/64` 不误伤 |

---

## 7. 已知环境约束

1. **`10808` 已失效**：WARP 注册先超时再直连，补池约 30 秒/个；清空 `proxies` 可消除这 10 秒。
2. **`xray_tun` 已失效但占默认路由**：`warp.tun:true` 会把握手送进死管道，因此必须 `tun:false`。
3. **`source_ipv6` 是真实 `/64`**：发布/分享配置时必须替换，否则暴露网络信息。
4. **WebUI 密码已重置**：`config.json` 只保留 Argon2id 哈希。
5. **`config.json.warp-state.json` 曾清空**：备份在 `.bak-20261103`，18 个冷却 `/64` 已移除。

---

## 8. 复现步骤

```powershell
# 1. 构建
$env:CGO_ENABLED = "0"
& 'C:\Go\go\bin\go.exe' build -trimpath -o opencode2api.exe ./cmd/opencode2api

# 2. 准备配置（从模板复制并修改）
Copy-Item .\config.example.json .\config.json
# 编辑 listen / server_keys / anonymous / warp / webui

# 3. 启动
.\start-background.ps1

# 4. 等待就绪
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz

# 5. 发请求
$body = '{"model":"deepseek-v4.1-flash-free","messages":[{"role":"user","content":"OK"}],"max_tokens":8,"stream":false}'
[IO.File]::WriteAllText("$PWD\request.json", $body, (New-Object System.Text.UTF8Encoding($false)))
curl.exe --noproxy "*" -H "Authorization: Bearer sk-local-test" -H "Content-Type: application/json" --data-binary "@$PWD\request.json" http://127.0.0.1:28080/v1/chat/completions

# 6. 看日志
Get-Content .\logs\stdout.log -Tail 50
```

---

## 9. 与上游的差异摘要

| 领域 | 变化 |
|---|---|
| WARP | 用户态出口池、`/64` 轮换、物理网卡绑定、启动降级、冷却持久化 |
| 429 | 会话粘出口、首 429 换会话、次 429 才退役出口 |
| 代理 | `#pool` HTTP/1.1、健康检查、key 重绑、熔断、IP 封禁冷却 |
| Windows | Job Object、后台脚本、就绪轮询 |
| 管理台 | 代理折叠、清空确认、模型探测、日志流、配额 |

---

## 10. 基线维护

更新本文件时至少记录：

1. 日期、PID、二进制 SHA-256；
2. `go version` 与构建命令；
3. `/healthz` 完整 JSON；
4. 本轮验证的请求数、成功率、`attempts` 分布；
5. 关键日志事件计数；
6. 配置变更（脱敏）。

> 不要在本文件写入真实 key、密码、`source_ipv6` 或任何可复用的凭据。
