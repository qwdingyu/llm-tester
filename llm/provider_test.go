package llm

import (
	"testing"
	"time"
)

func TestConfigGetTimeout(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		want    time.Duration
	}{
		{"nil", nil, defaultTimeout},
		{"未设置(0)", &Config{Timeout: 0}, defaultTimeout},
		{"负数", &Config{Timeout: -1}, defaultTimeout},
		{"自定义30s", &Config{Timeout: 30}, 30 * time.Second},
		{"自定义120s", &Config{Timeout: 120}, 120 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetTimeout()
			if got != tt.want {
				t.Errorf("GetTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigGetHTTPClient_Nil(t *testing.T) {
	var cfg *Config
	client := cfg.GetHTTPClient()
	if client == nil {
		t.Fatal("GetHTTPClient() 返回 nil")
	}
	if client.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", client.Timeout, defaultTimeout)
	}
}

func TestConfigGetHTTPClient_WithTimeout(t *testing.T) {
	cfg := &Config{Timeout: 15}
	client := cfg.GetHTTPClient()
	if client.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want 15s", client.Timeout)
	}
}

func TestConfigGetHTTPClient_WithProxy(t *testing.T) {
	cfg := &Config{Timeout: 10, ProxyURL: "http://127.0.0.1:7890"}
	client := cfg.GetHTTPClient()
	if client.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", client.Timeout)
	}
	// Transport.Proxy 被设置，但无法直接验证其值
	if client.Transport == nil {
		t.Error("Transport 不应为 nil")
	}
}

func TestNewProvider_Nil(t *testing.T) {
	p := NewProvider(nil)
	if p != nil {
		t.Error("NewProvider(nil) 应返回 nil")
	}
}

func TestNewProvider_OpenAI(t *testing.T) {
	cfg := &Config{APIType: "openai", BaseURL: "https://api.openai.com"}
	p := NewProvider(cfg)
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Errorf("NewProvider(openai) 应返回 OpenAIProvider, 得到 %T", p)
	}
}

func TestNewProvider_Ollama(t *testing.T) {
	cfg := &Config{APIType: "ollama", BaseURL: "http://localhost:11434"}
	p := NewProvider(cfg)
	if _, ok := p.(*OllamaProvider); !ok {
		t.Errorf("NewProvider(ollama) 应返回 OllamaProvider, 得到 %T", p)
	}
}

func TestNewProvider_Custom(t *testing.T) {
	cfg := &Config{APIType: "custom", BaseURL: "https://custom.test"}
	p := NewProvider(cfg)
	if _, ok := p.(*CustomProvider); !ok {
		t.Errorf("NewProvider(custom) 应返回 CustomProvider, 得到 %T", p)
	}
}

func TestNewProvider_Azure(t *testing.T) {
	cfg := &Config{APIType: "azure", BaseURL: "https://xxx.openai.azure.com"}
	p := NewProvider(cfg)
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Errorf("NewProvider(azure) 应返回 OpenAIProvider, 得到 %T", p)
	}
}

func TestBuildChatBody(t *testing.T) {
	body := BuildChatBody("gpt-4", &ChatRequest{Model: "gpt-4", Message: "hello", Temperature: 0.7, MaxTokens: 100})

	if body["model"] != "gpt-4" {
		t.Errorf("model = %v", body["model"])
	}
	if body["temperature"] != 0.7 {
		t.Errorf("temperature = %v", body["temperature"])
	}
	if body["max_tokens"] != 100 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	if body["stream"] != false {
		t.Error("stream 应为 false")
	}

	// 检查 messages 结构
	msgs, ok := body["messages"].([]map[string]string)
	if !ok {
		t.Fatal("messages 类型错误")
	}
	if len(msgs) != 1 {
		t.Fatalf("messages 长度 = %d", len(msgs))
	}
	if msgs[0]["role"] != "user" {
		t.Errorf("role = %q", msgs[0]["role"])
	}
	if msgs[0]["content"] != "hello" {
		t.Errorf("content = %q", msgs[0]["content"])
	}
}

func TestBuildTestBody(t *testing.T) {
	t.Run("with model", func(t *testing.T) {
		body := BuildTestBody("gpt-4")
		if body["model"] != "gpt-4" {
			t.Errorf("model = %v", body["model"])
		}
		if body["max_tokens"] != 1 {
			t.Errorf("max_tokens = %v", body["max_tokens"])
		}
		if body["stream"] != false {
			t.Error("stream 应为 false")
		}
	})

	t.Run("empty model defaults", func(t *testing.T) {
		body := BuildTestBody("")
		if body["model"] != "gpt-4o-mini" {
			t.Errorf("model = %v, 期望 gpt-4o-mini", body["model"])
		}
	})
}

func TestTrimURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com/v1"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1"},
		{"http://localhost:11434/", "http://localhost:11434"},
		{"https://x.com", "https://x.com"},
		{"", ""},
	}
	for _, tt := range tests {
		got := trimURL(tt.input)
		if got != tt.want {
			t.Errorf("trimURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}