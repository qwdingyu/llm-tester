// Package llm 的 Ollama 本地模型提供者实现。
//
// Ollama 使用与 OpenAI 不同的 API 协议:
// - 连接测试: GET /api/tags（获取本地已下载的模型列表）
// - 聊天: POST /api/chat（与 OpenAI 的 messages 格式类似但路径不同）
// - 不支持 /v1/models 和 /v1/chat/completions 路径
//
// Ollama 的特点:
// - 本地运行，不需要 API Key
// - 默认地址 http://localhost:11434
// - 不需要 HTTP-Referer / X-Title 等额外请求头
// - 响应格式与 OpenAI 略有差异（message 结构在顶层而非 choices 数组）
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

// OllamaProvider 实现了 Ollama 本地模型的调用
//
// 与 OpenAIProvider 的区别:
// - 无需 Authorization 请求头（Ollama 默认无认证）
// - 使用 /api/chat 而非 /v1/chat/completions
// - 响应格式不同（无 choices 数组，message 直接在顶层）
// - 通常运行在 localhost，延迟较低（< 100ms）
type OllamaProvider struct {
	config *Config
}

// TestConnection 测试与 Ollama 服务的连接
//
// 使用 GET /api/tags 获取本地已下载的模型列表。
// 与 OpenAI 的 /v1/models 不同:
// - Ollama 的 /api/tags 只返回已下载到本地的模型
// - 如果 Ollama 服务正在运行但没有模型，返回"连接成功（无可用模型）"
func (p *OllamaProvider) TestConnection(ctx context.Context) *ConnectionResult {
	baseURL := trimURL(p.config.BaseURL)
	url := baseURL + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("创建请求失败: %v", err),
		}
	}

	// Ollama 不需要 Authorization 请求头，直接发送请求
	client := p.config.GetHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		// 常见的失败原因: Ollama 服务未启动
		// 错误信息中附带"(确保Ollama已启动)"可以帮助用户快速定位问题
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: %v（确保Ollama已启动）", err),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ConnectionResult{Success: false, Message: fmt.Sprintf("读取响应失败: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: HTTP %d", resp.StatusCode),
		}
	}

	// 解析模型列表
	// Ollama /api/tags 响应格式: {"models": [{"name": "llama3:latest", ...}, ...]}
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err == nil {
		var models []string
		for _, m := range result.Models {
			models = append(models, m.Name)
		}
		if len(models) > 0 {
			return &ConnectionResult{
				Success: true,
				Message: fmt.Sprintf("连接成功，本地模型: %s", strings.Join(models, ", ")),
				Models:  models,
			}
		}
		// Ollama 服务在运行但没有模型时仍然返回成功
		return &ConnectionResult{
			Success: true,
			Message: "连接成功（无可用模型）",
		}
	}

	// JSON 解析失败作为连接失败处理
	// 因为 Ollama /api/tags 的响应格式非常稳定，解析失败说明服务异常
	return &ConnectionResult{
		Success: false,
		Message: "解析响应失败",
	}
}

// Chat 与 Ollama 模型对话
//
// 使用 POST /api/chat 发送消息。
// Ollama 的请求体格式与 OpenAI 相似:
// - model: 模型名称（必须已通过 ollama pull 下载）
// - messages: 消息列表（与 OpenAI 格式一致）
// - stream: false（必须显式设置，否则 Ollama 默认走 SSE 流式）
//
// 与 OpenAI 的主要差异:
// - 不需要 Content-Type 以外的请求头（无 Authorization）
// - 响应体结构不同（message 在顶层而非 choices[0].message）
func (p *OllamaProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()

	// Ollama /api/chat 的请求体
	// stream=false 同样重要，Ollama 和 OpenAI 一样默认走 SSE 流式
	bodyMap := map[string]interface{}{
		"model": p.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": req.Message},
		},
		"stream": false,
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	fullURL := trimURL(p.config.BaseURL) + "/api/chat"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("创建请求失败: %v", err),
			LatencyMs: time.Since(start).Seconds() * 1000,
		}
	}

	// Ollama 只需要 Content-Type 请求头
	// 不需要 User-Agent / Accept / Authorization 等
	httpReq.Header.Set("Content-Type", "application/json")

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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ChatResponse{Success: false, Error: fmt.Sprintf("读取响应失败: %v", err), LatencyMs: latency}
	}

	if resp.StatusCode != http.StatusOK {
		return &ChatResponse{
			Success:    false,
			Error:      fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody)),
			LatencyMs:  latency,
			StatusCode: resp.StatusCode,
		}
	}

	return p.parseOllamaResponse(respBody, latency)
}

// ListModels 获取本地模型列表
//
// 复用 TestConnection 的逻辑。
// Ollama 的模型列表是本地已下载的模型，与远程 API 无关。
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	result := p.TestConnection(ctx)
	if !result.Success {
		return nil, fmt.Errorf(result.Message)
	}
	return result.Models, nil
}

// parseOllamaResponse 解析 Ollama /api/chat 的响应
//
// Ollama 响应格式与 OpenAI 不同:
//
// OpenAI:
//
//	{"choices": [{"message": {"content": "..."}, "finish_reason": "stop"}], "usage": {...}}
//
// Ollama:
//
//	{"message": {"content": "..."}, "done": true, "done_reason": "stop", "usage": {...}}
//
// 主要差异:
// - message 在顶层而非 choices[0].message
// - done 布尔字段表示是否完成
// - done_reason 对应 OpenAI 的 finish_reason
// - usage 格式兼容但可能不返回（Ollama 部分版本不支持）
func (p *OllamaProvider) parseOllamaResponse(body []byte, latencyMs float64) *ChatResponse {
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Done       bool   `json:"done"`
		DoneReason string `json:"done_reason"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("解析响应失败: %v", err),
			LatencyMs: latencyMs,
		}
	}

	// done_reason 为空但 done=true 时，默认 finish_reason 为 "stop"
	// Ollama 的部分旧版本不返回 done_reason 字段
	finishReason := resp.DoneReason
	if finishReason == "" && resp.Done {
		finishReason = "stop"
	}

	result := &ChatResponse{
		Success:      true,
		Content:      resp.Message.Content,
		FinishReason: finishReason,
		LatencyMs:    latencyMs,
	}

	// Ollama 部分版本可能不返回 usage 信息
	if resp.Usage != nil {
		result.PromptTokens = resp.Usage.PromptTokens
		result.CompletionToks = resp.Usage.CompletionTokens
		result.TotalTokens = resp.Usage.TotalTokens
	}

	return result
}