// Package llm 的 OpenAI 兼容 API 提供者实现
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

// OpenAIProvider 实现了 OpenAI 兼容 API 的调用
type OpenAIProvider struct {
	config *Config
}

// TestConnection 测试与 OpenAI 兼容 API 的连接
func (p *OpenAIProvider) TestConnection(ctx context.Context) *ConnectionResult {
	baseURL := trimURL(p.config.BaseURL)
	var url string
	var req *http.Request
	var err error

	if p.config.APIType == "azure" {
		url = fmt.Sprintf("%s/deployments?api-version=2024-02-01", baseURL)
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{Success: false, Message: fmt.Sprintf("创建请求失败: %v", err)}
		}
		req.Header.Set("api-key", p.config.APIKey)
	} else {
		url = baseURL + "/v1/models"
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{Success: false, Message: fmt.Sprintf("创建请求失败: %v", err)}
		}
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	SetCommonHeaders(p.config, req)

	body, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(req))
	if errMsg != "" {
		return &ConnectionResult{Success: false, Message: "连接失败: " + errMsg}
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

	// 使用共享构建函数（自动包含 stream=false）
	bodyBytes, _ := json.Marshal(BuildChatBody(p.config.Model, req.Message, req.Temperature, req.MaxTokens))

	path := p.buildEndpointPath()
	fullURL := trimURL(p.config.BaseURL) + path

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{Success: false, Error: fmt.Sprintf("创建请求失败: %v", err), LatencyMs: time.Since(start).Seconds() * 1000}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if p.config.APIType == "azure" {
		httpReq.Header.Set("api-key", p.config.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	SetCommonHeaders(p.config, httpReq)

	respBody, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(httpReq))
	latency := time.Since(start).Seconds() * 1000

	if errMsg != "" {
		return &ChatResponse{Success: false, Error: errMsg, LatencyMs: latency}
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