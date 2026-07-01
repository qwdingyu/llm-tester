// Package llm 的自定义 API 提供者实现
// 支持用户自定义端点路径和请求格式
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CustomProvider 实现了自定义 API 端点的调用
type CustomProvider struct {
	config *Config
}

// TestConnection 测试自定义端点的连接
func (p *CustomProvider) TestConnection(ctx context.Context) *ConnectionResult {
	url := p.buildURL()

	// 构造一个简单的测试请求
	bodyMap := map[string]interface{}{
		"model": p.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
		"stream":     false,
	}

	// 如果没设置模型，用默认提示词
	if p.config.Model == "" {
		bodyMap = map[string]interface{}{
			"model": "gpt-4o-mini",
			"messages": []map[string]string{
				{"role": "user", "content": "ping"},
			},
			"max_tokens": 1,
			"stream":     false,
		}
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("创建请求失败: %v", err),
		}
	}

	// 设置请求头
	p.setHeaders(req)

	client := p.config.GetHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest {
		// 400 也可能是有效连接（如参数问题）
		return &ConnectionResult{
			Success: true,
			Message: fmt.Sprintf("连接成功，自定义端点: %s", url),
		}
	}

	return &ConnectionResult{
		Success: false,
		Message: fmt.Sprintf("连接失败: HTTP %d", resp.StatusCode),
	}
}

// Chat 发送聊天消息到自定义端点
func (p *CustomProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()
	url := p.buildURL()

	// 构造请求体（显式 stream=false 防止流式响应导致超时）
	bodyMap := map[string]interface{}{
		"model": p.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": req.Message},
		},
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stream":      false,
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("创建请求失败: %v", err),
			LatencyMs: time.Since(start).Seconds() * 1000,
		}
	}

	p.setHeaders(httpReq)

	client := p.config.GetHTTPClient()
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Seconds() * 1000

	if err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("请求失败: %v", err),
			LatencyMs: latency,
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return &ChatResponse{
			Success:    false,
			Error:      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
			LatencyMs:  latency,
			StatusCode: resp.StatusCode,
		}
	}

	// 尝试解析为标准 OpenAI 格式
	return (&OpenAIProvider{config: p.config}).parseChatResponse(respBody, latency)
}

// ListModels 自定义端点暂不支持获取模型列表
func (p *CustomProvider) ListModels(ctx context.Context) ([]string, error) {
	return nil, fmt.Errorf("自定义端点不支持获取模型列表")
}

// buildURL 根据配置构建完整的请求 URL
func (p *CustomProvider) buildURL() string {
	base := strings.TrimRight(p.config.BaseURL, "/")

	// 如果有自定义路径，直接拼接
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
func (p *CustomProvider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)

	// 设置 Authorization
	if p.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	// 设置 Referer
	if p.config.HTTPReferer != "" {
		req.Header.Set("HTTP-Referer", p.config.HTTPReferer)
		req.Header.Set("Referer", p.config.HTTPReferer)
	}

	// 设置 X-Title
	if p.config.XTitle != "" {
		req.Header.Set("X-Title", p.config.XTitle)
	}
}