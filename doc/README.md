# opencode2api Windows 部署文档

这套文档描述 `oc2-win/` 这份 Windows 独立部署。它在主仓库源码之上单独复制了一份代码和二进制，改这里不会影响仓库根目录。

当前基线是 **v1.5.1-win.1**（上游 `v1.5.1` / `6575c62`，2026-11-21）。完整发布说明、验证矩阵、配置快照、回滚步骤和维护清单见 [12-版本基线-v1.5.1-win.1.md](12-版本基线-v1.5.1-win.1.md)；版本历史见根目录 `CHANGELOG.md`。

## 文档索引

| 文件 | 内容 |
|---|---|
| [01-部署与运行.md](01-部署与运行.md) | 环境要求、构建、目录结构、启动停止、端口与凭据 |
| [02-配置参考.md](02-配置参考.md) | `config.json` 每个字段的含义和当前取值 |
| [03-网络-WARP-与出口.md](03-网络-WARP-与出口.md) | WARP 原理、实测数据、与代理/TUN 的关系、出口 IP 规则 |
| [04-接入OpenCode.md](04-接入OpenCode.md) | 在 OpenCode 里注册 `win` 模型组、模型清单、与官方组的区别 |
| [05-故障排查.md](05-故障排查.md) | 症状 → 原因 → 处理，按现象索引 |
| [06-架构说明.md](06-架构说明.md) | 代码分层、请求路径、WARP 池、进程生命周期、扩展点 |
| [07-已知问题与限制.md](07-已知问题与限制.md) | 已修/未修问题、环境约束、如何确认问题是否还在 |
| [08-备份与恢复.md](08-备份与恢复.md) | 仓库拓扑、入库范围、换机恢复、Release 资产、完整性检查 |
| [09-WARP-SOCKS5-UDP-基线.md](09-WARP-SOCKS5-UDP-基线.md) | 当前部署基线、SOCKS5 UDP 数据路径、验证记录、换节点与回滚 |
| [10-线路切换与WARP排障记录.md](10-线路切换与WARP排障记录.md) | 2026-11-07 排障记录、参数体检、换线后长超时 -1 的排查手册、cfnew/UDP 结论 |
| [11-进度存档-2026-11-10.md](11-进度存档-2026-11-10.md) | 历史交接：Clash 线路修复与 429 复核 |
| [12-版本基线-v1.5.1-win.1.md](12-版本基线-v1.5.1-win.1.md) | **当前版本基线**：上游完整对齐、变更、证据、配置、部署/回滚、维护清单 |

## 一分钟速查

| 项目 | 值 |
|---|---|
| 版本 | `v1.5.1-win.1` |
| 二进制 | `opencode2api.exe`，SHA-256 `6D614EFC858E791EAFC8B4C3163CB8423730A19C1A2376818BC0AA1DC8F44812` |
| API Base | `http://127.0.0.1:28080/v1` |
| WebUI | `http://127.0.0.1:28081` |
| API Key | `sk-win-local-test` |
| WebUI 账号 | `admin` / `win-admin-2026` |
| 后台启动 | `.\start-background.ps1` |
| 后台停止 | `.\stop-opencode2api.ps1` |
| 健康检查 | `curl.exe --noproxy "*" http://127.0.0.1:28080/healthz` |
| 基线画布 | Canvas `06GZR0P1MCHNSNZT4RXFX97XJV-opencode2api-win-v1-5-1-win-1` |
| 发布包 | `release/opencode2api-v1.5.1-win.1.zip`，SHA-256 `41018C0E2801E5BFEE8F9B9B5A6C461D47C179F57C674B007A87252F91C120C1` |
| 日志 | `logs\win3-out.log`、`logs\win3-err.log`（本次）；常规 `logs\stdout.log`、`logs\pid.txt` |

## 当前运行状态（2026-11-21 实测）

```json
{"status":"ok","ready":true,"version":"v1.5.1-win.1",
 "models":{"status":"ready","total":103,"exposed":11},
 "keys":{"zen":0,"go":0,"total":0,"anonymous":true},
 "proxies":{"total":1,"healthy":1,"unhealthy":0},
 "warp":{"status":"ready","stack":"ipv6","size":32,"preload":12,
         "active":5,"standby":5,"cooling":62}}
```

- 当前只用匿名免费道（`zen_keys`、`go_keys` 为空），不依赖付费账号 Key；OAuth 代码存在但未配置。
- `warp.pool_size=32`，`preload=12`；`cooling` 是历史 `/64` 冷却记录，不代表当前不可用。
- 免费配额按 **(project, 出口 /64)** 计：一个成功请求会消耗一个出口，池子约每 30–40 秒补一个；`active=0` 且 `cooling` 很多时等池子回血。
- WireGuard 外层 UDP 走 `socks_udp`（`socks5://127.0.0.1:10808`）中继；注册 HTTPS 走 `proxies[0]`。
- 免费道实测：`hy4-flash-free` 非流式 `200/16.8s`；流式 + `tool_choice=required` `200/50.0s`，工具调用与 `[DONE]` 正常。
- 旧 `config.json.models.catalog.json`（schema v3）会被本版拒绝并重新拉取，属正常升级；完整排障见 [05](05-故障排查.md) 与 [12](12-版本基线-v1.5.1-win.1.md)。

## 目录约定

```text
oc2-win/
  cmd/ internal/ webui/ assets/   源码（已复制，可独立构建）
  config.json                     当前运行配置（不要提交）
  config.example.json             原始模板
  opencode2api.exe                已构建二进制（v1.5.1-win.1）
  README-WIN.md                   快速上手
  doc/                            本文档目录
  docs/                           管理 API 与 Linux WARP 农场
  release/                        发布产物（gitignored）
  start-opencode2api.ps1          前台启动
  start-opencode2api.bat          双击启动
  start-supervised.ps1            前台启动的 Job Object 监督器（关闭窗口即整体回收）
  start-background.ps1            后台启动（写 pid 和日志）
  stop-opencode2api.ps1           停止（两种模式都适用，含边缘代理）
  logs/                           运行日志
```

> `oc2-win/` 是源码副本，不是 Git worktree。不要在主仓库根目录和 `oc2-win/` 里同时改同一份源码。
