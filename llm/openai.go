// Package llm 的 OpenAI 兼容 API 提供者实现。
//
// 本文件实现了大多数 OpenAI 兼容 API 的调用逻辑:
// - OpenAI / DeepSeek / Kimi / GLM-4 / 通义千问 等标准 OpenAI 兼容接口
// - Azure OpenAI Service（使用 api-key 认证 + 独立端点格式）
// - OpenAI Responses API 新格式（/v1/responses）
//
// 请求体构建和错误处理委托给 provider.go 的共享函数，
// 本文件只关注: 认证方式（Bearer vs api-key）、端点路径、响应格式差异。
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
//
// config 字段直接透传 provider.Config，不额外封装。
// 主要职责:
// 1. 根据 APIType 选择认证方式（Bearer token / Azure api-key）
// 2. 根据 EndpointMode 选择路径（chat/completions / responses）
// 3. 响应解析兼容两种格式（标准 choices / 新 output_text）
type OpenAIProvider struct {
	config *Config
}

// TestConnection 测试与 OpenAI 兼容 API 的连接
//
// 使用 GET /v1/models 获取模型列表来验证连接。
// 为什么选择 /v1/models 而非直接 POST /v1/chat/completions:
// - GET 请求不消耗 token 配额
// - /v1/models 的响应体比 chat 响应体小得多，传输更快
// - 可以同时获取可用模型列表，作为连接测试的附加信息
//
// Azure 使用独立的 /deployments?api-version=... 路径
// 因为 Azure OpenAI 的资源管理 API 路径格式与标准 OpenAI 不同
func (p *OpenAIProvider) TestConnection(ctx context.Context) *ConnectionResult {
	baseURL := trimURL(p.config.BaseURL)
	var url string
	var req *http.Request
	var err error

	// Azure 使用资源部署模型，路径格式为:
	//   GET {endpoint}/openai/deployments?api-version=2024-02-01
	// 认证使用 api-key 请求头，而非 Bearer token
	if p.config.APIType == "azure" {
		url = fmt.Sprintf("%s/deployments?api-version=2024-02-01", baseURL)
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{Success: false, Message: fmt.Sprintf("创建请求失败: %v", err)}
		}
		req.Header.Set("api-key", p.config.APIKey)
	} else {
		// 标准 OpenAI 兼容 API 使用 /v1/models 路径
		// 注意: base_url 不应包含 /v1（由代码统一添加）
		url = baseURL + "/v1/models"
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return &ConnectionResult{Success: false, Message: fmt.Sprintf("创建请求失败: %v", err)}
		}
		req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}

	SetCommonHeaders(p.config, req)

	// ReadBodyOK 统一处理网络错误和 HTTP 状态码错误
	body, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(req))
	if errMsg != "" {
		return &ConnectionResult{Success: false, Message: errMsg}
	}

	// 解析模型列表
	// OpenAI /v1/models 响应格式: {"data": [{"id": "gpt-4", "object": "model"}, ...]}
	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		var models []string
		for _, m := range result.Data {
			// 部分 API 使用 name 字段，部分使用 id 字段
			// 优先使用 name（因为部分自建 API 只用 name 做展示）
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

	// JSON 解析失败或模型列表为空时，仍然返回连接成功
	// 因为部分 API 虽然支持 /v1/chat/completions 但不提供 /v1/models 端点
	return &ConnectionResult{
		Success: true,
		Message: "连接成功（未获取到模型列表）",
	}
}

// Chat 发送聊天消息
//
// 使用 POST /v1/chat/completions 发送消息。
// 请求体通过 BuildChatBody 共享函数构建（自动包含 stream=false）。
//
// LatencyMs 在创建 HTTP 请求时就记录开始时间，
// 即使请求创建失败也能返回部分延迟数据（即 DNS 解析到请求构建的时间）。
func (p *OpenAIProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()

	// 使用共享构建函数（自动包含 stream=false，支持多轮/JSON模式）
	bodyBytes, _ := json.Marshal(BuildChatBody(p.config.Model, req))

	path := p.buildEndpointPath()
	fullURL := trimURL(p.config.BaseURL) + path

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{Success: false, Error: fmt.Sprintf("创建请求失败: %v", err), LatencyMs: time.Since(start).Seconds() * 1000}
	}

	// 认证方式取决于 API 类型
	// Azure: 使用 api-key 请求头（非标准）
	// 其他: 使用 Authorization: Bearer 标准方式
	httpReq.Header.Set("Content-Type", "application/json")
	if p.config.APIType == "azure" {
		httpReq.Header.Set("api-key", p.config.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	}
	SetCommonHeaders(p.config, httpReq)

	// ReadBodyOK 统一处理: 网络错误 + HTTP 状态码错误 + 响应体读取
	respBody, errMsg := ReadBodyOK(p.config.GetHTTPClient().Do(httpReq))
	latency := time.Since(start).Seconds() * 1000

	if errMsg != "" {
		return &ChatResponse{Success: false, Error: errMsg, LatencyMs: latency}
	}

	return p.parseChatResponse(respBody, latency)
}

// ListModels 获取模型列表
//
// 复用 TestConnection 的逻辑以避免重复代码。
// 与 TestConnection 的区别: 只返回模型名列表，不关心连接详情。
// 失败时返回 error（而非 ConnectionResult），方便 Gin handler 直接返回 HTTP 500。
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	// 复用 TestConnection 的逻辑
	result := p.TestConnection(ctx)
	if !result.Success {
		return nil, fmt.Errorf(result.Message)
	}
	return result.Models, nil
}

// buildEndpointPath 根据配置构建 API 路径
//
// 路径选择逻辑:
// 1. Azure: 使用 /deployments/{model}/chat/completions?api-version=...
//    因为 Azure OpenAI 的资源路径包含部署名称（deployment name）
// 2. Responses 模式: 使用 /v1/responses（OpenAI 2024+ 新格式）
// 3. 默认: 使用 /v1/chat/completions（标准格式，兼容性最好）
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
//
// 兼容两种响应格式:
// 1. 标准 chat completions 格式:
//    {"choices": [{"message": {"content": "..."}, "finish_reason": "stop"}], "usage": {...}}
// 2. OpenAI Responses API 新格式（2024+）:
//    {"output_text": "...", ...}
//
// 两种格式的兼容处理:
// - output_text 不为空时优先使用（新格式）
// - 否则退回到 choices[0].message.content（标准格式）
// - usage 信息为可选字段，不存在时返回 0 值
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

	// JSON 解析失败不是网络错误，不通过 ReadBodyOK 处理
	// 因为此时 HTTP 调用本身成功了，只是响应体格式不符合预期
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
	// output_text 是 top-level 字段，不在 choices 数组中
	if resp.OutputText != "" {
		content = resp.OutputText
		finishReason = "stop"
	}

	// 标准 chat completions 格式
	// content == "" 时作为兜底条件，避免 output_text 和 choices 同时存在时取错
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

	// usage 信息可能为 nil（部分 API 不返回此字段）
	if resp.Usage != nil {
		result.PromptTokens = resp.Usage.PromptTokens
		result.CompletionToks = resp.Usage.CompletionTokens
		result.TotalTokens = resp.Usage.TotalTokens
	}

	return result
}
