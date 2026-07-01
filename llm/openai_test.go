package llm

import (
	"testing"
)

func TestParseChatResponse_Standard(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`{
		"choices": [{
			"message": {"content": "Hello, world!"},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`)

	resp := provider.parseChatResponse(body, 1000.0)
	if !resp.Success {
		t.Fatal("parseChatResponse 返回失败")
	}
	if resp.Content != "Hello, world!" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", resp.PromptTokens)
	}
	if resp.CompletionToks != 20 {
		t.Errorf("CompletionToks = %d", resp.CompletionToks)
	}
	if resp.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d", resp.TotalTokens)
	}
	if resp.LatencyMs != 1000.0 {
		t.Errorf("LatencyMs = %f", resp.LatencyMs)
	}
}

func TestParseChatResponse_NoUsage(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`{
		"choices": [{
			"message": {"content": "No usage info"},
			"finish_reason": "length"
		}]
	}`)

	resp := provider.parseChatResponse(body, 500.0)
	if !resp.Success {
		t.Fatal("parseChatResponse 返回失败")
	}
	if resp.Content != "No usage info" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "length" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	// usage 不存在时应为零值
	if resp.PromptTokens != 0 || resp.CompletionToks != 0 || resp.TotalTokens != 0 {
		t.Errorf("usage 应为 0, 得到 %d/%d/%d", resp.PromptTokens, resp.CompletionToks, resp.TotalTokens)
	}
}

func TestParseChatResponse_OutputText(t *testing.T) {
	// OpenAI Responses API 格式
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`{
		"output_text": "Response via output_text",
		"choices": [{
			"message": {"content": "Should be ignored"},
			"finish_reason": "stop"
		}]
	}`)

	resp := provider.parseChatResponse(body, 200.0)
	if resp.Content != "Response via output_text" {
		t.Errorf("output_text 模式: Content = %q", resp.Content)
	}
}

func TestParseChatResponse_InvalidJSON(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`not json`)

	resp := provider.parseChatResponse(body, 0)
	if resp.Success {
		t.Error("无效 JSON 应返回失败")
	}
	if resp.Error == "" {
		t.Error("无效 JSON 应返回错误信息")
	}
}

func TestParseChatResponse_EmptyChoices(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`{"choices": []}`)

	resp := provider.parseChatResponse(body, 0)
	if !resp.Success {
		t.Error("空 choices 应返回成功")
	}
	if resp.Content != "" {
		t.Errorf("Content = %q, 期望空", resp.Content)
	}
}

func TestParseChatResponse_Unicode(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	body := []byte(`{
		"choices": [{"message": {"content": "你好，世界！"}, "finish_reason": "stop"}]
	}`)

	resp := provider.parseChatResponse(body, 0)
	if resp.Content != "你好，世界！" {
		t.Errorf("Unicode 内容 = %q", resp.Content)
	}
}

func TestBuildEndpointPath_Default(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4"}}
	path := provider.buildEndpointPath()
	if path != "/v1/chat/completions" {
		t.Errorf("默认路径 = %q", path)
	}
}

func TestBuildEndpointPath_Responses(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{Model: "gpt-4", EndpointMode: "responses"}}
	path := provider.buildEndpointPath()
	if path != "/v1/responses" {
		t.Errorf("responses 路径 = %q", path)
	}
}

func TestBuildEndpointPath_Azure(t *testing.T) {
	provider := &OpenAIProvider{config: &Config{APIType: "azure", Model: "gpt-4-deploy"}}
	path := provider.buildEndpointPath()
	expected := "/deployments/gpt-4-deploy/chat/completions?api-version=2024-02-01"
	if path != expected {
		t.Errorf("Azure 路径 = %q", path)
	}
}

func TestOllamaParseResponse(t *testing.T) {
	provider := &OllamaProvider{config: &Config{Model: "llama3"}}
	body := []byte(`{
		"message": {"content": "Ollama response"},
		"done": true,
		"done_reason": "stop",
		"usage": {
			"prompt_tokens": 15,
			"completion_tokens": 25,
			"total_tokens": 40
		}
	}`)

	resp := provider.parseOllamaResponse(body, 300.0)
	if !resp.Success {
		t.Fatal("parseOllamaResponse 返回失败")
	}
	if resp.Content != "Ollama response" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q", resp.FinishReason)
	}
	if resp.PromptTokens != 15 {
		t.Errorf("PromptTokens = %d", resp.PromptTokens)
	}
	if resp.CompletionToks != 25 {
		t.Errorf("CompletionToks = %d", resp.CompletionToks)
	}
}

func TestOllamaParseResponse_NoDoneReason(t *testing.T) {
	provider := &OllamaProvider{config: &Config{Model: "llama3"}}
	body := []byte(`{
		"message": {"content": "Done without reason"},
		"done": true
	}`)

	resp := provider.parseOllamaResponse(body, 0)
	if resp.FinishReason != "stop" {
		t.Errorf("done=true 但无 done_reason 时应默认 stop, 得到 %q", resp.FinishReason)
	}
}

func TestOllamaParseResponse_InvalidJSON(t *testing.T) {
	provider := &OllamaProvider{config: &Config{Model: "llama3"}}
	body := []byte(`not json`)
	resp := provider.parseOllamaResponse(body, 0)
	if resp.Success {
		t.Error("无效 JSON 应返回失败")
	}
}