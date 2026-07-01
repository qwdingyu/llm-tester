// Package llm 的 Ollama 本地模型提供者实现
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
type OllamaProvider struct {
	config *Config
}

// TestConnection 测试与 Ollama 服务的连接
func (p *OllamaProvider) TestConnection(ctx context.Context) *ConnectionResult {
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	url := baseURL + "/api/tags"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("创建请求失败: %v", err),
		}
	}

	client := p.config.GetHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: %v（确保Ollama已启动）", err),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return &ConnectionResult{
			Success: false,
			Message: fmt.Sprintf("连接失败: HTTP %d", resp.StatusCode),
		}
	}

	// 解析模型列表
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
		return &ConnectionResult{
			Success: true,
			Message: "连接成功（无可用模型）",
		}
	}

	return &ConnectionResult{
		Success: false,
		Message: "解析响应失败",
	}
}

// Chat 与 Ollama 模型对话
func (p *OllamaProvider) Chat(ctx context.Context, req *ChatRequest) *ChatResponse {
	start := time.Now()

	// Ollama /api/chat 的请求体
	bodyMap := map[string]interface{}{
		"model": p.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": req.Message},
		},
		"stream": false,
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	baseURL := strings.TrimRight(p.config.BaseURL, "/")
	fullURL := baseURL + "/api/chat"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return &ChatResponse{
			Success:   false,
			Error:     fmt.Sprintf("创建请求失败: %v", err),
			LatencyMs: time.Since(start).Seconds() * 1000,
		}
	}

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

	respBody, _ := io.ReadAll(resp.Body)

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
func (p *OllamaProvider) ListModels(ctx context.Context) ([]string, error) {
	result := p.TestConnection(ctx)
	if !result.Success {
		return nil, fmt.Errorf(result.Message)
	}
	return result.Models, nil
}

// parseOllamaResponse 解析 Ollama /api/chat 的响应
func (p *OllamaProvider) parseOllamaResponse(body []byte, latencyMs float64) *ChatResponse {
	var resp struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Done bool `json:"done"`
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

	if resp.Usage != nil {
		result.PromptTokens = resp.Usage.PromptTokens
		result.CompletionToks = resp.Usage.CompletionTokens
		result.TotalTokens = resp.Usage.TotalTokens
	}

	return result
}