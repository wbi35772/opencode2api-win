# 接入 OpenCode

网关本身只是 OpenAI 兼容的 HTTP 服务，任何客户端都能用。这里记录把它注册成 OpenCode 模型组的做法。

## 配置位置

```text
C:\Users\24999\.config\opencode\opencode.json
```

在顶层 `providers` 里加一个 provider，ID 用 `win`：

```json
"win": {
  "name": "Windows 本地",
  "package": "aisdk:@ai-sdk/openai-compatible",
  "settings": {
    "baseURL": "http://127.0.0.1:28080/v1",
    "apiKey": "sk-win-local-test"
  },
  "models": {
    "deepseek-v4.1-flash-free": {
      "modelID": "deepseek-v4.1-flash-free",
      "name": "DeepSeek V4.1 Flash Free",
      "capabilities": {
        "tools": true,
        "input": ["text", "image"],
        "output": ["text"]
      },
      "limit": { "context": 200000, "output": 32000 }
    }
  }
}
```

改完不需要重启 OpenCode：

```powershell
opencode reload
opencode models
```

`opencode models` 里应该能看到 `win/` 前缀的模型。

## 已注册的 8 个模型

| 模型 ID | 上下文 | 最大输出 | 备注 |
|---|---|---|---|
| `beam-free` | 1,048,576 | 131,072 | 超长上下文 |
| `big-pickle` | 200,000 | 32,000 | |
| `deepseek-v4.1-flash-free` | 200,000 | 32,000 | 日常主力 |
| `flint-40b-free` | 262,144 | 65,536 | |
| `glm-5.3-flash-free` | 200,000 | 32,000 | 支持图片/视频输入 |
| `mimo-v2.6-flash-free` | 200,000 | 32,000 | 支持图片/音频/视频 |
| `nemotron-3.5-lightning-free` | 262,144 | 262,144 | 输出可以很长 |
| `nemotron-3.5-super-vl-free` | 1,048,576 | 131,072 | 图片输入 |

这些数值来自网关的 `/v1/models`，不是猜的。

## 使用

```powershell
# 指定模型跑一次
opencode run -m win/deepseek-v4.1-flash-free "Reply with OK only."

# 列出所有 win 模型
opencode models | Select-String '^win/'

# 直接打 API
curl.exe --noproxy "*" -H "Authorization: Bearer sk-win-local-test" `
  http://127.0.0.1:28080/v1/models
```

OpenChamber 读的是同一份 OpenCode 配置，模型选择器刷新后会多出 **Windows 本地** 分组。

## 官方组和本地组的区别

这是最容易踩的坑：两个组用的是**同一个模型名**，但走完全不同的路。

| 你选的 | 实际出站 | 额度池 | 稳定性 |
|---|---|---|---|
| `opencode/deepseek-v4.1-flash-free` | OpenCode 进程直连 `opencode.ai`，用本机公网 IP | 按本机 IP 算 | 受本机网络和 429 影响 |
| `win/deepseek-v4.1-flash-free` | 发到 `127.0.0.1:28080`，网关再走 WARP | 按 WARP 的每个 `/64` 算 | 网关可轮换出口 |

所以官方组 429 的时候，切到 `win/` 往往还能用；反过来也一样。

WARP 只在网关进程内部生效，**不会**让 OpenCode 的官方组也走 WARP。

## 常见返回

| 返回 | 含义 | 处理 |
|---|---|---|
| 401 | API Key 不对 | 检查 `settings.apiKey` 和 `config.server_keys` |
| 404 model | 网关没有这个模型 | `curl /v1/models` 看实际列表 |
| 429 `FreeUsageLimitError` | 出口额度用完 | 等冷却、换出口、或等 WARP 轮换到新的 `/64` |
| 502 / 504 | 上游或代理失败 | 看 `logs\stdout.log` 的 `request_failed` 事件 |

网关的 429 是**透传上游**的，不是网关自己的限流。看到它就说明请求确实打到了上游并且被拒。

## OpenAI SDK / 其他客户端

```text
OPENAI_BASE_URL=http://127.0.0.1:28080/v1
OPENAI_API_KEY=sk-win-local-test
```

只要是 OpenAI 兼容客户端都可以，不限于 OpenCode。

## 前提

用 `win` 组时网关必须开着：

```powershell
cd win
.\start-background.ps1
```

进程不在，客户端会直接连接被拒绝。
