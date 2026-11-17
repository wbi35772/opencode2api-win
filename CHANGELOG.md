# Changelog

本仓库是 `opencode2api` 的 Windows 独立分支。版本号格式：`上游版本-win.N`。

## v1.4.8-win.2 — 2026-11-17

### 补齐 v1.4.6→v1.4.7 区间遗漏

- `#166` 官方工具名与流式收尾：`official` 别名表（`BindOfficial`/`Official`）、`OfficialToolCandidates`/`bindOfficialToolAliases`、未决工具名的参数扣留与 `finish()` 补帧、`finish_reason` 收尾；修复幽灵调用与末帧丢失。
- `#171` 被动可用性：`RecordPassiveObservation` 按真实请求记账（仅 400/404/410/422 读 body）；`models.active_probes` 成为显式开关（默认关闭；本机运行配置也保持关闭，只用被动记账）。
- `#153` 代理健康探测改为 `https://opencode.ai/`，并使用 Bun TLS 指纹客户端，避免第三方探测误杀可用出口。
- WebUI 配置页新增「启用免费模型后台主动探测」（中/英/越/意）。

### 修正

- `#167` Playground relay 固定从未进入本仓库：win.1 的移植清单与发布说明表述有误；本版仍未包含（只影响诊断页，不影响请求路径）。
- 构建参数补充 `-s -w` 以减小体积：`-trimpath -ldflags "-s -w -X main.version=v1.4.8-win.2"`。

### 验证

- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿。
- 部署后实测：极简、带 tools 非流式、带 tools 流式与工具调用回环，见 `BASELINE.md` 验证矩阵。

## v1.4.8-win.1 — 2026-11-17

### 对齐上游 v1.4.8

- `#182` 客户端工具名从 `clint_` 改为 `mcp_`；修复真实会话带 tools 时被上游免费层直接 429 的问题。
- 免费层重试硬化：保留最近上游 HTTP 错误、取消早退、响应体 `DrainAndClose`、WARP 租约不泄漏。
- title 请求预判、单次尝试、429/传输失败本地合成回退；标题请求不注入官方工具。
- 新增 `upstream.prompt_environment`（工作目录、workspace root、平台、git 标记）。
- legacy `functions` 只重命名一次；相邻 assistant 消息合并为一轮。
- Playground 固定 relay（`#167`）：**未移植**（win.2 修正：该提交从未进入本仓库）。
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
- 发布提交 `71619a8`，tag `v1.4.8-win.1`；二进制与发布包从该 tag 可复现。
- 未移植 v1.4.8 之后的 `0d05fe93`。

## v1.4.6-win — 2026-11-01

- 合并上游 v1.4.6（`58f5667`，合并提交 `a93d805`）。
- 保留 Windows 侧 WARP 改动：物理网卡绑定、启动失败后台重试、池/注册器加固。
- 基础设施：后台启动脚本、WebUI、WARP SOCKS5 UDP 数据路径、基线文档。
