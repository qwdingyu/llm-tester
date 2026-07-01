---
name: go-production-hardening
description: >-
  Go 项目生产就绪加固清单：安全审计、goroutine 泄漏修复、上下文传播、代理/超时统一客户端工厂、GitHub Actions CI/CD、跨平台构建脚本
source: auto-skill
extracted_at: '2026-07-01T07:16:24.846Z'
---

# Go 项目生产就绪加固模式

适用于将 Go 项目从"能跑"推进到"能上线"阶段——包括安全检查、并发泄漏修复、Context 传播、CI/CD 和跨平台发布。

## 1. 安全审计：敏感文件权限

### API Key / 凭据文件权限

```go
// ❌ 危险：0644 导致 API Key 世界可读
os.MkdirAll(configDir, 0755)       // 目录权限过宽
os.WriteFile(tmpPath, data, 0644)   // 文件权限过宽

// ✅ 安全：仅所有者可读写
os.MkdirAll(configDir, 0700)
os.WriteFile(tmpPath, data, 0600)
```

**核验清单：**
- [ ] 配置文件目录 `0700`（非 `0755`）
- [ ] 配置文件本身 `0600`（非 `0644`）
- [ ] 所有持久化的 API Key/Token 写入路径均适用
- [ ] `ExportJSON` 等导出功能是否也包含敏感字段（功能需求，需加 UI 提示）


## 2. 并发安全：WorkerPool goroutine 泄漏模式

### 问题：channel 操作无 `ctx.Done()` 保护

```go
// ❌ 危险模式：output 缓存满 + 超时触发时，worker 永久阻塞
func (p *WorkerPool) startWorker(fn func(int) TestResult) {
    for reqID := range p.input {
        select {
        case <-p.ctx.Done():
            return
        default:
            result := fn(reqID)
            p.output <- result  // 无 select 保护！缓存满时阻塞且无法被 ctx 唤醒
        }
    }
}
```

### 修复：output 写入也加 select 保护

```go
func (p *WorkerPool) startWorker(fn func(int) TestResult) {
    for reqID := range p.input {
        select {
        case <-p.ctx.Done():
            return
        default:
            result := fn(reqID)
            select {
            case p.output <- result:
            case <-p.ctx.Done():
                return
            }
        }
    }
}
```

### 问题：`RunBenchmark` 超时后写 input 永久阻塞

```go
// ❌ 危险：超时后所有 worker 退出，for 循环继续向 pool.input 写 → 永久阻塞
results := RunBenchmark(totalReqs, timeout, fn)
```

### 修复：input 写入也加 select 保护 + 传递 parent context

```go
func RunBenchmark(ctx context.Context, concurrency int, totalReqs int,
    timeout time.Duration, fn func(reqID int) TestResult) []TestResult {

    pool := NewWorkerPool(concurrency)
    benchCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    pool.Start(benchCtx, fn)

    for i := 0; i < totalReqs; i++ {
        select {
        case pool.input <- i:
        case <-benchCtx.Done():
            pool.Shutdown()
            return pool.CollectResults()
        }
    }

    pool.Shutdown()
    return pool.CollectResults()
}
```

### 关键原则

| 场景 | channel 操作 | 保护方式 |
|------|-------------|---------|
| Worker 读 input | `range p.input` | 隐式（channel 关闭时退出） |
| Worker 写 output | `p.output <- result` | `select { case <-ctx.Done() }` |
| 提交者写 input | `pool.input <- i` | `select { case <-ctx.Done() }` |
| 消费者读 output | `<-p.output` | `range p.output`（关闭后退出） |


## 3. Context 传播：客户端断开检测

### 场景：HTTP 长耗时操作

```go
// ❌ 问题：不检查客户端是否断开
for i := 0; i < rounds; i++ {
    provider.Chat(c.Request.Context(), req)  // 即使客户端关了，仍在执行
    // SSE Flush 也会失败，但服务器还在做无用功
}
```

### 修复：每轮检查 `ctx.Err()`

```go
ctx := c.Request.Context()
for i := 0; i < rounds; i++ {
    if err := ctx.Err(); err != nil {
        log.Printf("客户端断开，提前终止（已完成 %d/%d 轮）", i, rounds)
        break
    }
    result := provider.Chat(ctx, req)
    // ...
}
```

### 确认异步 WorkerPool 也传播 Context

- `RunBenchmark` 接受 `context.Context` 参数（而非使用 `context.Background()`）
- 批量测试等所有异步路径的父 ctx 来自 `c.Request.Context()`
- 这样客户端断开 → HTTP Context 取消 → WorkerPool Context 取消 → worker 提前退出


## 4. 超时和代理：统一 HTTP 客户端工厂

不要在 3 个 Provider 中四处散落 `&http.Client{Timeout: xxx}`。

```go
// llm/provider.go — 统一工厂方法
func (c *Config) GetHTTPClient() *http.Client {
    transport := &http.Transport{
        Proxy: http.ProxyFromEnvironment,  // 兜底：环境变量
    }

    // 配置代理优先
    if c.ProxyURL != "" {
        if proxyURL, err := url.Parse(c.ProxyURL); err == nil {
            transport.Proxy = http.ProxyURL(proxyURL)
        }
    }

    return &http.Client{
        Timeout:   c.GetTimeout(),
        Transport: transport,
    }
}
```

所有 Provider 统一调用 `p.config.GetHTTPClient()`：

```go
// openai.go, ollama.go, custom.go
client := p.config.GetHTTPClient()
resp, err := client.Do(req)
```

**好处：** 代理、超时、TLS 配置等改动只需修改一处。

### 代理配置场景：环境变量兜底 + 配置覆盖

```go
// GetHTTPClient 代理策略（优先级从高到低）
func (c *Config) GetHTTPClient() *http.Client {
    transport := &http.Transport{
        Proxy: http.ProxyFromEnvironment,  // ① 兜底：读取 HTTP_PROXY / HTTPS_PROXY / NO_PROXY
    }

    // ② 如果配置了代理，覆盖环境变量
    if c.ProxyURL != "" {
        if proxyURL, err := url.Parse(c.ProxyURL); err == nil {
            transport.Proxy = http.ProxyURL(proxyURL)
        }
    }

    return &http.Client{
        Timeout:   c.GetTimeout(),
        Transport: transport,
    }
}
```

**适用场景：** 用户本地有其他 AI 工具通过代理访问外网，LLM Tester 需要共享该代理配置。`http.ProxyFromEnvironment` 自动读取系统环境变量，`ProxyURL` 允许应用内配置覆盖。

### 前端代理字段与后端同步

`storage.Config` 和 `llm.Config` 同时添加 `ProxyURL` 字段，`frontend` 配置表单中增加代理地址输入框。API 调用时前端将整个 config 对象传给后端，后端通过 `toLLMConfig()` 转换为 `llm.Config` 后调用 `GetHTTPClient()`。所有 Provider 统一受益，无需逐个修改。


## 5. `go.mod` / `go.sum` 提交原则

**`go.sum` 必须提交**，否则 GitHub Actions 中 `go vet` 报错：

```
cmd/llm-tester/main.go:18:2: missing go.sum entry for module providing package ...
```

**`go.mod` 必须有完整 indirect 依赖**，否则 CI 中报错：

```
go: updates to go.mod needed; to update it: go mod tidy
```

✅ 提交前运行：
```bash
go mod tidy
git add go.mod go.sum && git commit
```


## 6. GitHub Actions CI/CD 工作流

### 最小可用模板

```yaml
name: build
on:
  push:
    branches: [main]
    tags: [v*]
  pull_request:
    branches: [main]

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod    # 自动读取 go 版本
          cache: true
      - run: go vet ./...
      - run: go build ./cmd/<name>

  release:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: check
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod, cache: true }
      - run: make release            # 跨平台编译 + 打包
      - uses: softprops/action-gh-release@v2
        with:
          files: dist/*
          generate_release_notes: true
```

### CI 常见失败根因排查

| CI 错误 | 可能原因 | 修复方法 |
|---------|---------|---------|
| `missing go.sum entry` | `go.sum` 未提交 | `git add go.sum` |
| `updates to go.mod needed` | `go.mod` 缺少 indirect 依赖 | `go mod tidy` |
| `undefined: X` | 某文件缺少 import | 检查本地 vs 提交版本的差异 |
| `go: not found` | setup-go 版本不对 | 确认 `go.mod` 中的 `go X.Y` 在 setup-go 中存在 |


## 7. 跨平台构建脚本

### Makefile（主入口，macOS/Linux/WSL）

```makefile
APP_NAME     := myapp
MAIN_PATH    := ./cmd/myapp
OUTPUT_DIR   := ./dist
VERSION      := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS      := -s -w

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

build:
	go build -ldflags="$(LDFLAGS)" -o $(APP_NAME) $(MAIN_PATH)

build-all:
	mkdir -p $(OUTPUT_DIR)
	for plat in $(PLATFORMS); do \
		GOOS=$$(echo $$plat | cut -d/ -f1); \
		GOARCH=$$(echo $$plat | cut -d/ -f2); \
		EXT=$$([ "$$GOOS" = "windows" ] && echo ".exe"); \
		go build -ldflags="$(LDFLAGS)" \
			-o "$(OUTPUT_DIR)/$(APP_NAME)-$(VERSION)-$${GOOS}-$${GOARCH}$${EXT}" \
			$(MAIN_PATH); \
	done

release: build-all
	# 为每个平台创建 tar.gz / zip

clean:
	rm -rf $(OUTPUT_DIR) $(APP_NAME) $(APP_NAME).exe
```

### `.gitignore`

```
# 构建产物
/myapp
/myapp.exe
/dist/

# 配置数据（敏感凭据）
/.myapp/
```

## 8. 完整验证清单

### 编译与静态分析
- [ ] `go build ./cmd/<name>/` 无错误
- [ ] `go vet ./...` 无警告

### 安全
- [ ] 配置目录 `0700`，配置文件 `0600`
- [ ] API Key 不会通过日志泄漏
- [ ] 导出功能明确告知包含敏感数据

### 并发
- [ ] WorkerPool 的 output 写入有 `select + ctx.Done()` 保护
- [ ] `RunBenchmark` 的 input 写入有 `select + ctx.Done()` 保护
- [ ] `RunBenchmark` 接受外部 `context.Context`

### HTTP 长耗时操作
- [ ] 循环体每轮检查 `ctx.Err()`
- [ ] SSE 端点传播请求 Context 到异步操作

### stream=false protection for LLM API POST requests

**Problem:** Many OpenAI-compatible APIs default to SSE streaming when `stream` parameter is not specified. Go's `io.ReadAll(resp.Body)` on a streaming response blocks forever until timeout (default 60s).

**❌ Patch approach (individual per Provider):**
```go
// openai.go
bodyMap := map[string]interface{}{"stream": false, ...}
// custom.go  
bodyMap := map[string]interface{}{"stream": false, ...}
// ollama.go — already has stream: false (but for different reason)
```

**✅ General solution: Shared request body builder in base package**
```go
// llm/provider.go — one place, all providers benefit
func BuildChatBody(model, message string, temperature float64, maxTokens int) map[string]interface{} {
    return map[string]interface{}{
        "model":       model,
        "messages":    []map[string]string{{"role": "user", "content": message}},
        "temperature": temperature,
        "max_tokens":  maxTokens,
        "stream":      false,  // ← critical: prevents SSE streaming timeout
    }
}
```

All providers call `BuildChatBody()` instead of constructing their own JSON body. If the API format changes, only one function needs updating.

### Shared error classification for HTTP APIs

**Problem:** Each provider implements its own error classification, leading to duplicated code and inconsistent error messages.

**❌ Pattern to avoid:**
```go
// openai.go — local error functions
func NewAPIError(...) *struct{...}
func classifyError(...) struct{...}
```

**✅ Pattern: Centralized error classification**
```go
// errors/errors.go — single source of truth
type ErrorCode string
const (
    ErrNetworkTimeout     ErrorCode = "network_timeout"
    ErrAuthInvalidKey     ErrorCode = "auth_invalid_key"
    ErrAuthQuotaExhausted ErrorCode = "auth_quota_exhausted"
    ErrServerError        ErrorCode = "server_error"
    // ... 15+ error codes
)

type APIError struct {
    Code       ErrorCode
    Message    string    // Chinese human-readable
    Detail     string
    StatusCode int
    Suggestion string    // Actionable advice
}

func NewAPIError(statusCode int, body []byte, err error) *APIError
```

Then use a shared response reader in the base package:
```go
// llm/provider.go
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

All providers use `ReadBodyOK()` instead of manually calling `client.Do()` + `io.ReadAll()` + error checking. This eliminates 3+ duplicate error handling blocks per provider.

### General solution over individual patches

**Principle:** When fixing a bug that affects multiple implementations, push the fix to the shared abstraction layer rather than patching each implementation individually.

**Signs you need a general solution:**
- The same fix appears in 2+ files with similar code
- You're writing the same comment (e.g., "stream=false prevents SSE timeout") in multiple places
- A new developer needs to remember to apply the fix when adding a new provider

**How to apply:**
1. Identify the shared behavior (request body construction, error handling, HTTP client setup)
2. Move it to the base package (`llm/provider.go`)
3. All implementations (OpenAI, Ollama, Custom) call the shared function
4. New implementations automatically get the fix

### CI/CD
- [ ] `go.sum` 已提交
- [ ] `go.mod` 运行过 `go mod tidy`
- [ ] CI workflow 在 GitHub Actions 中通过
- [ ] 标签推送可触发全平台 Release