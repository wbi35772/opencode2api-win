# WARP SOCKS5 UDP 中继基线

> 基线日期：2026-11-05（本地时间）
> 适用范围：`win/` 这份 Windows 独立仓库与当前部署，不含 InstaCloud、Docker 和 Linux farm。
> 本文记录**当前实际运行的二进制、配置、数据路径、验证结果和回滚方式**，并回答“换 UDP 节点能否刷新 WARP 池”的问题。

## 1. 当前部署基线

| 项目 | 值 |
|---|---|
| 工作目录 | `D:\Project\Openchamber\Token\opencode\jasonxu114514-opencode2api\win` |
| 运行二进制 | `opencode2api.exe` |
| 二进制 SHA256 | `1955AB1F783EA5DFD68B9666A4E50343AF2590E5AE5F49E3417AD2E9DA569301` |
| 二进制大小 | 19,716,608 字节 |
| 上一版备份 | `opencode2api.exe.bak-20261105-2043`（SHA256 `3C9DC38526B5CB4EA1A4622455006C70FCA5BB2E1B56D326D366D6B7E466E81A`，19,641,344 字节） |
| 配置备份（启用中继前） | `config.json.bak-20261105-2100-socksudp`（SHA256 `C2AE2D436548EEE28B47FB840648AFD917FFD1551F7DFEFD019F0CFBB6073162`） |
| API 监听 | `127.0.0.1:28080` |
| WebUI 监听 | `127.0.0.1:28081` |
| WARP | `enabled=true`、`pool_size=12`、`stack=ipv6`、`tun=true` |
| WARP 外层 UDP | `socks_udp=socks5://127.0.0.1:10808` |
| 注册/普通代理 | `proxies=["socks5://127.0.0.1:10808"]` |
| `source_ipv6` | 空（与 `socks_udp` 互斥） |
| 启动/停止 | `.\start-background.ps1` / `.\stop-opencode2api.ps1` |
| 日志 | `logs\stdout.log`、`logs\stderr.log`、`logs\pid.txt` |
| 持久化状态 | `config.json.warp-state.json`（已判死的 `/64` 冷却记录）等 `config.json.*` 运行文件 |

本次切换后的健康检查（数值会随负载变化）：

```json
{
  "status": "ok",
  "ready": true,
  "warp": {
    "status": "ready",
    "stack": "ipv6",
    "size": 12,
    "preload": 8,
    "active": 8,
    "standby": 8,
    "cooling": 61
  }
}
```

`cooling` 是历史 `/64` 的冷却记录数，不代表当前出口不可用。

## 2. 数据路径

当前有两段出口，各自独立配置：

```text
注册（HTTPS）
  opencode2api.exe
    -> proxies[0] = socks5://127.0.0.1:10808（TCP CONNECT）
    -> api.cloudflareclient.com
    -> Cloudflare 分配本次注册的 WARP 设备地址（IPv6 /64）

数据面（WireGuard UDP）
  opencode2api.exe（进程内 netstack）
    -> WireGuard 外层 UDP
    -> socks_udp = socks5://127.0.0.1:10808（SOCKS5 UDP ASSOCIATE）
    -> v2rayN / xray 10808
    -> Cloudflare WARP endpoint
    -> 目标站看到的是 WARP 分配的 IPv6（不是 10808 节点的公网 IP）
```

两个关键点：

1. **目标站看到的出口 IP 是 WARP 分配的 IPv6**，不是本机 IP，也不是 v2rayN 节点 IP。节点只负责把 WireGuard 包送到 Cloudflare。
2. **注册走哪个节点，会影响 Cloudflare 本次看到的源 IP 和分配结果**。同一节点反复注册容易落到同一个 `/64`，日志表现为 `Discarding duplicate WARP exit ...`，随后出现 `registration paused for 1m0s`。

## 3. 配置语义

| 字段 | 当前值 | 说明 |
|---|---|---|
| `warp.socks_udp` | `socks5://127.0.0.1:10808` | 可选。为空时保持旧行为：WireGuard 外层 UDP 从本机直接发出 |
| `warp.tun` | `true` | 对回环 SOCKS5 代理没有影响；非回环代理且 `tun=false` 时，中继 socket 会钉在物理网卡上 |
| `warp.source_ipv6` | 空 | 与 `socks_udp` 互斥：中继模式下外层源地址由代理负责 |
| `proxies[0]` | `socks5://127.0.0.1:10808` | 注册和普通上游流量使用；只要求 TCP，不要求 UDP |

行为细节：

- `socks_udp` 是显式 opt-in，默认空，不影响原有部署。
- 启用后先做 preflight（TCP 握手 + UDP ASSOCIATE）。代理不可用时**不会消耗设备注册**，启动走 `warp_startup_fallback`，随后按 15s → 30s → 1m → 2m → 5m 后台重试。
- 中继有共享熔断：连续 3 次传输失败后打开 60 秒。打开期间快速失败、不摘除现有出口、不发起新注册；冷却后只放一个半开探针。
- 注册代理和数据面代理**可以不是同一个节点**：注册只需要 SOCKS5 TCP CONNECT，数据面节点必须支持 UDP ASSOCIATE。
- `/64` 去重仍然生效；中继不会改变“同一 `/64` 算同一个出口额度”的规则。

## 4. 本次验证记录（2026-11-05）

| 测试 | 证据 | 结果 |
|---|---|---|
| v2rayN/xray 10808 监听 | xray PID 26620，`TCP 127.0.0.1:10808 LISTENING` | 通过 |
| SOCKS5 UDP ASSOCIATE + STUN | `STUN reply from 162.159.207.0:3478 via 127.0.0.1:55304` | 通过 |
| 独立 debug 实例（28090，pool=3，`socks_udp`） | 探测出口 `2a09:bac1:7681:62b0::39d:2c`、`2a09:bac5:c82d:1678::23d:36` | 通过 |
| 独立实例真实免费模型请求 | `anonymous WARP upstream accepted request ... "proxy":"warp:2a09:bac5:c82d:1678::/64"`，200 | 通过 |
| 生产单请求 | `deepseek-v4.1-flash-free` 返回 `pong`，200，6.6s | 通过 |
| 生产 3 并发 | 3/3 返回 200；其中一条 `anon:429 anon:429 anon:200` | 通过 |
| 单元测试 | `go test ./internal/warp`（含 live STUN） | 通过 |
| 构建与全量测试 | `go build ./...`、`go test ./...` | 通过 |
| 错误检查 | 无 `level":"ERROR"`、无 `relay circuit`、无 `warp_proxy_fallback` | 通过 |

启动时出现过一次 `warp_startup_fallback`：新注册撞上已 active/cooling 的 `/64`，后台 8 秒后 `warp_retry_succeeded` 恢复。这是继承的 60 条 spent block 导致的重复出口，不是中继故障。

## 5. 更换 UDP 节点与刷新 WARP 池

**结论：方向正确，但要分清“注册出口”和“数据面出口”。**

| 配置 | 决定什么 | 是否必须支持 UDP |
|---|---|---|
| `proxies[0]` | 注册请求从哪个源 IP 出去，从而影响本次注册拿到的 WARP 设备地址/`/64` | 否，HTTPS 走 TCP 即可 |
| `warp.socks_udp` | WireGuard 外层 UDP 能不能通、走哪条链路 | 是，必须支持 SOCKS5 UDP ASSOCIATE |

当前两者指向同一个 v2rayN 节点，所以换节点后“注册源 + UDP 链路”一起换。重新注册会生成新的 WARP 设备地址，从而刷新池子里的 IPv6/`/64`。但 Cloudflare 没有“这个节点注册池用完”的显式计数器；实际看到的是限速、重复 `/64`、`registration paused` 这些现象。换源 IP 能绕开当前现象，但新节点仍可能随机落到旧 `/64`。

### 操作步骤

1. 确认新节点是 SOCKS5 且支持 UDP ASSOCIATE。可以用项目自带的 live 测试：
   ```powershell
   $env:WARP_SOCKS_LIVE='socks5://NEW_HOST:NEW_PORT'
   & 'C:\Go\go\bin\go.exe' test ./internal/warp -run TestLiveSocksRelaySTUN -v -count=1
   ```
   看到 `STUN reply from ...` 才算通过。
2. 修改 `config.json`：
   ```json
   "proxies": ["socks5://NEW_HOST:NEW_PORT"],
   "warp": {
     "enabled": true,
     "pool_size": 12,
     "stack": "ipv6",
     "tun": true,
     "socks_udp": "socks5://NEW_HOST:NEW_PORT"
   }
   ```
   如果只想换数据面、保留原注册出口，可以只改 `warp.socks_udp`，让 `proxies[0]` 继续指向原节点。
3. 让新配置生效：
   - 改 `socks_udp` 会改变池子的 key，保存配置/重启后会重建 WARP 池；
   - 只改 `proxies` 不会自动重建，需要重启进程。
   最稳妥的做法是保存后重启：`.\stop-opencode2api.ps1`，再 `.\start-background.ps1`。
4. 想彻底清掉旧池记忆：先备份，再删除 `config.json.warp-state.json`。它只保存已判死 `/64` 的冷却记录，删除不会影响设备注册代码，但会失去旧冷却信息。
5. 验证：
   ```powershell
   curl.exe --noproxy "*" http://127.0.0.1:28080/healthz
   ```
   期望 `warp.status=ready`，`active/standby` 正常增长；日志没有成片 duplicate、没有 `warp_startup_fallback`、没有 relay 熔断。

### 不要误解的地方

- 新节点的公网 IP 不会成为 WARP 出口；目标站看到的始终是 Cloudflare 分配的 WARP IPv6。
- 换节点不会改变 Cloudflare 账号/设备额度规则，只改变注册来源和链路。
- 免费额度按 `(会话, WARP /64)` 计；换节点拿到新的 `/64` 才等于换了一个新的额度单元。
- 如果新节点只支持 TCP、不支持 UDP，preflight 会失败，WARP 不会注册，也不会把旧出口全部烧掉。

## 6. 运维检查清单

```powershell
# 健康检查
curl.exe --noproxy "*" http://127.0.0.1:28080/healthz

# 最近日志
Get-Content .\logs\stdout.log -Tail 100

# 看中继控制连接（opencode2api 到 10808）
netstat -ano | findstr :10808

# 看进程的 UDP socket（中继数据面）
$pid = (Get-Content .\logs\pid.txt)
Get-NetUDPEndpoint -OwningProcess $pid
```

| 日志事件 | 含义 | 处理 |
|---|---|---|
| `warp_startup_fallback` | 启动时没建出出口，先降级运行 | 看后面的 `warp_retry_succeeded`；持续失败查节点 UDP 和 Cloudflare 可达性 |
| `WARP egress recovered in the background` | 后台重试成功，已热切回 WARP | 无需重启 |
| `Discarding duplicate WARP exit ... already active or cooling down` | 新注册落到已被占用/冷却的 `/64` | 正常去重；连续出现会触发注册暂停 |
| `registration paused for 1m0s` | 连续重复出口后主动节流 | 等一分钟；频繁出现考虑换注册节点或清理 state |
| `relay circuit is open` / `relay unavailable` | 中继连续失败，熔断打开 | 检查 v2rayN/xray 与 10808 的 UDP 能力 |
| `warp_proxy_fallback` | 免费档没有可用 WARP 出口，回退到普通代理 | 检查 WARP 池和出口健康 |

## 7. 回滚

```powershell
# 1. 停
.\stop-opencode2api.ps1

# 2. 恢复二进制和配置
Copy-Item .\opencode2api.exe.bak-20261105-2043 .\opencode2api.exe -Force
Copy-Item .\config.json.bak-20261105-2100-socksudp .\config.json -Force

# 3. 启
.\start-background.ps1
```

回滚会回到“无 `socks_udp`、外层 UDP 直连”的版本；`config.json.warp-state.json` 可以保留，也可以按需删除以清空冷却记录。

## 8. 本次变更清单

新增：

- `internal/warp/socks.go`：SOCKS5 UDP ASSOCIATE 客户端、UDP 封装/解封装、认证、控制连接重连、preflight、共享熔断。
- `internal/warp/socks_test.go`：假 SOCKS5 服务器、round-trip、认证、重连、熔断、penalty 短路。
- `internal/warp/socks_live_test.go`：真实代理 STUN 探测（`WARP_SOCKS_LIVE` 启用）。

修改：

- `internal/warp/pool.go`：`Options.SocksUDP`、`Pool.relay`、penalize 熔断短路、refill 的 `relayUnavailablePause` 分支。
- `internal/warp/exit.go`：`newExitWithRelay`、Bind 注入。
- `internal/config/config.go`：`WarpConfig.SocksUDP` 与 Normalize 校验（含 `source_ipv6` 冲突）。
- `internal/gateway/runtime.go`：池 key 加入 `SocksUDP`、Options 透传。
- `webui/index.html`、`webui/js/pages/config.js`、`webui/js/i18n.js`：WARP 配置页 `socks_udp` 字段与文案。
- `README-WIN.md`、`doc/README.md`、`doc/02-配置参考.md`、`doc/03-网络-WARP-与出口.md`：同步当前基线。

## 9. 已知限制

- 中继节点必须支持 SOCKS5 UDP ASSOCIATE；只支持 TCP 的节点无法作为数据面。
- 中继多了一跳，延迟高于直连 UDP；节点本身抖动会直接反映到 WARP 隧道。
- 共享熔断是进程内状态；重启进程后重新计数。
- `/64` 去重和 24 小时额度冷却仍然生效；换节点不保证一定拿到全新 `/64`。
- `warp-state.json` 会继承历史冷却记录；换节点后如持续遇到重复 `/64`，再考虑清理。
