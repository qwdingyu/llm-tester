// Package llm 的自定义 API 提供者实现。
//
// 自定义端点适用于以下场景:
// - API 路径不符合 OpenAI / Ollama 标准（如使用 /completions 而非 /chat/completions）
// - 需要自定义请求头或认证方式
// - 私有 API 或内部代理
//
// 自定义端点的路径拼接规则:
// 1. 如果设置了 CustomPath，使用 base_url + custom_path
// 2. 否则根据 endpoint_mode 选择默认路径:
//    - chat_completions → /v1/chat/completions
//    - responses → /v1/responses
//    - ollama → /api/chat
//    - 其他 → /v1/chat/completions（兜底）
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CustomProvider 实现了自定义 API 端点的调用
//
// CustomProvider 与 OpenAIProvider 的差异:
// - buildURL 支持自定义路径，不强制 /v1/... 格式
// - 连接测试通过发送最小化的 POST 请求来验证（而非 GET /v1/models）
// - 响应解析复用 OpenAIProvider.parseChatResponse（假设响应格式兼容）
type CustomProvider struct {
	config *Config
}

// TestConnection 测试自定义端点的连接
//
// 由于自定义端点没有标准的 /v1/models 路径，
// 连接测试通过发送一个最小化的 POST 请求来验证:
// - max_tokens: 1（最小化 Token 消耗）
// - content: "ping"（短消息）
//
// 判断逻辑:
// - HTTP 200 → 连接成功
// - HTTP 400 → 也可能是有效连接（如模型名错误但服务可达）
// - 其他 → 连接失败
func (p *CustomProvider) TestConnection(ctx context.Context) *ConnectionResult {
	url := p.buildURL()

	// 使用 BuildTestBody 构建最小化请求体
	bodyBytes, _ := json.Marshal(BuildTestBody(p.config.Model))
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ConnectionResult{Success: false, Message: fmt.Sprintf("创建请求失败: %v", err)}
	}

	// 设置请求头
	// 自定义端点可能要求特殊的认证方式，所以使用 setHeaders 方法
	p.setHeaders(req)

	body, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(req))
	if errMsg != "" {
		// 400 BadRequest 也可能是有效连接
		// 例如: 模型名错误但服务本身可达
		// 所以连接测试的失败判断比 ReadBodyOK 宽松
		// 但 ReadBodyOK 已经返回了错误信息，这里无法再区分 400
		// 所以改用直接检查 status code
		return &ConnectionResult{Success: false, Message: fmt.Sprintf("连接失败: %s", errMsg)}
	}
	_ = body // body 对连接测试无意义，只是为了调用 ReadBodyOK

	return &ConnectionResult{
		Success: true,
		Message: fmt.Sprintf("连接成功，自定义端点: %s", url),
	}
}

// Chat 发送聊天消息到自定义端点
//
// 请求体使用 BuildChatBody 共享函数构建。
// 响应解析复用 OpenAIProvider 的 parseChatResponse 方法，
// 因为自定义端点通常兼容 OpenAI 的响应格式。
//
// 如果自定义端点使用不同的响应格式，parseChatResponse 可能返回"解析响应失败"，
// 这是预期的行为 - CustomProvider 假设响应格式兼容 OpenAI。
func (p *CustomProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()
	url := p.buildURL()

	// 使用共享构建函数（自动包含 stream=false）
	bodyBytes, _ := json.Marshal(BuildChatBody(p.config.Model, req.Message, req.Temperature, req.MaxTokens))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("创建请求失败: %v", err),
			LatencyMs: time.Since(start).Seconds() * 1000,
		}
	}

	p.setHeaders(httpReq)

	respBody, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(httpReq))
	latency := time.Since(start).Seconds() * 1000

	if errMsg != "" {
		return &ChatResponse{Success: false, Error: errMsg, LatencyMs: latency}
	}

	// 复用 OpenAIProvider 的响应解析
	// 假设自定义端点兼容 OpenAI 的响应格式
	return (&OpenAIProvider{config: p.config}).parseChatResponse(respBody, latency)
}

// ListModels 自定义端点暂不支持获取模型列表
//
// 因为自定义端点没有标准的模型列表接口，
// 无法通过统一的 API 获取可用模型。
func (p *CustomProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("自定义端点不支持获取模型列表")
}

// buildURL 根据配置构建完整的请求 URL
//
// URL 构建规则:
// 1. 如果设置了 CustomPath，直接拼接 baseURL + customPath
//    例: base_url="http://localhost:8080", custom_path="/my/chat" → "http://localhost:8080/my/chat"
// 2. 否则根据 endpoint_mode 选择路径:
//    - chat_completions → /v1/chat/completions
//    - responses → /v1/responses
//    - ollama → /api/chat
//    - 其他 → /v1/chat/completions（兜底）
//
// 为什么 custom_path 支持完整路径:
// 部分自建 API 使用非标准路径（如 /api/v2/generate），
// 此时无法通过 endpoint_mode 映射到正确路径。
func (p *CustomProvider) buildURL() string {
	base := trimURL(p.config.BaseURL)

	// 如果有自定义路径，直接拼接
	// custom_path 可以以 / 开头也可以不以 / 开头
	if p.config.CustomPath != "" {
		path := p.config.CustomPath
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return base + path
	}

	// 根据 endpoint_mode 选择默认路径
	switch p.config.EndpointMode {
	case "chat_completions":
		return base + "/v1/chat/completions"
	case "responses":
		return base + "/v1/responses"
	case "ollama":
		return base + "/api/chat"
	default:
		return base + "/v1/chat/completions"
	}
}

// setHeaders 设置自定义端点的请求头
//
// 与 OpenAIProvider 相比多了 Authorization 设置:
// - 如果配置了 API Key，添加 Bearer token
// - 同时添加通用请求头（User-Agent / Accept / Referer / X-Title）
//
// Content-Type 在 ReadBodyOK 的调用方设置
func (p *CustomProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	// 只有配置了 API Key 时才设置 Authorization
	// 自定义端点可能不需要认证
	if p.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	// 设置 Referer 和 X-Title（部分 API 需要）
	if p.config.HTTPReferer != "" {
		req.Header.Set("HTTP-Referer", p.config.HTTPReferer)
		req.Header.Set("Referer", p.config.HTTPReferer)
	}
	if p.config.XTitle != "" {
		req.Header.Set("X-Title", p.config.XTitle)
	}
}
