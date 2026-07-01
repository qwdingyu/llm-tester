---
name: llm-api-provider-architecture
description: >-
  Go LLM API 提供者抽象层：共享请求体构建、统一错误分类、HTTP 客户端工厂、
  多 Provider 注册与参数统一处理
source: auto-skill
extracted_at: '2026-07-01T13:10:13.249Z'
---

# Go LLM API Provider 统一抽象层模式

适用于需要同时支持多个 LLM API 提供者（OpenAI 兼容、Ollama、Azure、自定义端点）的 Go 项目。核心思想：将所有 Provider 共用的逻辑提取到基包，避免每个 Provider 重复实现。

## 架构概览

```
llm/                      # provider 抽象层
├── provider.go           # 接口 + Config + 共享函数（BuildChatBody / ReadBodyOK / GetHTTPClient）
├── openai.go             # OpenAIProvider: OpenAI 兼容 API
├── ollama.go             # OllamaProvider: Ollama 本地模型
└── custom.go             # CustomProvider: 自定义端点
errors/
└── errors.go             # 结构化错误分类（统一被 provider.go 引用）
```

## 1. Provider 接口定义

```go
// llm/provider.go
type Provider interface {
    TestConnection(ctx context.Context) *ConnectionResult
    Chat(ctx context.Context, req *ChatRequest) *ChatResponse
    ListModels(ctx context.Context) ([]string, error)
}

// 工厂函数
func NewProvider(cfg *Config) Provider {
    if cfg == nil {
        return nil
    }
    switch cfg.APIType {
    case "ollama":
        return &OllamaProvider{config: cfg}
    case "custom":
        return &CustomProvider{config: cfg}
    default:
        // 绝大多数第三方 API 兼容 OpenAI 的 /v1/chat/completions 格式
        return &OpenAIProvider{config: cfg}
    }
}
```

**关键设计决策：**
- 接口小而稳定（仅 3 个方法）
- 工厂函数用 switch 而非反射
- 未知类型默认走 OpenAIProvider（兼容性最好）

## 2. 共享请求体构建

### 问题：POST 请求缺少 stream=false 导致 60s 超时

大量 OpenAI 兼容 API 在未指定 `stream` 参数时默认返回 SSE 流式响应。Go 的 `io.ReadAll(resp.Body)` 读取流式响应会永久阻塞直到默认 60s 超时。

### ❌ 错误做法：每个 Provider 各自构造 body

```go
// openai.go — 手写 body
bodyMap := map[string]interface{}{"model": ..., "messages": ..., "stream": false}

// custom.go — 又写一遍完全相同的 body
bodyMap := map[string]interface{}{"model": ..., "messages": ..., "stream": false}
```

### ✅ 正确做法：共享构建函数

```go
// llm/provider.go — 一处定义，全部收益
func BuildChatBody(model, message string, temperature float64, maxTokens int) map[string]interface{} {
    return map[string]interface{}{
        "model":       model,
        "messages":    []map[string]string{{"role": "user", "content": message}},
        "temperature": temperature,
        "max_tokens":  maxTokens,
        "stream":      false,  // ← 关键：防止 SSE 流式导致 io.ReadAll 永久阻塞
    }
}

// 各 Provider 调用
// openai.go
bodyBytes, _ := json.Marshal(BuildChatBody(p.config.Model, req.Message, req.Temperature, req.MaxTokens))

// custom.go — 同样的调用
bodyBytes, _ := json.Marshal(BuildChatBody(p.config.Model, req.Message, req.Temperature, req.MaxTokens))
```

**好处：** 新增 Provider 时自动获得 stream=false 保护；修改格式只需改一处。

## 3. 统一 HTTP 客户端工厂

不要在每个 Provider 中散落 `&http.Client{Timeout: ...}`。

```go
// llm/provider.go
func (c *Config) GetHTTPClient() *http.Client {
    transport := &http.Transport{
        Proxy: http.ProxyFromEnvironment,  // 兜底：读取环境变量 HTTP_PROXY
    }
    if c.ProxyURL != "" {                 // 配置代理优先
        if proxyURL, err := url.Parse(c.ProxyURL); err == nil {
            transport.Proxy = http.ProxyURL(proxyURL)
        }
    }
    return &http.Client{
        Timeout:   c.GetTimeout(),
        Transport: transport,
    }
}

// 所有 Provider 统一使用
client := p.config.GetHTTPClient()
resp, err := client.Do(req)
```

**统一管理的内容：**
- 超时
- HTTP 代理（配置优先、环境变量兜底）
- TLS 配置（后续可扩展）

## 4. 统一错误分类

### 问题：重复的错误分类代码

```go
// openai.go — 自己实现错误分类
func classifyError(statusCode int, body []byte, err error) struct { ... }

// 没有其他 Provider 使用这个函数 —— 每个 Provider 有各自的错误处理
```

### ❌ 避免：每个 Provider 实现自己的错误映射

Provider 之间错误分类逻辑完全一致（401→Key 无效、429→限流、5xx→服务器错误），不应该有 N 份重复代码。

### ✅ 正确做法：集中到 errors 包 + 通用读取函数

```go
// errors/errors.go — 单一真相来源
type ErrorCode string
const (
    ErrNetworkTimeout     ErrorCode = "network_timeout"
    ErrAuthInvalidKey     ErrorCode = "auth_invalid_key"
    ErrAuthQuotaExhausted ErrorCode = "auth_quota_exhausted"
    ErrServerError        ErrorCode = "server_error"
    // ... 15+ 错误码
)

type APIError struct {
    Code       ErrorCode
    Message    string    // 中文用户可见消息
    Detail     string
    StatusCode int
    Suggestion string    // 中文可操作建议
}

func NewAPIError(statusCode int, body []byte, err error) *APIError {
    // 1. 检查网络错误（err != nil）
    // 2. 按 HTTP 状态码分类（401/403/429/5xx 等）
    // 3. 检查响应体关键词（"quota exhausted" → 额度用尽）
}
```

然后在基包中提供 `ReadBodyOK` 统一处理 HTTP 响应：

```go
// llm/provider.go — 一行完成：读取 + 错误分类 + 中文提示
func ReadBodyOK(resp *http.Response, err error) ([]byte, string) {
    if err != nil {
        apiErr := errors.NewAPIError(0, nil, err)
        return nil, apiErr.Message + ": " + apiErr.Suggestion
    }
    defer resp.Body.Close()
    body, readErr := io.ReadAll(resp.Body)
    if readErr != nil {
        return nil, fmt.Sprintf("读取响应失败: %v", readErr)
    }
    if resp.StatusCode != http.StatusOK {
        apiErr := errors.NewAPIError(resp.StatusCode, body, nil)
        return nil, fmt.Sprintf("%s - %s", apiErr.Message, apiErr.Suggestion)
    }
    return body, ""
}
```

**效果对比：**

| 维度 | ❌ 无统一层（旧代码） | ✅ 有统一层（新代码） |
|------|---------------------|---------------------|
| openai.go Chat | 19 行：手动 Do → io.ReadAll → 分类 error → 返回 | 6 行：BuildChatBody → ReadBodyOK → parseChatResponse |
| openai.go TestConnection | 25 行：手动构建 → Do → 分类 → 返回 | 12 行：构建 req → ReadBodyOK → 解析 models |
| 错误分类 | 各自实现，不一致 | errors 包统一，中文提示一致 |
| 新增 Provider | 需重写全部 | 只需实现接口 + 响应解析 |

### 关键错误分类规则（errors.NewAPIError 实现）

```
网络错误:     err != nil → timeout → "连接超时"
                       → connection refused → "连接被拒绝"
                       → no such host → "DNS 解析失败"
                       → 其他 → "网络错误"

HTTP 4xx:     401/403 + "quota"/"exhausted"/"insufficient" → "额度已用尽"
              401/403 + "invalid_api_key" → "API Key 无效"
              401/403 → "鉴权失败"
              429 → "请求频率超限"
              400 + "context_length"/"maximum context" → "上下文长度超限"
              400 + "model_not_found" → "模型不存在"
              400 → "请求参数错误"
              404 → "端点不存在"

HTTP 5xx:     503 → "服务暂不可用"
              其他 5xx → "服务器错误"

HTTP 其他:    兜底 → "未知错误"
```

## 5. Provider 间差异处理

### 路径差异

```go
// openai.go — 路径由 EndpointMode 控制
func (p *OpenAIProvider) buildEndpointPath() string {
    if p.config.APIType == "azure" {
        return fmt.Sprintf("/deployments/%s/chat/completions?api-version=...", p.config.Model)
    }
    switch p.config.EndpointMode {
    case EndpointResponses:
        return "/v1/responses"
    default:
        return "/v1/chat/completions"
    }
}

// ollama.go — 固定路径
func (p *OllamaProvider) Chat(...) {
    fullURL := trimURL(p.config.BaseURL) + "/api/chat"
}

// custom.go — 自定义路径或模式推导
func (p *CustomProvider) buildURL() string {
    if p.config.CustomPath != "" {
        return baseURL + p.config.CustomPath
    }
    switch p.config.EndpointMode {
    case "ollama": return base + "/api/chat"
    default:       return base + "/v1/chat/completions"
    }
}
```

### 认证差异

```go
// 统一在 SetCommonHeaders 中设置公共头，但认证方式各自处理
// openai.go
httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)  // Azure 用 api-key

// ollama.go — 不需要认证头

// custom.go — 根据配置决定
if p.config.APIKey != "" {
    httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
}
```

### 响应解析差异

每个 Provider 有自己的 JSON 反序列化逻辑，无法统一：

```go
// openai.go — choices[0].message.content + usage
// ollama.go — message.content（顶层, 无 choices） + done_reason
// custom.go — 复用 openai.go 的 parseChatResponse（假设格式兼容）
```

但要注意：custom.go 如果格式不兼容，应该有自己的解析函数，不要强行复用。

## 6. 两个 Config 结构体的设计理由

```go
// storage.Config — 面向持久化
type Config struct {
    Name     string  `json:"name"`
    APIKey   string  `json:"api_key"`
    Timeout  int     `json:"timeout"`
    ProxyURL string  `json:"proxy_url,omitempty"`
}

// llm.Config — 面向 HTTP 调用
type Config struct {
    APIType  string
    APIKey   string
    Timeout  int
    ProxyURL string
}
```

**为什么两个 struct 而不是一个？**

避免循环依赖：`main` → `storage` → `llm` 是允许的，但 `storage` → `llm` 会形成 `main → storage → llm → storage` 的循环。

**解决方案：** `main.go` 做桥梁，提供转换函数：

```go
// cmd/llm-tester/main.go
func toLLMConfig(cfg *storage.Config) *llm.Config {
    if cfg == nil { return nil }
    return &llm.Config{
        APIType:      cfg.APIType,
        BaseURL:      cfg.BaseURL,
        APIKey:       cfg.APIKey,
        Model:        cfg.Model,
        Timeout:      cfg.Timeout,
        ProxyURL:     cfg.ProxyURL,
        // ... 逐字段映射
    }
}
```

**注意：** 两个 struct 字段类型相同但类型不同，**不能直接强制转换** `(*llm.Config)(&storageConfig)`——Go 要求类型完全一致（同一类型定义）。

## 7. 验证清单

### Provider 层测试

- [ ] 三个 Provider 的 `TestConnection` 都用 `ReadBodyOK` 统一错误分类
- [ ] 三个 Provider 的 `Chat` 都用 `BuildChatBody` 共享请求体
- [ ] 三个 Provider 的 `Chat` 都用 `GetHTTPClient()` 统一 HTTP 客户端
- [ ] `SetCommonHeaders` 被所有 Provider 调用
- [ ] `errors.NewAPIError` 被 `ReadBodyOK` 调用（而非每个 Provider 各自实现）
- [ ] Provider 间差异（路径/认证）只在实际有差异的地方处理

### 安全

- [ ] API Key 文件权限：目录 0700，文件 0600
- [ ] 文件写入使用原子方式（temp + rename）

### 连接测试路径一致性

连接测试的路径应与聊天端点保持一致：

```go
// ✅ OpenAI: 连接用 GET /v1/models，聊天用 POST /v1/chat/completions
// 统一使用 /v1 前缀

// ❌ 不要出现：连接用 /models（404），聊天用 /v1/chat/completions（200）
// URL 路径不一致导致"连接测试失败但聊天能正常"的迷惑现象
```

### 前端 API 测试

运行时验证各 Provider 的完整调用链路可用，包括：

- [ ] 连接测试 → 返回模型列表
- [ ] 聊天测试 → 返回回复内容 + Token 统计
- [ ] 模型列表 → 返回可用模型名
- [ ] 批量/基准/Burn 测试 → SSE 流式推送正确

## 8. 常见陷阱

### 陷阱 1: 连接测试路径与聊天路径不一致

**原因：** 连接测试用 `GET {base}/models`，聊天用 `POST {base}/v1/chat/completions`——后者多了一层 `/v1`。

**修复：** 连接测试改为 `GET {base}/v1/models`，与聊天端点版本前缀一致。

### 陷阱 2: 配置 base_url 包含 /v1 导致双重重叠

**原因：** `buildEndpointPath()` 始终返回 `/v1/chat/completions`。如果 base_url 已是 `https://api.openai.com/v1`，则最终 URL 为 `https://api.openai.com/v1/v1/chat/completions`。

**解决方案：** base_url 不应包含 `/v1`（代码统一前缀）。特殊 API（GLM-4、DashScope）保留原始路径。

### 陷阱 3: 死代码不被清理

**现象：** Provider 有自有的 `NewAPIError` 函数，同时 `errors` 包也有完全相同的定义，两者互不引用。

**检查方法：**
```bash
grep -r "func NewAPIError" --include="*.go"
grep -r "errors\." --include="*.go" | grep -v "_test.go"
```

**修复：** Provider 调用 `errors.NewAPIError`，删除 Provider 本地的错误分类函数。
