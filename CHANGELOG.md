# Changelog

本仓库是 `opencode2api` 的 Windows 独立分支。版本号格式：`上游版本-win.N`。

## v1.5.1-win.1 — 2026-11-21

### 完整对齐上游 v1.5.1

- 第一次使用**完整 merge** 而非选择性移植：`git merge v1.5.1`（上游 `6575c62`），合并提交 `18b6a15`。
- `0d05fe9` 批量代理管理：`POST /api/proxies/check`、`ProxyCheckResult`、`ProxyConnectError`、`config.ValidateProxyEntry`、Bun TLS 探测；WebUI 新增代理页（批量粘贴/检查/勾选删除）。`anonymous` 模板默认改为 `false`；本机 `config.json` 显式保留 `true`。
- `e2b2a7c`（v1.5.0）Go/Zen OAuth 重构：`internal/goauth` 删除，改为 `internal/oauth`；配置字段 `go_subscription` → `go_oauth`，新增 `zen_oauth`；管理/网关 OAuth 路由、catalog schema v4（OAuth tier/pending）、错误与 `Retry-After` 脱敏、短 Key 哈希显示。
- `e2b2a7c` 安全/流式硬化：代理与 WARP 客户端拒绝 HTTP 重定向（防 `x-api-key`/请求体重放）；SSE 只转发完整帧、单帧上限 16 MiB、畸形/超长帧不转发；首帧前不提交 200，无效前奏 502；非流式响应上限 64 MiB；工具重命名键加入 `choiceIndex`。
- `8f691a0`（#190）客户端环境隔离：新增 `client_environment.go`，解析 Chat `system`/`developer`、Anthropic `system`、Responses `instructions`/`input`、Codex `<environment_context>` 和顶层 `client_environment`；每请求快照；4096 字节/控制字符/尖括号校验；冲突或畸形回退 `unknown`，不再注入宿主机 cwd/workspace/platform/git。
- `6575c62`（v1.5.1，#188）免费模型工具描述 NBSP：客户端工具顶层 `description` 的 ASCII 空格替换为 `U+00A0`；参数 schema、追加的官方工具和名称还原不动，幂等。用于规避上游免费层对工具描述字面措辞的拒绝。

### 保留的 fork 能力

- WARP：24h 额度黑名单持久化、注册前预检、会话亲和、SOCKS5 UDP ASSOCIATE、Windows 物理网卡钉扎/TUN 开关、分级冷却、池预热。
- 免费道：`mcp_` 前缀、官方工具别名与流式收尾（#166）、被动可用性记账（#171）、OpenCode 原点 + Bun TLS 代理探测（#153）。
- Windows 启动/停止脚本、`doc/09` SOCKS5 UDP 基线、WebUI 主动探测开关与四语文案。

### 合并后修复

- `internal/gateway/pool.go`、`internal/admin/server.go` 的非法 UTF-8（em dash）已还原。
- 删除 fork 与上游重复的 `RefreshCatalog`，保留上游 OAuth-aware 版本。
- 删除无调用方的 `internal/goauth`；`config.example.json` 重新加入 fork 的 `warp.socks_udp`/`warp.tun` 示例。

### 验证

- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿（gateway 0.127s、protocol 0.037s、warp 12.216s）。
- 部署 `v1.5.1-win.1`：14,328,832 B，SHA-256 `6D614EFC858E791EAFC8B4C3163CB8423730A19C1A2376818BC0AA1DC8F44812`，PID 24188。
- 免费道：`hy4-flash-free` 非流式 `200/16.8s`（`PONG`）；流式 + `tool_choice=required` `200/50.0s`（`get_weather {"city":"Paris"}`、`finish_reason=tool_calls`、`[DONE]`）。
- 同池 `deepseek-v4.1-flash-free`/`big-pickle` 在旧出口上仍会 429；换出口后 `hy4` 成功，确认是出口状态而非版本故障。
- catalog 旧 v3 缓存被拒绝并重新拉取（v4），属正常升级。

### 已知限制

- 本机仍只用匿名免费道，`go_oauth`/`zen_oauth` 未配置。
- WARP 依赖本机 Clash `10808` 的 TCP + UDP；旧 `7897` 不再使用。
- `#167` Playground relay 固定仍未移植。
- 发布提交 `e092353`，tag `v1.5.1-win.1`。

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
