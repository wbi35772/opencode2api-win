# opencode2api Windows 独立测试版

这个目录是主仓库的独立副本，源码、WebUI 资源和 Windows 二进制都在这里，修改不会影响上一级目录。

完整文档在 [`doc/README.md`](doc/README.md)，下面只放最常用的部分。

- 构建环境：Windows 11 x64，Go 1.24+（实际使用 Go 1.27.3），`CGO_ENABLED=0`
- 二进制：`opencode2api.exe`（约 20 MB，静态构建，不需要额外 DLL）
- 配置：`config.json`，首次启动已把 WebUI 密码转成 Argon2id 哈希
- 默认只监听本机回环地址，没有对局域网开放

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

`ready: true` 表示 API 已经可以接请求；如果 `status` 是 `degraded`，通常只是模型目录还在刷新，等几秒会变成 `ok`。

如果免费额度耗尽（当前直连就是这种情况），健康检查可能显示 `ready: false`、`no_healthy_proxies`。这表示免费通道不可用，不代表进程没启动；WebUI 和带 Key 的 API 仍然可以访问。启动脚本不会因为 `ready: false` 而判定启动失败。

## 启动和停止

前台运行（关闭窗口即停止）：

```powershell
.\start-opencode2api.ps1
```

或者双击：

```text
start-opencode2api.bat
```

后台运行（日志写到 `logs\`，PID 写到 `logs\pid.txt`）：

```powershell
.\start-background.ps1
```

停止后台进程：

```powershell
.\stop-opencode2api.ps1
```

也可以直接结束进程：

```powershell
Get-Process opencode2api | Stop-Process -Force
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

也可以用 OpenAI SDK 直接指向：

```text
OPENAI_BASE_URL=http://127.0.0.1:28080/v1
OPENAI_API_KEY=sk-win-local-test
```

## 当前状态说明

当前配置是 **WARP 开启、`proxies` 为 `direct`**，实测通过：

- WARP 池 18 秒就绪，`pool_size: 4`，2 个 active + 2 个 standby
- 连续 8 次免费请求全部 `200`
- 线上连打 5 次：`200 2.61s / 2.93s / 2.72s / 7.90s / 2.77s`
- `/healthz` 返回 `status: ok`、`models.stale: false`、`warp.status: ready`
- 带 Key 的 `/v1/models` 返回 8 个免费模型

WARP 在进程内用 netstack 完成，本机不会新增网卡、不会改默认路由，**只有网关进程自己走 WARP**；OpenCode 原生客户端直连 opencode.ai 时用的是你本机的真实 IP，两者额度互不影响。细节见 [`doc/03-网络-WARP-与出口.md`](doc/03-网络-WARP-与出口.md)。

### 如果 WARP 起不来

启动时最多等 60 秒，拿不到出口就以"WARP 未启用"继续跑，28080/28081 照常监听，随后在后台按 15s→30s→1m→2m→5m 自动重试，成功后热切换回 WARP，不需要重启。日志里是 `warp_startup_fallback`，恢复时是 `WARP egress recovered in the background`。

判断 WARP 能不能用的标准是**本机能否连上 `api.cloudflareclient.com`（注册）以及到 WARP endpoint 的 UDP 2408**。日志里那个 `1.1.1.1/cdn-cgi/trace` 是在隧道内部访问的，本机直连连不上 1.1.1.1 并不影响 WARP——实测本机 1.1.1.1 一直超时的那段时间 WARP 照样 8/8 通。

**TUN 模式已经不影响 WARP。** 注册用的 HTTPS 和 WireGuard 的 UDP 都钉在物理网卡上，开着 sing-box / v2rayN 的 TUN 实测串行 8/8、并发 6/6 全通，期间还能注册出新出口。只有"物理网卡到不了 Cloudflare、只有 TUN 能到"这种反过来的情况，才需要 `set OPENCODE2API_WARP_NO_PIN=1` 启动。

如果不想用 WARP：

```json
"warp": { "enabled": false },
"proxies": ["direct"]
```

注意：WARP 和 `proxies` 在免费模型上是二选一的关系；一个代理就是一个出口，opencode2api 不会对外部代理做 `/64` 去重。

### 出口怎么选

| 方案 | 成功率实测 | 平均延迟 | 依赖 |
|---|---|---|---|
| WARP（当前） | 8/8、5/5；TUN 下 8/8 + 6/6 | 约 3.5s | 本机能到 Cloudflare |
| 直连 `direct` | 连接 20/20 | 约 1.3s | 免费额度按本机 IP，很快就 429 |
| 本地代理 `10808` | **9/20** | 约 1.5s | v2rayN 必须在线，且会丢一半连接 |

## 修改配置

`config.json` 常用字段：

```json
{
  "listen": "127.0.0.1:28080",
  "server_keys": ["sk-win-local-test"],
  "anonymous": true,
  "warp": {
    "enabled": true,
    "pool_size": 4,
    "stack": "ipv6",
    "source_ipv6": ""
  },
  "proxies": ["direct"],
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
- 不要把 `config.json` 和 `logs/` 提交到公开仓库。

## 重新构建

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o opencode2api.exe ./cmd/opencode2api
```

只需要 Go 1.24+；项目没有 cgo 依赖。构建完成后重启进程即可。

## 目录

```text
win/
  cmd/                 入口
  internal/            网关、协议、模型、WARP 等
  webui/               管理界面（编译时嵌入）
  assets/              静态资源
  config.json          当前运行配置
  config.example.json  原始模板
  opencode2api.exe     已构建的 Windows 二进制
  start-opencode2api.ps1
  start-opencode2api.bat
  start-background.ps1
  stop-opencode2api.ps1
  doc/                 完整文档
  logs/                运行日志和 PID
```

## 注意

- 这是独立副本，不是 Git 工作树；不要在主仓库根目录和 `win/` 里同时修改同一份源码。
- 同一份 `config.json` 不要同时启动两个进程，缓存、轮换状态和端口会冲突。
- 免费模型额度按出口 IP/网段计算，429 不一定是配置错误；换出口或换代理后再判断。
