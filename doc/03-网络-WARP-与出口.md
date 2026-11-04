# 网络、WARP 与出口

## WARP 是怎么实现的

不是系统级 VPN，是**进程内**的用户态 WireGuard + gvisor netstack：

```go
// internal/warp/exit.go:119
tunDevice, network, err := netstack.CreateNetTUN(localAddresses, dns, 1280)
```

`internal/warp/doc.go` 的说明是"packets … and the netstack terminates TCP inside the process"。也就是说隧道只存在于 `opencode2api.exe` 这个进程内部，只服务它自己发往上游的请求。

实际验证（WARP 开启并运行时）：

```text
网卡：VMnet8 / 以太网 / VMnet1 / vEthernet(WSL)     ← 没有新增虚拟网卡
IPv6 默认路由：无
全局 IPv6 地址：无（只有 fe80:: 链路本地）
```

本机连全局 IPv6 地址都没有，而 WARP 出口报告的是：

```text
2a09:bac1:1980:fe10::/64
2a09:bac5:2641:281e::/64
```

只可能来自进程内部。进程内的 WireGuard UDP socket 现在还会钉在物理网卡上（见下面「和 TUN 共存」），迭代后本机的全局 IPv6 地址已经存在，但结论不变：WARP 不影响系统里其它程序的出站。

**由此得出**：打开 WARP **不会**让系统里其它程序走 WARP，浏览器、OpenCode 自带的官方模型组、其他 CLI 都还是走本机直连。只有经过网关的请求才走 WARP。

## 出口按什么算

| 概念 | 规则 |
|---|---|
| 出口单位 | WARP 下是每个 IPv6 `/64`；代理下是每个代理 |
| 免费额度 | 上游按出口 IP/网段计，和你的本机公网 IP 无关 |
| 去重 | 只对内置 WARP 的 `/64` 做冷却去重，外部代理不去重 |
| 额度隔离 | 直连（官方客户端）和 WARP（网关）是两个独立的额度池 |

## 实测数据（2026-10-29，本机，未开 TUN）

### WARP 池

`pool_size=4`、`stack=ipv6`：

```text
18 秒就绪，2 个出口预加载
8 次免费模型对话：200 x 8
延迟 1.53–7.78 秒，均值约 3.5 秒
两个出口属于不同 /64
```

线上实例（同一份配置）连续 5 次：

```text
200 2.61s / 200 2.93s / 200 2.72s / 200 7.90s / 200 2.77s
```

健康检查：

```json
"warp":{"status":"ready","stack":"ipv6","size":4,"active":2,"standby":2,"cooling":0}
```

### 修复后的线上复测（2026-10-30 19:08–19:10）

`pool_size=3`、`stack=ipv6`、钉选开启：

```text
串行 10 次           200 x 10，2.1–7.6 秒
并发 6 次            200 x 6，2.6–8.6 秒
流式 3 次            200 x 3，首字节 2.1/2.1/11.9 秒，均含 [DONE]
主实例串行 10 次      200 x 10，2.6–4.4 秒
主实例流式 3 次       200 x 3，首字节 2.2/2.1/11.9 秒
出口轮换             2a09:bac5:2644:281e::3ff:6c → 2a09:bac5:2641:3141::4e8:1
```

期间一次 `transport error` 在 10 秒后换出口重试成功（`anon:transport:10001ms anon:200:1867ms`），冷却只记 1 条 90 秒；健康检查 `active 2, standby 2, cooling 0`。没有再出现 499/502 挂死。

### 连接稳定性对比

| 目标 | 直连 | 走 `127.0.0.1:10810` |
|---|---|---|
| `opencode.ai/`（20 次） | **20/20**，平均 1.27s，最大 1.99s | **9/20**，平均 1.49s |
| `models.opencode.ai`（10 次） | 8/10，平均 6.82s，最大 8.70s | 7/10，平均 2.91s |

`models.opencode.ai` 直连很慢，经常超过 10 秒，这也是之前 `model_catalog_stale` 告警的来源。走 WARP 后该告警消失。

### 免费额度

同一时间点：

```text
直连官方 API（10 次）  -> 429 x 10，1 秒内返回 FreeUsageLimitError
经网关走 WARP（8 次）  -> 200 x 8
```

同一个模型，一个挂一个通，差别只在出口。

## 池子怎么保持可用

来自 `internal/warp/pool.go`：

| 机制 | 值 | 作用 |
|---|---|---|
| `egressCooldown` | 30 分钟 | 被上游 403/429 判死的 `/64` 半小时内不再放回池子（额度是按这个块算的） |
| `transportCooldown` | 90 秒 | 隧道自己断了（超时/EOF）只短停，因为 Cloudflare 常把同一个 `/64` 再发回来，停 30 分钟会把唯一能用的块锁死 |
| 池空等待上限 | 25 秒 | 一个可用出口都没有时，`Acquire` 最多等 25 秒就返回 503 `warp_unavailable`，不再让请求挂到 100 秒以上 |
| 探测超时 | 7 秒/端点 | 覆盖 WireGuard 握手重传；同一端点的两个探测目标并行，最多试 6 个端点 |
| 注册重试间隔 | 5 秒 | 失败后的基础间隔 |
| 退避 | 1m → 2m → 4m → 5m 封顶 | 连续注册失败不会打爆 Cloudflare |
| 注册 429 | 遵守 `Retry-After` | 被限流时按对方给的时间等 |
| 热备 | 最后一个出口留 standby | 切换出口不用等新注册 |
| 上游 403/429 | 立即换出口 | 不再重试同一个 |

`pool_size` 是"保持多少出口在线"，不是"一次开多少"。不超过 8 时全部预热，开 6 就是 6 个都热着；超过 8 的部分按并发扩容。

### 429 冷却与淘汰（2026-11-01）

- 额度型 403/429 按公网 `/64` 冷却：首次 30 分钟，同一 `/64` 再次命中按 30m → 2h → 8h → 24h 递增，`Retry-After` 更长时优先。
- 冷却截止时间和命中次数持久化在 `config.json.warp-state.json`，重启后继承；2xx 会清空该 `/64` 的命中次数。
- 替换注册失败时只有传输层失败（90 秒冷却）的出口会回到池子；额度/限速出口不会复活，池空时由配置的代理兜底。
- 普通限速（响应体明示 `rate_limit` / `too many requests`）只冷却 1–5 分钟，不累计额度惩罚；无法识别的 403/429 一律按额度处理。

## WARP 依赖本机直连什么

只有两件事必须走本机网卡，其余全部发生在隧道内部：

| 依赖 | 协议/端口 | 实际路径 | 用途 |
|---|---|---|---|
| `api.cloudflareclient.com` | TCP 443 | 钉选的物理网卡 | 注册、启用设备 |
| WARP endpoint（`162.159.192.x`、`188.114.x` 等） | UDP 2408 | 钉选的物理网卡 | WireGuard 握手与数据 |
| `1.1.1.1` / `[2606:4700:4700::1111]` | TCP 443 | **隧道内部** | 仅用于验证隧道通不通 |

第三行是重点。`internal/warp/exit.go:350` 的探测走的是 `e.network.DialContext(...)`，也就是 netstack，不是本机 socket：

```go
DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
    return e.network.DialContext(ctx, target.network, address)
},
```

所以日志里这句

```text
verify WARP ipv4 egress: Get "https://1.1.1.1/cdn-cgi/trace": context deadline exceeded
```

的意思是"隧道里打不开 1.1.1.1"，**不是**"本机直连连不上 1.1.1.1"。本机能不能直连 1.1.1.1 和 WARP 能不能用，是两件事。

实测对照（2026-10-30）：

```text
07:36  1.1.1.1 直连 000(12s)   api.cloudflareclient.com 000(12s)   → WARP 起不来（注册卡在 TLS 握手）
07:38  1.1.1.1 直连 000(12s)   api.cloudflareclient.com 404(2.0s)   → WARP 正常注册，8/8、5/5 全通
```

判断 WARP 能不能用的标准是 **`api.cloudflareclient.com` 通不通 + 到 WARP endpoint 的 UDP 通不通**，不是 1.1.1.1。

隧道建立后的探测按顺序打两个目标：先 `https://opencode.ai/cdn-cgi/trace`（就是免费档真正要访问的域名，同时验证 DNS、TLS 和业务路径），再 `https://[2606:4700:4700::1111]/cdn-cgi/trace`。两者在同一个端点内并行，7 秒超时，最多换 6 个端点。要换第一个目标就改 `upstreamTraceURL`（`internal/warp/exit.go`）。

注册（HTTPS）现在优先走 `proxies` 里第一个非 `direct` 的代理，直连作为后备：`newRegistrar(log, proxy)` 会建两个 client，代理 client 不钉网卡（否则连不到 127.0.0.1），请求失败时自动用钉选的直连 client 重发。启动日志会出现 `WARP registration uses proxy ***`。WireGuard 数据面仍然只走本机——实测 10808 的 SOCKS5 UDP ASSOCIATE 能建（relay `127.0.0.1:57258`），但 `223.5.5.5:53`、`8.8.8.8:53`、`1.1.1.1:53`、`172.65.90.20:443` 全部 8 秒无回包（0/4），节点不转发 UDP，所以隧道本身无法借用它。

## 建不出出口时：降级启动 + 后台重试

早期版本是 fail-closed（建不出出口就退出、端口完全不监听）。现在改成：

| 阶段 | 行为 |
|---|---|
| 启动 | 最多等 `warpStartupDeadline`（90 秒）跑第一次注册 |
| 超时 | 以 `warp.enabled=false` 重建网关，28080/28081 照常监听，日志 `warp_startup_fallback` |
| 后台重试 | 15s → 30s → 1m → 2m → 5m → 之后每 5 分钟 |
| 重试成功 | 热切换到 WARP，日志 `WARP egress recovered in the background`，不需要重启 |

注意降级期间 anonymous 免费档走的是 `proxies`，`["direct"]` 就是本机直连，额度按本机公网 IP 计——也就是最不划算的那条路。所以看到 `warp_startup_fallback` 应该尽快查 Cloudflare 可达性。

匿名免费档还有第二层兜底：WARP 建不出出口、返回 429/403（额度或 IP 封禁）、或 5xx 时，`doAnonymousUpstream` 会把这次请求交给 `proxies` 里的代理再试一遍（日志 `warp_proxy_fallback`），不再直接 503。确定性 4xx（请求本身太长或格式错）不回退，原样返回。

流式请求另有一个 2 分钟空闲看门狗（`warpStreamIdle`，`internal/gateway/warp.go`）：WARP 响应体连续 2 分钟没有新字节就关闭 body 并把出口标记为失败，客户端收到明确错误后可以重试，而不是挂到请求级超时。这也是"测试能过、真实会话几十秒就断"的主要差别——测试是几秒的短请求，真实会话要维持几分钟的流。

## 和 TUN 共存

早期版本 TUN 一开 WARP 必挂。2026-10-30 起 WARP 的出站全部钉在物理网卡上，不再理会默认路由：

| 文件 | 做法 |
|---|---|
| `internal/warp/iface_windows.go` | `IP_UNICAST_IF`（IPv4，接口索引按网络字节序）/ `IPV6_UNICAST_IF`（选项号同为 31，主机序） |
| `internal/warp/bind.go` | WG 的 udp4 / udp6 socket 都带这个 `Control` |
| `internal/warp/registrar.go` | 注册用的 HTTPS Dialer 同样钉选（这是之前漏掉的一环） |

网卡是自动挑的：跳过 tun / tap / wintun / wireguard / singbox / clash / v2ray / vEthernet（VMware、WSL）等，并且**实际发一个 DNS 查询验证那张网卡真的能上网**，没有路由的虚拟网卡不会被选中；结果缓存 5 分钟，刷新失败时保留旧钉选。

实测（TUN 开启，`singbox_tun` 持有 `0.0.0.0/0`）：

```text
串行 8 次补全                    8/8 200
并发 6 次补全                    6/6 200
TUN 握着默认路由期间新注册出口    07:47:21  2a09:bac5:2642:2da5::48c:20
                                07:47:31  2a09:bac5:2643:183c::26a:57
```

注册 HTTPS 和 WireGuard 握手都不经过 TUN，这才说明整条引导链已经独立。辅助观察：

```text
netsh interface ipv4 show destinationcache
  以太网(ifIndex 9) → 192.0.2.1     WARP endpoint 的实际路径
  singbox_tun(16)   → 10.1.0.2       钉选之前的残留
```

仍然会失败的情况只有一个：**物理网卡完全到不了 Cloudflare，而只有 TUN 能到**。此时注册也会失败——这是刻意的，不希望它偷偷走回 TUN。需要那种行为时用逃生开关启动：`set OPENCODE2API_WARP_NO_PIN=1`。

## 代理健康检查

`internal/gateway/refresh.go`：

```go
proxyHealthCheckURL     = "https://cloudflare.com/cdn-cgi/trace"
proxyHealthCheckTimeout = 10 * time.Second
proxyHealthCheckInterval = 15 * time.Minute
```

配了代理时，网关会定期以及在有流量时探测这个地址。它不通就会把代理标成不可用，健康检查出现 `no_healthy_proxies`，免费档随即不可用，进行中的请求也可能被取消（现象是恰好 10 秒的 504）。

这也是为什么"代理明明能访问 opencode.ai 却不能用"：健康检查打的是 `cloudflare.com`，和业务域名不是同一条路径。

## 当前选择

```json
"warp": { "enabled": true, "pool_size": 3, "stack": "ipv6" },
"proxies": ["direct", "http://127.0.0.1:10808"]
```

理由：匿名免费档优先走 WARP，实测 10/10、6/6 全通并真实轮换 `/64`；WARP 不可用或被额度挡住时自动回退到 `proxies`（`direct` 优先，本机 xray 10808 作为后备），注册 HTTPS 也走 10808。`pool_size` 不用开太大——`/64` 供给有限，建议 3–6；新版把额度命中按 30m→2h→8h→24h 递增并持久化，池子再大也不能凭空造出未消耗的 `/64`。

要换回代理：`warp.enabled=false`，`proxies=["http://127.0.0.1:10808"]`。要换回纯直连：`warp.enabled=false`，`proxies=["direct"]`，但要接受 429。
