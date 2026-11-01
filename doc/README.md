# opencode2api Windows 部署文档

这套文档描述 `win/` 目录这份独立部署：它在主仓库源码之上单独复制了一份代码和二进制，改这里不会影响仓库根目录。

## 文档索引

| 文件 | 内容 |
|---|---|
| [01-部署与运行.md](01-部署与运行.md) | 环境要求、构建、目录结构、启动停止、端口与凭据 |
| [02-配置参考.md](02-配置参考.md) | `config.json` 每个字段的含义和当前取值 |
| [03-网络-WARP-与出口.md](03-网络-WARP-与出口.md) | WARP 原理、实测数据、与代理/TUN 的关系、出口 IP 规则 |
| [04-接入OpenCode.md](04-接入OpenCode.md) | 在 OpenCode 里注册 `win` 模型组、模型清单、与官方组的区别 |
| [05-故障排查.md](05-故障排查.md) | 症状 → 原因 → 处理，按现象索引 |

## 一分钟速查

| 项目 | 值 |
|---|---|
| API Base | `http://127.0.0.1:28080/v1` |
| WebUI | `http://127.0.0.1:28081` |
| API Key | `sk-win-local-test` |
| WebUI 账号 | `admin` / `win-admin-2026` |
| 后台启动 | `.\start-background.ps1` |
| 后台停止 | `.\stop-opencode2api.ps1` |
| 健康检查 | `curl.exe --noproxy "*" http://127.0.0.1:28080/healthz` |
| 日志 | `logs\stdout.log`、`logs\stderr.log`、`logs\pid.txt` |

## 当前运行状态（2026-10-29 实测）

```json
{"status":"ok","ready":true,
 "models":{"status":"ready","total":80,"exposed":8,"stale":false},
 "proxies":{"total":1,"healthy":1},
 "warp":{"status":"ready","stack":"ipv6","size":4,"active":2,"standby":2,"cooling":0}}
```

- WARP 已开启，`pool_size=4`，4 个出口全部预热（最多预热 8 个）
- 连续 5 次免费模型对话全部 200，耗时 2.6–7.9 秒
- 模型目录在线刷新成功，`degraded` 告警已消失

## 目录约定

```text
win/
  cmd/ internal/ webui/ assets/   源码（已复制，可独立构建）
  config.json                     当前运行配置（不要提交）
  config.example.json             原始模板
  opencode2api.exe                已构建二进制
  README-WIN.md                   快速上手
  README-WIN 的细节都收进 doc/ 了
  doc/                            本文档目录
  start-opencode2api.ps1          前台启动
  start-opencode2api.bat          双击启动
  start-supervised.ps1            前台启动的 Job Object 监督器（关闭窗口即整体回收）
  start-background.ps1            后台启动（写 pid 和日志）
  stop-opencode2api.ps1           停止（两种模式都适用，含边缘代理）
  logs/                           运行日志
```

> `win/` 是源码副本，不是 Git worktree。不要在主仓库根目录和 `win/` 里同时改同一份源码。
