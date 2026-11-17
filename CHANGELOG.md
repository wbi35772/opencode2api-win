# Changelog

本仓库是 `opencode2api` 的 Windows 独立分支。版本号格式：`上游版本-win.N`。

## v1.4.8-win.1 — 2026-11-17

### 对齐上游 v1.4.8

- `#182` 客户端工具名从 `clint_` 改为 `mcp_`；修复真实会话带 tools 时被上游免费层直接 429 的问题。
- 免费层重试硬化：保留最近上游 HTTP 错误、取消早退、响应体 `DrainAndClose`、WARP 租约不泄漏。
- title 请求预判、单次尝试、429/传输失败本地合成回退；标题请求不注入官方工具。
- 新增 `upstream.prompt_environment`（工作目录、workspace root、平台、git 标记）。
- legacy `functions` 只重命名一次；相邻 assistant 消息合并为一轮。
- Playground 请求固定到一个 relay（`#167`）。
- Go Device OAuth 订阅登录（`#168`）：本地为配置占位，未实现完整 OAuth 流程。
- 新增 `proxy_bindings`、`go_subscription`、`models.active_probes` 配置与 WebUI 保留逻辑。

### 免费道修复

- 旧 exe 的 `clint_` 前缀是 429 的第一层原因；已部署 v1.4.8 构建。
- 重置已耗尽的 `warp-state.json`，重新注册全新 WARP 出口。
- `warp.pool_size` 16→32、`max_anonymous_exits` 0→32、`free_tier_block_seconds` 0→120。
- 实测：极简 200/28.6s、带 tools JSON 200/35.1s、带 tools SSE 200/8.0s；部署后极简 200/29.1s。

### 文档

- 新增 `doc/12-版本基线-v1.4.8-win.1.md`。
- 更新 `BASELINE.md`、`README-WIN.md`、`doc/README.md`、`doc/02`、`doc/05`、`doc/07`。
- 新增 Canvas 基线画布「opencode2api-win 基线总览 · 2026-11-17（v1.4.8-win.1）」。

### 已知限制

- WARP 依赖本机 Clash 7897 的 TCP + UDP。
- 免费额度按 `project × 出口` 计算，不是无限。
- 当前发布来自未提交工作区；下次发布前先提交并打 tag。
- 未移植 v1.4.8 之后的 `0d05fe93`。

## v1.4.6-win — 2026-11-01

- 合并上游 v1.4.6（`58f5667`，合并提交 `a93d805`）。
- 保留 Windows 侧 WARP 改动：物理网卡绑定、启动失败后台重试、池/注册器加固。
- 基础设施：后台启动脚本、WebUI、WARP SOCKS5 UDP 数据路径、基线文档。
