# Linux 版 WARP 方案 A 参考方案（`registration_proxies` / warp-farm）

> 适用：InstaCloud compute `opencode2api-2`，镜像部署自 `wbi35772/opencode2api@warp-stability`
> 目标：注册走多个出口代理，让 Cloudflare 分配不同的 egress `/64`；WireGuard 数据面仍从容器直出
> 性质：参考方案，命令均未执行；执行前先做第 2 节的只读检查

---

## 0. 结论（TL;DR）

`warp-stability` 已经把方案 A 做成开关，**不需要改代码**：

```sh
# 容器内
touch /var/lib/opencode2api/warp-farm.txt
```

然后重启 `opencode2api-2`。启动时 `opencode2api-wrapper.sh` 会先跑 `proxyctl start`，`proxyctl` 检测到 marker 后自动：

- 把 `cfnew/proxies.txt` 里的本地 SOCKS 监听写入 `warp.registration_proxies`
- 设 `warp.enabled = true`
- 清空 `proxies` 和 `proxyfile`，让数据面留在 WARP
- 保留你现有的 `pool_size`、`stack`、`source_ipv6`

当前配置已经是 `pool_size=16, stack=ipv6, source_ipv6` 为空，所以**只差一个 marker + 重启**。

---

## 1. 现状（实测，2026-11-05）

| 项 | 值 | 状态 |
|---|---|---|
| 部署仓库 | `wbi35772/opencode2api@warp-stability` | 已验证 |
| compute | `opencode2api-2` | 已验证 |
| 进程 | PID1 `opencode2api-app ... -web-same-port`；PID48 `mihomo -d .../cfnew` | 已验证 |
| `warp.enabled` | `false` | 已验证 |
| `warp.pool_size` | `16` | 已验证 |
| `warp.stack` | `ipv6` | 已验证 |
| `warp.source_ipv6` | 不存在（空） | 已验证 |
| `proxyfile` | `/var/lib/opencode2api/cfnew/proxies.txt` | 已验证 |
| `proxies` | `[]` | 已验证 |
| 本地 SOCKS 监听 | `127.0.0.1:21000-21201`，共 202 个 | 已验证 |
| 存活监听 | 181 / 202 | 已验证 |
| 去重出口 IP | 59 个 | 已验证 |
| `config.json.warp-state.json` | 8402 B，旧冷却记录 | 已验证 |
| `warp-farm.txt` | 不存在 | 已验证 |
| 线上镜像是否含 farm 分支 | 未验证（`insta` 登录过期） | **待确认** |

### 为什么当前池子起不来

容器直连单一云出口 IP，Cloudflare 反复给同一小批 `/64`：

```
2a09:bac1:76e1:62b0::/64
2a09:bac1:7681:62b0::/64
2a09:bac1:76a1:62b0::/64
2a09:bac1:76c1:62b0::/64
2a09:bac5:c82a:188c::/64
...
```

`pool_size=16` 想要 16 个不同出口，但源只给得出约 10 个，剩下的被 `errDuplicateEgress` 丢掉；连续 3 次重复触发 1 分钟退避，重复攒到阈值后 preload 暂停 5 分钟（日志 `preload paused for 5m0s after N duplicate egresses`）。`healthz` 于是长期是 `active=1, cooling=99`。

这不是 IPv6 握手失败：日志里 `egress ready at 2a09:...` 说明注册和隧道都成功，卡的是**出口 `/64` 多样性**。

---

## 2. 执行前检查（只读，先做）

### 2.1 确认 `insta` 登录

```powershell
insta status
```

必须看到 `user: <你的账号>`。如果是 `(not logged in)`，先 `insta login`。本文所有 `insta` 命令都通过：

```powershell
$insta = "$env:USERPROFILE\.local\tools\node\insta.cmd"
```

### 2.2 确认线上镜像含 farm 分支（关键）

```powershell
& $insta compute exec opencode2api-2 -- sh -c "grep -c warp-farm /usr/local/bin/proxyctl; grep -a -c registration_proxies /usr/local/bin/opencode2api-app"
```

期望两个数字都 **≥ 1**：

- `proxyctl` 里出现 `warp-farm` → 镜像含 farm 开关
- `opencode2api-app` 里出现 `registration_proxies` → 二进制支持该字段

如果任一为 `0`，**不要创建 marker**：当前镜像太旧，需要从 `warp-stability` 最新 commit 重新构建/部署后再执行。否则 marker 会被忽略，`proxyctl` 走非 farm 分支，把 `warp.enabled` 重新写回 `false`。

### 2.3 确认代理池出口

```powershell
& $insta compute exec opencode2api-2 -- sh -c "proxyctl status; jq '.warp' /var/lib/opencode2api/config.json"
```

期望：

```
MIHOMO_PID=<pid>
MIHOMO_ALIVE=1
LISTENERS=202
PORT_BASE=21000
```

```json
{
  "enabled": false,
  "pool_size": 16,
  "stack": "ipv6"
}
```

### 2.4 出口 IP 普查（决定 `pool_size` 上限）

```powershell
& $insta compute exec opencode2api-2 -- sh -c "seq 21000 21201 | xargs -P 32 -I{} sh -c 'curl -s --max-time 6 -x socks5h://127.0.0.1:{} https://www.cloudflare.com/cdn-cgi/trace | grep ^ip=' > /tmp/egress.txt; echo -n 'alive='; wc -l < /tmp/egress.txt; echo -n 'distinct='; sort -u /tmp/egress.txt | wc -l"
```

本次实测：

```
alive=181
distinct=59
```

`pool_size` 必须 **≤ distinct**，且代码硬上限是 32。建议：

| 目标 | `pool_size` | 说明 |
|---|---|---|
| 保守起步 | **16** | 当前值，先验证 farm 模式能稳定到 16 |
| 提高并发 | **24** | 观察 `cooling` 是否仍然很小 |
| 上限 | **32** | 代码 `maxPreload=32`，再大没有意义 |
| 不要 | > distinct | 只会制造 duplicate + preload 暂停 |

> 59 个出口 IP 不一定映射到 59 个不同 `/64`。Cloudflare 可能把相邻 IP 分到同一个 `/64`。所以先从 16 开始，按实测 `active` 再往上加。

---

## 3. 参数与边界

```json
"warp": {
  "enabled": true,
  "pool_size": 16,
  "stack": "ipv6",
  "source_ipv6": "",
  "registration_proxies": [
    "socks5://127.0.0.1:21000",
    "socks5://127.0.0.1:21001"
  ]
}
```

- `stack` 必须是 `"ipv6"`：出口 `/64` 才有意义。
- `source_ipv6` **留空**：容器没有 routed `/64`，填了 `IPV6_FREEBIND` 能绑上但回包回不来，表现为 `rx=0`。
- **不要加 `tun`**：Linux 构建用 `DisallowUnknownFields`，未知字段直接拒绝启动。`tun` 是 Windows 分支的字段。
- `registration_proxies` 只影响注册 HTTPS；WireGuard 数据面仍从容器 `eth0` 直出。
- 代理 URL 支持 `http` / `https` / `socks5`；`Normalize` 会 trim、去重、校验 scheme 和 host。
- 每个代理独立暂停：传输失败停 20s，Cloudflare 429 按 `Retry-After`（默认 12s，最大 5m）。
- `direct` 永远排在最后兜底；代理全挂时仍会尝试直连注册（但拿不到新 `/64`）。

---

## 4. 变更步骤

### 4.1 备份

在容器内（`ssh opencode2api-2.insta` 之后，或通过 `insta compute exec` 包一层）：

```sh
cd /var/lib/opencode2api
cp -a config.json config.json.bak-farm
cp -a config.json.warp-state.json config.json.warp-state.json.bak-farm 2>/dev/null || true
ls -l config.json.bak-farm config.json.warp-state.json.bak-farm 2>/dev/null
```

PowerShell 一条条执行版：

```powershell
$insta = "$env:USERPROFILE\.local\tools\node\insta.cmd"
& $insta compute exec opencode2api-2 -- sh -c "cd /var/lib/opencode2api && cp -a config.json config.json.bak-farm && cp -a config.json.warp-state.json config.json.warp-state.json.bak-farm 2>/dev/null || true"
```

> 固定后缀会覆盖上一次 `.bak-farm`。要保留多版本，把后缀换成 `$(date +%Y%m%d-%H%M%S)`，并在 PowerShell 里用反引号转义 `$`。

### 4.2 确认 `pool_size` / `stack`（可选）

当前配置已经是 `16 / ipv6 / 无 source_ipv6`，可以跳过。要改就执行：

```sh
cd /var/lib/opencode2api
jq '.warp.pool_size = 16
    | .warp.stack = "ipv6"
    | del(.warp.source_ipv6)
    | del(.warp.tun)' config.json > config.json.tmp && mv config.json.tmp config.json
jq '.warp' config.json
```

> `del(.warp.tun)` 是防御性写法：万一从 Windows 配置复制过 `tun`，Linux 构建会拒绝启动。

### 4.3 打开 farm 开关

```sh
touch /var/lib/opencode2api/warp-farm.txt
ls -l /var/lib/opencode2api/warp-farm.txt
```

PowerShell：

```powershell
& $insta compute exec opencode2api-2 -- sh -c "touch /var/lib/opencode2api/warp-farm.txt && ls -l /var/lib/opencode2api/warp-farm.txt"
```

`proxyctl` 的 farm 分支就是这一段（代码依据）：

```sh
if [ -f "$state_dir/warp-farm.txt" ]; then
  jq --rawfile pf "$work_dir/proxies.txt" \
    '.warp.registration_proxies = ($pf | split("\n") | map(select(length > 0) | split("#")[0]))
     | .warp.enabled = true
     | .proxies = []
     | .proxyfile = ""' \
    "$config_path" > "$config_path.tmp" && mv "$config_path.tmp" "$config_path"
  return 0
fi
```

注意它**不修改** `pool_size`、`stack`、`source_ipv6`，所以 4.2 要先做。

### 4.4 重启

```powershell
& $insta compute restart opencode2api-2
```

启动顺序：

1. `opencode2api-wrapper.sh` 检测到 `proxycenter/providers.txt` 或 `merged.json`，执行 `proxyctl start`
2. `proxyctl` 从缓存重建 fleet、启动 mihomo、等待端口
3. `proxyctl` 写 `config.json`（farm 分支）
4. `exec opencode2api-app`，应用读取新配置并开始注册

> 不需要手动 `proxyctl apply`。重启时 wrapper 会自动跑。若你只想热应用而不重启，`POST /api/config/reload` 可以重读配置，但它不会重建 fleet，也不如重启干净。

---

## 5. 验证

### 5.1 配置已改写

```powershell
& $insta compute exec opencode2api-2 -- sh -c "jq '.warp | {enabled, pool_size, stack, source_ipv6, n: (.registration_proxies | length), first: .registration_proxies[0], last: .registration_proxies[-1]}' /var/lib/opencode2api/config.json"
```

期望：

```json
{
  "enabled": true,
  "pool_size": 16,
  "stack": "ipv6",
  "source_ipv6": null,
  "n": 202,
  "first": "socks5://127.0.0.1:21000",
  "last": "socks5://127.0.0.1:21201"
}
```

同时确认数据面已被清空：

```powershell
& $insta compute exec opencode2api-2 -- sh -c "jq '{proxies, proxyfile}' /var/lib/opencode2api/config.json"
```

期望 `{"proxies":[],"proxyfile":""}`。

### 5.2 healthz

```powershell
& $insta compute exec opencode2api-2 -- sh -c "wget -qO- http://127.0.0.1:8081/healthz"
```

期望 `warp` 部分：

- `status: "ready"`
- `stack: "ipv6"`
- `size: 16`
- `active` 逐步升到 16（启动时先 1，再后台补到 preload）
- `cooling` 很小（个位数），不再长期 99

> `preload` 由 `size` 推导；旧镜像可能显示 12，新代码是 `min(size, 32)`。以 `active` 是否接近 `size` 为准。

### 5.3 日志

```powershell
& $insta compute logs opencode2api-2 --since 10m --limit 500 2>&1 | Select-String -Pattern 'WARP|warp|duplicate|registration|egress|paused'
```

成功信号：

- 多条 `egress ready at 2a09:...`，且 `/64` 前缀各不相同
- 没有连续的 `Discarding duplicate WARP exit`
- 没有 `preload paused for 5m0s after N duplicate egresses`
- 没有 `registration paused for 1m0s` 的密集刷屏

失败信号与处理：

| 日志 | 含义 | 处理 |
|---|---|---|
| `Discarding duplicate ... /64` 连续出现 | 新代理仍映射到旧 `/64` | 降低 `pool_size`，或换更分散的代理源 |
| `preload paused ... duplicate egresses` | 池子补货连续拿重复 | 同上；不要加大 `pool_size` |
| `registration paused for 1m0s` 频繁 | 多数代理同时处于暂停窗口，`acquire` 返回最短等待 | 等 1 分钟；代理池越大越分散 |
| `first registration paused` 后启动失败 | 前几个代理都不可用 | 查 `proxyctl status`、`curl -x` 抽测；必要时 `proxyctl sync` |
| `WARP handshake` / `verify WARP ... egress` 失败 | 数据面 UDP 2408 被云厂商限制 | 注册代理解决不了；查 `warpprobe`，考虑换端口/数据面也走代理 |

### 5.4 真实请求

```powershell
& $insta compute exec opencode2api-2 -- sh -c "wget -qO- --header='Authorization: Bearer TOKEN' http://127.0.0.1:8081/v1/models | head -c 300"
```

拿到模型列表后，在容器内发一条免费模型请求（`ssh opencode2api-2.insta` 之后；JSON 双引号用 sh 转义）：

```sh
TOKEN='TOKEN'
MODEL='MODEL'
curl -sS http://127.0.0.1:8081/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"max_tokens\":8}"
```

成功：HTTP 200 + 正常 completion。若 429，先看日志是否触发了 session 轮换（第一次 429 会换 session 重试同一出口，第二次才退休该出口）。

### 5.5 稳定性观察（30 分钟）

每 5 分钟抓一次 healthz，记录 `active / standby / cooling`：

```powershell
1..6 | ForEach-Object {
  & $insta compute exec opencode2api-2 -- sh -c "wget -qO- http://127.0.0.1:8081/healthz" 2>&1 |
    Select-String -Pattern '"warp"'
  Start-Sleep -Seconds 300
}
```

判定：

- `active` 稳定在 12-16，`cooling` 个位数 → 成功
- `active` 停在 4-8，`cooling` 缓慢上升 → 实际 `/64` 供给就是这么多，把 `pool_size` 降到 `active + 2`
- `active` 掉到 1，日志重复 → 代理出口太集中，换源或降 `pool_size`

---

## 6. 回滚

### 6.1 最快回滚（推荐）

```sh
rm /var/lib/opencode2api/warp-farm.txt
```

然后重启。`proxyctl` 走非 farm 分支，自动写回：

```json
{
  "warp": { "enabled": false },
  "proxies": [],
  "proxyfile": "/var/lib/opencode2api/cfnew/proxies.txt"
}
```

即恢复到当前的“代理数据面 + WARP 关闭”状态。

```powershell
& $insta compute exec opencode2api-2 -- sh -c "rm -f /var/lib/opencode2api/warp-farm.txt"
& $insta compute restart opencode2api-2
```

### 6.2 完整回滚（含配置）

```sh
cd /var/lib/opencode2api
cp -a config.json.bak-farm config.json
cp -a config.json.warp-state.json.bak-farm config.json.warp-state.json 2>/dev/null || true
rm -f warp-farm.txt
```

然后重启。

### 6.3 启动失败时

`insta compute restart` 在服务起不来时会 best-effort 回滚到上一版配置。若没有自动回滚：

```powershell
& $insta compute restart opencode2api-2
```

仍失败就执行 6.2，再重启。不要留着 marker 反复重启——每次都会重新走 farm 分支。

---

## 7. 风险与注意事项

### 7.1 WebUI 保存会丢 `registration_proxies`

`internal/admin/server.go` 的 `ConfigUpdate.Warp` 是 `config.WarpConfig`，而 `webui/js/pages/config.js` **没有渲染** `registration_proxies`。在 WebUI 点保存时，前端提交的 `warp` 对象不含该字段，后端会把候选配置里的列表清空并持久化。

后果：

- 保存后运行中的池子会在下一次 rebuild 时退回 direct 注册
- 下次重启时 `proxyctl` 会重新写入（farm marker 还在），所以重启可恢复
- 但保存到重启之间，池子可能已经开始拿重复 `/64`

处理：

- 改配置优先用 `config.json` + 重启，不用 WebUI 保存
- 如果必须用 WebUI，保存后立刻执行一次 `proxyctl apply` + 重启（或直接重启）
- 长期方案：给 `config.js` 补一个只读展示/编辑框，或给 `ConfigUpdate` 做 merge 而不是整体替换

### 7.2 第一次注册是同步的

`newPool` 里第一个 exit 同步注册并 gate readiness；失败会 fail closed。`createExit` 最多试 4 次，但只在“重复 egress”时重试，代理传输失败会直接返回错误。

当前 181/202 存活，前 4 个全挂的概率约 `0.1^4 ≈ 0.0001`，风险很低。若真的启动失败：

1. `proxyctl status` 看 mihomo 是否活着
2. 抽测几个端口：`curl -x socks5h://127.0.0.1:21000 https://www.cloudflare.com/cdn-cgi/trace`
3. `proxyctl sync` 刷新订阅后重启
4. 仍失败就临时把 `registration_proxies` 缩到存活子集（但注意 `proxyctl` 下次启动会重写）

### 7.3 每个 `/64` 的额度仍然有限

代码注释写得很直白：`Each exit carries exactly one free request while its /64 is fresh`。`registration_proxies` 只是让你**能 mint 更多不同的 `/64`**，不是让单个 `/64` 变成无限额度。

- 16 个出口 ≈ 16 次“新鲜”免费请求，之后进入 24h 冷却
- 第一次 429 会换 session 重试同一出口；第二次 429 才退休该出口
- 想要更高吞吐，只能继续扩源 IP，不能靠调大 `pool_size`

### 7.4 代理池是单点

mihomo（PID48）由 wrapper 启动；容器重启会重建。运行中如果 mihomo 挂了：

- 注册会逐个代理失败（每个停 20s），最后 fallback 到 direct
- 池子不会崩，但拿不到新 `/64`，`active` 会慢慢掉
- `proxyctl status` 的 `MIHOMO_ALIVE` 是第一个要看的指标

### 7.5 `warp-state.json` 先保留

它记录已消费的 `/64`，重启后不会重复注册。farm 模式拿到的 `/64` 通常是新的，保留没有坏处。

只有一种情况考虑清空：日志显示新注册的 `/64` **全部**落在旧 cooling 集合里，`active` 一直起不来。这时可以备份后清空做一次干净测试：

```sh
cd /var/lib/opencode2api
mv config.json.warp-state.json config.json.warp-state.json.spent-$(date +%Y%m%d-%H%M%S)
```

但清空不会让已经限速的 `/64` 变可用，只是让池子重新试一遍。根本解决仍然是更多样的源 IP。

### 7.6 数据面依赖 WARP

farm 模式会把 `proxies` 和 `proxyfile` 清空。这意味着：

- 免费模型请求全部走 WARP 出口
- WARP 不可用时，免费链路直接失败（不会自动回退到代理数据面）
- 带 key 的 zen/go 链路不受影响

如果免费链路是主业务，建议保留 6.1 的回滚路径，并监控 `healthz.warp.status`。

---

## 8. 代码依据

| 文件 | 关键点 |
|---|---|
| `internal/config/config.go` | `WarpConfig.RegistrationProxies`；`Normalize` 校验 scheme/host、去重、`pool_size` 1-32、`source_ipv6` 需 ipv6 且 /64 或更短 |
| `internal/warp/pool.go` | `Options.RegisterProxies`；`NewWithOptions`；`maxPreload=32`；`preloadFor(size)=min(size,32)`；`createExit` 4 次重试；`errDuplicateEgress`；`refillFailureThreshold=3`；`refillBackoffBase=1m`；`refillBackoffMax=5m`；`egressCooldown=24h` |
| `internal/warp/registrar.go` | 每个代理一个 `http.Client`；`acquire()` 轮询、跳过暂停中的、direct 最后；`registrationProxyFailurePause=20s`；`defaultRateLimitPause=12s`；`maxRateLimitPause=5m` |
| `internal/gateway/runtime.go` | `selectWARP` key 包含 `registrationProxies`；首次注册超时至少 `RegistrationWindow+30s`；`warpStatePath = configPath + ".warp-state.json"` |
| `internal/gateway/warp.go` | `defer429`：第一次 429 换 session 重试同一出口，第二次 429 退休出口 |
| `internal/admin/server.go` | `ConfigUpdate.Warp` 是整体 `config.WarpConfig`，WebUI 未提交的字段会被清空 |
| `opencode2api-wrapper.sh` | 启动时先 `proxyctl start`，再 `exec opencode2api-app` |
| `proxyctl` | `warp-farm.txt` marker；farm 分支写 `registration_proxies`、`enabled=true`、`proxies=[]`、`proxyfile=""`；非 farm 分支写 `enabled=false`、`proxyfile=cfnew/proxies.txt` |

---

## 9. Checklist

- [ ] `insta status` 显示已登录
- [ ] `proxyctl` 含 `warp-farm`，app 含 `registration_proxies`（第 2.2 节）
- [ ] `proxyctl status` 显示 `MIHOMO_ALIVE=1`、`LISTENERS=202`
- [ ] 出口普查：`distinct` ≥ 想要的 `pool_size`，且 `pool_size` ≤ 32
- [ ] 备份 `config.json` 和 `config.json.warp-state.json`
- [ ] 确认 `pool_size` / `stack=ipv6` / 无 `source_ipv6` / 无 `tun`
- [ ] `touch /var/lib/opencode2api/warp-farm.txt`
- [ ] 重启 `opencode2api-2`
- [ ] `jq '.warp'` 显示 `enabled=true`、`registration_proxies` 非空、`proxies=[]`、`proxyfile=""`
- [ ] healthz `warp.status=ready`，`active` 接近 `size`，`cooling` 很小
- [ ] 日志无连续 duplicate / 无 preload pause
- [ ] 真实免费模型请求 200
- [ ] 30 分钟稳定性观察通过
- [ ] 回滚路径已确认（删 marker + 重启）
