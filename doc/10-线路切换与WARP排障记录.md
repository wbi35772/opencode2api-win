# 线路切换与 WARP 排障记录（2026-11-07）

> 记录范围：`win/` 这份 Windows 独立仓库与当前部署；不含 InstaCloud 的 `opencode2api-2`、Docker 和 Linux farm。
> 本文归档 2026-11-07 的排障结论、代码修复、实测数据和换线后的排查手册。部署基线仍见 [09-WARP-SOCKS5-UDP-基线.md](09-WARP-SOCKS5-UDP-基线.md)。

## 1. 结论摘要

- **WARP 完全不运行的直接原因**：生产 `config.json` 里 `warp.enabled=false`（mtime 2026-11-07 08:21:01），不是线路故障。08:40 恢复启用后建池成功。
- **启动修复**：`newPool` 的首个出口现在会对 `errDuplicateEgress`（`/64` 已 active/cooling）每 2 秒重试，直到调用方 deadline；旧行为是固定 4 次后 fail closed，冷却块多时会让启动先走 `warp_startup_fallback`。
- **当前状态（17:57）**：`warp.status=ready`、`size=6`、`preload=6`、`active=6`、`standby=6`、`cooling=72`；09:24 的 free 请求 `HTTP 200 / 3.50s / attempts=1`。
- **429 是 free 配额按出口/会话计**，不是线路坏；`pool_size` 12→6 不会降低 429，只减少启动时的注册 churn。当前瓶颈是 72 个 cooling `/64`（最早 2026-11-07 17:08:48 到期，最晚 2026-11-08 09:18:17）。
- **换线后“每天一次、几十分钟到几小时所有 CF 节点 -1”**：最像 ISP 每日重拨触发 + 新 IP 被 GFW/Cloudflare 拉黑，或新线路到 Cloudflare 的路由/国际出口问题；不是 UDP 被单独查杀。手动重拨换 IP 可区分 A/B。
- **cfnew / Workers 没有通用 UDP**：只做 DNS UDP；EDtunnel 的 `VLESS_OUTBOUND` 之所以有 UDP，是因为出站落在一台外部 VPS 上；纯 Worker 不能扛 WARP 的 WireGuard UDP。

## 2. 时间线（本地时间 UTC-8）

| 时间 | 事件 | 证据 |
|---|---|---|
| 08:07:51 | 旧网关启动（PID 30800） | `start-supervised.ps1` 父进程 39332 |
| 08:21:01 | `config.json` 被改为 `warp.enabled=false` | healthz 无 `warp` 字段 |
| 08:39:50 | 备份 `config.json.bak-20261107-083950`，改回 `enabled=true` | 重启后 08:40:03 启动 |
| 08:40:18 | 建池成功，`size=12, preload=8` | healthz `warp ready` |
| 08:41:14 | 非流式 free 请求 200 | 6.51s，attempts=1 |
| 08:50:26 | 备份 `config.json.bak-20261107-085026`，`pool_size` 12→6 | 重启后 08:50:38 启动 |
| 08:50:51–08:51:16 | 连续 4 个 `/64` 都是 cooling，首出口 fail closed | `warp_startup_fallback` |
| 08:54:40 | 后台 retry 第 3 次成功 | `size=6, active=1, standby=1, cooling=66` |
| 09:11:42 | 备份旧 exe `opencode2api.exe.bak-20261107-091142` | 替换为新构建 |
| 09:11:44 | 新网关启动（PID 42524） | 当前运行进程 |
| 09:20 | SOCKS5 UDP STUN 超时（`i/o timeout`） | relay circuit 打开 60s |
| 09:21:34 | `SOCKS5 UDP relay recovered` | 日志事件 |
| 09:22 | STUN 3/3 通过，直连 STUN 也通过 | `STUN reply from 162.159.207.0:3478` |
| 09:24:15 | healthz `active=4, standby=4` | 池子已重新填到 4 个出口 |
| 09:24:27 | free 请求 200 | 3.50s，attempts=1，`content:"OK"` |
| 17:57 | healthz `active=6, standby=6, cooling=72` | 池子满 6/6 |

## 3. 代码变更

### 3.1 `internal/warp/pool.go`

新增常量：

```go
const firstDuplicateRetryDelay = 2 * time.Second
```

`newPool` 的首出口循环从“只等注册 429 的 pause，其他错误立即 fail closed”改为：

- `registrationPause(err) > 0`：等待服务端给的 pause；
- `errors.Is(err, errDuplicateEgress)`：等待 2 秒后重试；
- 其他错误：仍然 fail closed。

效果：spent-block 集合很大时，首出口不会因为连续撞到 cooling `/64` 而放弃启动；会一直重试到调用方 deadline。日志在 debug 级别输出 `first egress is a spent block; retrying in 2s`。

### 3.2 `internal/warp/pool_test.go`

新增 `TestNewPoolRetriesDuplicateFirstEgress`：

- 前 6 次 `createExit` 返回 `errDuplicateEgress`，第 7 次返回 fresh 出口；
- 15 秒 ctx；
- 断言 `active=1`、`egressKey=2a09:bac5:c82f:32a0::/64`、`calls >= 7`。

### 3.3 构建与验证

- `go test ./...`、`go vet ./...` 通过（Go 1.27.3，`C:\Go\go\bin\go.exe`）。
- 当前二进制：SHA256 `507F69216E491D9CD3AD8F632848748226C5284D3B784325F9FB342BD511BAEF`，19,814,400 字节。
- 旧二进制备份：`opencode2api.exe.bak-20261107-091142`。
- **尚未提交**：`internal/warp/pool.go`（modified）、`internal/warp/pool_test.go`（untracked）、本文档与索引改动都在工作区；未跟踪的调试脚本按之前约定保留，不提交。

## 4. 参数体检

| 参数 | 现值 | 性质 | 评估 |
|---|---|---|---|
| `probeTimeout` | 7s | 硬编码 `exit.go` | 当前线路够用；高延迟线路建议 10–15s 并做成可配置 |
| `probeCandidates` | 6 | 硬编码 | 一般够用；坏线路可能误杀可用出口 |
| 探测目标 | `https://opencode.ai/cdn-cgi/trace` + `[2606:4700:4700::1111]` | 硬编码 | 目标被 RST 时所有出口判死；建议可配置 |
| 启动 WARP 等待 | 90s | 硬编码 `runtime.go` | 小于注册器最坏约 134s，慢线路会先 fallback |
| 注册请求 | 15s dial / 10s TLS / 30s client / 4 次，退避 2/4/8s | 硬编码 `registrar.go` | 与 90s 启动等待不匹配；代理失败会回退直连 |
| `emptyPoolWait` | 25s | 硬编码 | 合理；空池后回退代理 |
| `refillRetryDelay` | 5s | 硬编码 | 合理；连续 3 次失败后 1–5m 退避 |
| `maxPreload` | 8 | 硬编码 | 合理，避免启动注册过多 |
| `pool_size` | 6 | 可配置（1–32） | 不创造新 `/64`；6 比 12 少 churn，但冷却块多时仍填不满 |
| `source_ipv6` / `socks_udp` | 空 / `socks5://127.0.0.1:10808` | 可配置，互斥 | 当前正确；数据面走 SOCKS5 UDP |

关键判断：

- **7s 不是今天的问题**：09:24 建池和请求都在秒级完成；没有出现 `handshake=never rx=0`。
- **90s 启动等待是“看起来起不来”的来源之一**：注册器最坏 `4×30s + 2+4+8s ≈ 134s`，90s 会先截断并走 `warp_startup_fallback`，后台再重试。
- **硬编码探测目标风险最大**：IPv6 栈只探 `opencode.ai` 和 `1.1.1.1` 的 v6 trace；新线路对 `opencode.ai` 不友好时，所有出口都会被误判。
- **`pool_size=12` 的期望值与 `/64` 供给冲突**：Cloudflare 按 `/64` 发出口、429 后 24h 冷却；state 里已有 72 个 cooling，12 个注册里多次重复。6 只是减少启动注册次数，不会降低 429。
- **state 清理**：`config.json.warp-state.json` 保存 cooling `/64`。删除前先备份；清空会复用已花掉的 `/64`，代价是再吃一轮 429。当前最早 17:08:48 到期，最晚 2026-11-08 09:18:17。

## 5. Cloudflare Workers / cfnew 的 UDP 结论

- cfnew（`byJoey/cfnew`）README 的协议表：**DNS 只做 VLESS 的 UDP DNS 转发；不提供通用 UDP、游戏 UDP 或 QUIC 隧道。**
- Workers/Pages 运行时只有出站 TCP `connect()`；2026-08 新增的是经 Spectrum 的**入站 TCP**，UDP 仍未开放。
- `workers-tunnel` README 明确写 “UDP proxy is not supported”。
- EDtunnel 的 “VLESS outbound proxy with full UDP support” 是 Worker 把流量转发到一台**外部 VLESS/VPS**，UDP 由那台 VPS 发出；Worker 本身仍是 TCP 前置。
- Cloudflare Spectrum 是付费 L4 代理：任意 TCP/UDP 要 Enterprise；低档只有 SSH/RDP/Minecraft。
- 当前节点是 **Cloudflare 前置的 VPS/Argo**（`VLESS+WS+TLS → instacloud.wbi35772.us.ci:443`，path `/vless-argo?ed=2560`），不是 Worker；UDP 由后面的 VPS 出站，所以 STUN 能间歇通过。
- 结论：要 WARP UDP，保留/优化 VPS+Argo（确认 XUDP、换 UDP 质量更好的 VPS），或上 Spectrum；cfnew 这类纯 Worker 方案不适用。Hysteria2 不能走 Cloudflare CDN，只能直连 VPS。

## 6. 换线后“每天长时间 -1”的排查

普通 PPPoE 24h 重拨只断 1–5 分钟；几十分钟到几小时的全 CF `-1` 不是重拨本身，更像：

- **A. 重拨后拿到被拉黑的 IP**：ISP 每日重拨是触发点，新 IP 被 GFW 或 Cloudflare 标记；所有 CF anycast 对该 IP 不可达，持续到下次换 IP/解封。
- **B. 新 ISP 到 Cloudflare 的路由/国际出口问题**：换 IP 也没用，所有 CF 节点一起 `-1`，`tracert` 卡在 ISP 国际出口。

区分方法（断的时候立刻做）：

```powershell
# 1. 记录当前公网 IP
curl.exe -sS --max-time 5 https://ifconfig.me

# 2. 手动重拨（路由器断开/连接）换 IP，再测
curl.exe -sS --max-time 5 https://www.cloudflare.com/cdn-cgi/trace
curl.exe -sS --max-time 5 https://1.1.1.1/cdn-cgi/trace

# 3. 区分封锁类型
curl.exe -v --max-time 8 https://www.cloudflare.com/cdn-cgi/trace
tracert -d 1.1.1.1
```

判读：

- 换 IP 后立刻恢复 → **A，IP 被拉黑**（GFW/Cloudflare）。
- 换 IP 仍 `-1` → **B，线路/路由问题**。
- TCP 通但返回 403/1020 → Cloudflare 封 IP；TCP 超时/RST → 路由/GFW。
- 手机热点能用 → 新线路问题；热点也不能用 → 节点/VPS 侧。
- 顺手排除新线路常见坑：IPv6 坏（`curl -4` vs `-6`）、PPPoE MTU 1492（`ping -f -l 1472 1.1.1.1`）、DNS 污染（`nslookup`）；公网 IP 是 `100.64.0.0/10` 说明是 CGNAT 共享 IP，更容易被 Cloudflare 连带封。

### 处理优先级

1. **换 IP**：路由器重拨；脚本检测 CF trace，失败就重拨；CGNAT 找 ISP 要公网 IP/桥接。
2. **走 IPv6**：`curl.exe -6` 测 CF；能用就把节点域名设成 AAAA-only，或客户端强制 IPv6。
3. **加 TCP 中转（最稳）**：找一台非 CF IP 的 VPS，做纯 TCP 转发：

   ```bash
   # 中转 VPS 上
   socat TCP-LISTEN:8443,fork,reuseaddr TCP:instacloud.wbi35772.us.ci:443
   ```

   客户端把 `address` 改成 `RELAY_IP`、`port` 改成 `8443`，**SNI/Host 仍填 `instacloud.wbi35772.us.ci`**。UDP 在 VLESS/XUDP 的 TCP 流里，中转只转 TCP 就够，WARP UDP 也能过。

4. **换 CF 目标**：换域名/SNI/端口/CF IP，只在“目标被封”时有帮助；源 IP 被封时没用。
5. **找 ISP 换段/换线**：整个 IP 段被标记时，重拨没用。
6. **降低触发**：少用 Argo/高并发，换 VLESS-REALITY 等；已被拉黑的 IP 不会因此解封。

注意：中转 IP 也可能被拉黑，选不同 ISP/地区、可轮换的 VPS；Hysteria2 这类 UDP 协议不能走 TCP 中转，得用 UDP 转发或直连 VPS。

## 7. 当前部署与节点

| 项目 | 值 |
|---|---|
| API / WebUI | `127.0.0.1:28080` / `127.0.0.1:28081` |
| 网关进程 | PID 42524，2026-11-07 09:11:44 启动 |
| WARP | `enabled=true`、`pool_size=6`、`stack=ipv6`、`tun=true` |
| WARP 数据面 | `socks_udp=socks5://127.0.0.1:10808` |
| 注册/普通代理 | `proxies=["socks5://127.0.0.1:10808"]` |
| `source_ipv6` | 空（与 `socks_udp` 互斥） |
| 当前节点 | xray SOCKS inbound `127.0.0.1:10808`（`udp=true`）；outbound `VLESS+WS+TLS → instacloud.wbi35772.us.ci:443`，path `/vless-argo?ed=2560` |
| 当前 healthz | `warp ready, size=6, preload=6, active=6, standby=6, cooling=72` |

当前 healthz（17:57）：

```json
{"status":"ok","ready":true,"warp":{"status":"ready","stack":"ipv6","size":6,"preload":6,"active":6,"standby":6,"cooling":72}}
```

请求日志里的 `egress`/`exit_id` 为空是遥测字段没填（`meta.Egress` 未赋值），不代表没走 WARP；用 healthz 的 `standby` 下降和 `attempts` 判断。

## 8. 待办

- 提交本轮修复与文档：`internal/warp/pool.go`、`internal/warp/pool_test.go`、本文、`doc/README.md`、`doc/09` 的更新说明；当前尚未 commit。
- 继续观察 `relay circuit`、`registration paused`、`warp_startup_fallback`；如果 relay 再次打开，优先查 VPS 的 UDP 出口/NAT，而不是继续加 `pool_size`。
- 如果换线后的长时间 `-1` 再次出现，按第 6 节先记录公网 IP、手动重拨换 IP 再测；A 类考虑 TCP 中转，B 类找 ISP 换段/换线。
- 后续可考虑把 `probeTimeout`、探测目标、启动等待做成配置项，减少硬编码对换线的敏感性。
- 未跟踪的调试脚本（`add_cooldown.py` 等）和 `docs/warp-farm-linux.md` 按之前约定保留，不提交。

## 9. 证据与命令

```powershell
# 健康检查
curl.exe --noproxy "*" -sS --max-time 5 http://127.0.0.1:28080/healthz

# 当前配置
$cfg = Get-Content .\config.json -Raw | ConvertFrom-Json
"warp.enabled=$($cfg.warp.enabled) pool=$($cfg.warp.pool_size) socks_udp=$($cfg.warp.socks_udp)"

# 当前二进制
(Get-FileHash .\opencode2api.exe -Algorithm SHA256).Hash
(Get-Item .\opencode2api.exe).Length

# state 冷却记录
$s = Get-Content .\config.json.warp-state.json -Raw | ConvertFrom-Json
$s.cooling.PSObject.Properties.Count
```

日志关键事件：

- `WARP egress recovered in the background`
- `Discarding duplicate WARP exit ... already active or cooling down`
- `OpenCode WARP first registration paused for ...`
- `SOCKS5 UDP relay recovered` / `relay circuit is open`
- `warp_startup_fallback` / `warp_retry_succeeded`

## 10. 2026-11-09 补充：429 持续复发的根因与修复

- **根因（已验证）**：free 额度按公网 `/64` 记账，而新注册的 WARP 出口大多落在“别人当天已经用掉额度”的共享块上。旧的 `AcquireFor` 轮询会把未验证的新块直接发给真实请求，撞上 429 才退役；探针（`probeAttempt`）用空 affinity 轮询，也会消耗并退役新块。两者叠加会出现连锁：429 退役 → 注册新块 → 新块再 429 → 池子被烧穿（03:27 那次 `cooling=142`，`active/standby` 一度 1/0）。
- **修复**：
  - `internal/warp/exit.go`：新增 `exit.lastOK` 与 `markSuccess`/`served`，由 `Report` 的 2xx 路径盖章。
  - `internal/warp/pool.go`：`AcquireFor` 改为 proven-first，`leaseLocked` 先租已验证出口，只有在没有可用已验证出口时才碰未验证块；429 时 `Unbind` 继续丢弃会话 pin。
  - `internal/gateway/probe.go`：探针固定 `probeAffinity`，不再每小时轮询扫描新块。
  - `config.json`：`warp.pool_size` 6 → 10（preload 8），给已验证出口留出备用量。
- **实测（已验证，2026-11-09 03:47 重启后）**：新二进制 SHA256 `7A6DA0A4C6BD61D09FA3CD764BEB4D36C79A37730C4D8F3F3C4BB1F8AB347759`；连续 5 个全新 session+project 请求全部 200，第一个 `attempts=6`（5×429→200，冷启动采样），其余 4 个均 `attempts=1`；healthz `size=10, preload=8, active=2, standby=2, cooling=157`。`go test ./...` 通过。
- **未验证/限制**：长周期（24h）429 频率仍需观察；主机系统 IPv6 仍不可用（只有 `::1`，无默认路由，ISP/路由侧未下发），与 WARP 无关（WARP 为用户态 netstack）。若要连冷启动那一次采样也消掉，可考虑在出口注册时做一次真实 free-tier 探针，代价是每次注册多一个请求。

## 11. 2026-11-09 补充：注册期 free-tier 预检（把 429 挡在池外）

- **目的**：429 的根因是共享 `/64` 已被别人用掉；proven-first 只能让请求少撞一次，冷启动仍会采样坏块。现在每个新出口在入池前先发一个极小 free-tier 请求验证额度，坏块直接隔离 24h，不再消耗真实请求的第一次尝试。
- **实现**：
  - `internal/warp/pool.go`：新增 `Verifier`/`DialFunc`/`ErrEgressSpent`；`SetVerifier` 在网关构造后安装，并对池内空闲、未验证的出口做后台补检；`createExit` 在候选可用后调用 `checkQuota`，返回 `ErrEgressSpent` 时 `quarantine` 该 `/64` 24h 并继续注册下一个；验证返回 2xx 会给出口盖 `lastOK`，因此入池即 proven。
  - `internal/gateway/probe.go`：`verifyWARPEgress` 用目录里第一个可用的 free 模型发一条 `Reply with OK.` 的最小请求（BunTransport，经候选出口，45s 预算）；`isFreeUsageLimit` 只把带 `FreeUsageLimitError`/`free usage limit` 标记的 429/403 判为“块已耗尽”，普通限流或探测失败不隔离。
  - `internal/gateway/runtime.go`：`build()` 在 `New` 之后调用 `warpPool.SetVerifier(gateway.verifyWARPEgress)`；池被多个 runtime 共享时每次 build 都会重新安装。
- **实测（已验证，2026-11-09 04:09 重启后）**：新二进制 SHA256 `EEFC59DE42A1572C61BF9D2ED8FE0BC8EBAFACEFAFF5B5BEBDC0D5E42EF218D3`；启动后日志出现 `already spent; cooling 24h0m0s before use`，坏块在入池前被拦下；连续 4 个全新 session+project 请求全部 200 且全部 `attempts=1`；重启后 `free_tier_429_body` 计数为 0。`go test ./...`、`go build ./...` 通过，`gofmt` 无输出。
- **代价与限制**：每次注册多一个极小请求（消耗该块少量额度）；预检只在 `catalog.List()` 有可用 free 模型时生效，目录未就绪时出口仍按“未验证”入池；Cloudflare 目前反复下发相同 `/64`（日志里大量 duplicate），池子很难涨到 `preload=8`，`active` 长期在 1–2，属于环境现象，不影响请求成功。
- **测试**：新增 `TestCreateExitQuarantinesSpentEgress`，覆盖“坏块隔离、下一个可用块入池、reservation 释放”。
