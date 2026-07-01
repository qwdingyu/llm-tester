// Package llm 的 OpenAI 兼容 API 提供者实现
// 支持 OpenAI、DeepSeek、Kimi、GLM、通义千问等兼容接口，以及 Azure
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

// OpenAIProvider 实现了 OpenAI 兼容 API 的调用
type OpenAIProvider struct {
	config *Config
}

// TestConnection 测试与 OpenAI 兼容 API 的连接
func (p *OpenAIProvider) TestConnection(ctx context.Context) *ConnectionResult {
	// 构造请求：获取模型列表
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	var url string
	var req *http.Request
	var err error

	if p.config.APIType == "azure" {
		// Azure 使用不同的端点格式
		url = fmt.Sprintf("%s/deployments?api-version=2024-02-01", baseURL)
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{
				Success: false,
				Message: fmt.Sprintf("创建请求失败: %v", err),
			}
		}
		req.Header.Set("api-key", p.config.APIKey)
	} else {
		url = baseURL + "/models"
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{
				Success: false,
				Message: fmt.Sprintf("创建请求失败: %v", err),
			}
		}
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	// 设置通用请求头
	p.setCommonHeaders(req)

	client := &http.Client{Timeout: p.config.GetTimeout()}
	resp, err := client.Do(req)
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: %v", err),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		apiErr := NewAPIError(resp.StatusCode, body, nil)
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: %s", apiErr.Message),
		}
	}

	// 解析模型列表
	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		var models []string
		for _, m := range result.Data {
			name := m.ID
			if m.Name != "" {
				name = m.Name
			}
			models = append(models, name)
		}
		if len(models) > 0 {
			return &ConnectionResult{
				Success: true,
				Message: fmt.Sprintf("连接成功，可用模型: %s", strings.Join(models, ", ")),
				Models:  models,
			}
		}
	}

	return &ConnectionResult{
		Success: true,
		Message: "连接成功（未获取到模型列表）",
	}
}

// Chat 发送聊天消息
func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()

	// 构造请求体
	bodyMap := map[string]interface{}{
		"model": p.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": req.Message},
		},
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
	}

	// 判断端点模式
	path := p.buildEndpointPath()
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	fullURL := baseURL + path

	bodyBytes, _ := json.Marshal(bodyMap)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{
			Success:    false,
			Error:      fmt.Sprintf("创建请求失败: %v", err),
			LatencyMs:  time.Since(start).Seconds() * 1000,
		}
	}

	// 设置请求头
	httpReq.Header.Set("Content-Type", "application/json")
	if p.config.APIType == "azure" {
		httpReq.Header.Set("api-key", p.config.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	p.setCommonHeaders(httpReq)

	client := &http.Client{Timeout: p.config.GetTimeout()}
	resp, err := client.Do(httpReq)
	latency := time.Since(start).Seconds() * 1000

	if err != nil {
		apiErr := NewAPIError(0, nil, err)
		return &ChatResponse{
			Success:   false,
			Error:     apiErr.Message + ": " + apiErr.Suggestion,
			LatencyMs: latency,
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		apiErr := NewAPIError(resp.StatusCode, respBody, nil)
		return &ChatResponse{
			Success:    false,
			Error:      apiErr.Message + " - " + apiErr.Suggestion,
			LatencyMs:  latency,
			StatusCode: resp.StatusCode,
		}
	}

	return p.parseChatResponse(respBody, latency)
}

// ListModels 获取模型列表
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	// 复用 TestConnection 的逻辑
	result := p.TestConnection(ctx)
	if !result.Success {
		return nil, fmt.Errorf(result.Message)
	}
	return result.Models, nil
}

// buildEndpointPath 根据配置构建 API 路径
func (p *OpenAIProvider) buildEndpointPath() string {
	if p.config.APIType == "azure" {
		return fmt.Sprintf("/deployments/%s/chat/completions?api-version=2024-02-01", p.config.Model)
	}

	// 根据 endpoint_mode 选择路径
	switch p.config.EndpointMode {
	case EndpointResponses:
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}

// setCommonHeaders 设置通用请求头
func (p *OpenAIProvider) setCommonHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	// 设置 HTTP-Referer（部分 API 需要）
	if p.config.HTTPReferer != "" {
		req.Header.Set("HTTP-Referer", p.config.HTTPReferer)
		req.Header.Set("Referer", p.config.HTTPReferer)
	}

	// 设置 X-Title（部分 API 需要）
	if p.config.XTitle != "" {
		req.Header.Set("X-Title", p.config.XTitle)
	}
}

// parseChatResponse 解析 OpenAI 兼容的聊天响应
func (p *OpenAIProvider) parseChatResponse(body []byte, latencyMs float64) *ChatResponse {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		OutputText string `json:"output_text,omitempty"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("解析响应失败: %v", err),
			LatencyMs: latencyMs,
		}
	}

	// 提取回复内容
	content := ""
	finishReason := ""

	// 兼容 responses 格式（OpenAI 新格式）
	if resp.OutputText != "" {
		content = resp.OutputText
		finishReason = "stop"
	}

	// 标准 chat completions 格式
	if len(resp.Choices) > 0 && content == "" {
		content = resp.Choices[0].Message.Content
		finishReason = resp.Choices[0].FinishReason
	}

	result := &ChatResponse{
		Success:      true,
		Content:      content,
		FinishReason: finishReason,
		LatencyMs:    latencyMs,
	}

	// 提取 usage 信息
	if resp.Usage != nil {
		result.PromptTokens = resp.Usage.PromptTokens
		result.CompletionToks = resp.Usage.CompletionTokens
		result.TotalTokens = resp.Usage.TotalTokens
	}

	return result
}

// NewAPIError 创建 LLM API 错误
func NewAPIError(statusCode int, body []byte, err error) *struct {
	Message string
	Suggestion string
} {
	apiErr := classifyError(statusCode, body, err)
	return &struct {
		Message string
		Suggestion string
	}{Message: apiErr.Message, Suggestion: apiErr.Suggestion}
}

// classifyError 根据状态码和响应体分类错误
func classifyError(statusCode int, body []byte, err error) struct {
	Message string
	Suggestion string
} {
	// 网络错误
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "timeout") {
			return struct {
				Message    string
				Suggestion string
			}{Message: "连接超时", Suggestion: "请检查网络连接或 API 地址"}
		}
		if strings.Contains(errMsg, "connection refused") {
			return struct {
				Message    string
				Suggestion string
			}{Message: "连接被拒绝", Suggestion: "请检查 API 地址和端口"}
		}
		return struct {
			Message    string
			Suggestion string
		}{Message: "网络错误", Suggestion: "请检查网络连接后重试"}
	}

	// HTTP 状态码错误
	bodyStr := string(body)
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		if strings.Contains(bodyStr, "quota") || strings.Contains(bodyStr, "exhausted") || strings.Contains(bodyStr, "insufficient") {
			return struct {
				Message    string
				Suggestion string
			}{Message: "额度已用尽", Suggestion: "请充值或更换 API Key"}
		}
		return struct {
			Message    string
			Suggestion string
		}{Message: "API Key 无效", Suggestion: "请检查 API Key 是否正确"}
	case statusCode == http.StatusTooManyRequests:
		return struct {
			Message    string
			Suggestion string
		}{Message: "请求频率超限", Suggestion: "请降低请求频率"}
	case statusCode >= 500:
		return struct {
			Message    string
			Suggestion string
		}{Message: "服务器错误", Suggestion: "API 服务端异常，请稍后重试"}
	default:
		return struct {
			Message    string
			Suggestion string
		}{Message: "请求失败", Suggestion: fmt.Sprintf("HTTP %d: %s", statusCode, bodyStr)}
	}
}