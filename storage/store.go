// Package storage 提供 LLM 测试配置的持久化存储。
// 配置保存在 ~/.llm_tester/configs.json 中。
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Config 表示一个 LLM API 的测试配置
type Config struct {
	Name          string  `json:"name"`                     // 配置名称
	APIType       string  `json:"api_type"`                 // API 类型: openai / ollama / azure / custom
	BaseURL       string  `json:"base_url"`                 // API 基础地址
	APIKey        string  `json:"api_key"`                  // API Key
	Model         string  `json:"model"`                    // 模型名称
	CustomPath    string  `json:"custom_path,omitempty"`    // 自定义路径（custom 类型使用）
	EndpointMode  string  `json:"endpoint_mode,omitempty"`  // 端点模式: chat_completions / responses
	HTTPReferer   string  `json:"http_referer,omitempty"`   // HTTP-Referer 头
	XTitle        string  `json:"x_title,omitempty"`        // X-Title 头
	Temperature   float64 `json:"temperature"`              // 温度参数 (0-2)
	MaxTokens     int     `json:"max_tokens"`               // 最大输出 token 数
	Timeout       int     `json:"timeout"`                  // HTTP 请求超时（秒），0 表示使用默认值 60s
}

// configsData 是配置文件的顶层结构
type configsData struct {
	Configs map[string]*Config `json:"configs"`
}

// Store 管理配置的读写
type Store struct {
	mu       sync.RWMutex
	filePath string          // 配置文件路径
	configs  map[string]*Config // name -> Config
}

// NewStore 创建配置存储实例
func NewStore() (*Store, error) {
	s := &Store{
		configs: make(map[string]*Config),
	}
	// 配置文件路径: ~/.llm_tester/configs.json
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户目录失败: %w", err)
	}
	configDir := filepath.Join(home, ".llm_tester")
	// 确保目录存在
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}
	s.filePath = filepath.Join(configDir, "configs.json")
	// 加载已有配置
	if err := s.load(); err != nil {
		// 文件不存在不是错误，首次使用会创建
		if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return s, nil
}

// load 从文件加载配置
func (s *Store) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var cfgData configsData
	if err := json.Unmarshal(data, &cfgData); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}
	s.configs = cfgData.Configs
	if s.configs == nil {
		s.configs = make(map[string]*Config)
	}
	return nil
}

// save 将配置写入文件（原子写入：先写临时文件再重命名）
func (s *Store) save() error {
	cfgData := configsData{Configs: s.configs}
	data, err := json.MarshalIndent(cfgData, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	// 原子写入：先写临时文件，再重命名
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}
	return nil
}

// List 返回所有配置的名称列表
func (s *Store) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.configs))
	for name := range s.configs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetAll 返回所有配置
func (s *Store) GetAll() map[string]*Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]*Config, len(s.configs))
	for k, v := range s.configs {
		result[k] = v
	}
	return result
}

// Get 获取指定名称的配置
func (s *Store) Get(name string) *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.configs[name]
}

// Save 保存配置（新增或覆盖）
func (s *Store) Save(name string, cfg *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[name] = cfg
	return s.save()
}

// SaveAll 批量保存配置（用于导入）
func (s *Store) SaveAll(configs map[string]*Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, cfg := range configs {
		s.configs[name] = cfg
	}
	return s.save()
}

// Delete 删除指定名称的配置
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, name)
	return s.save()
}

// ImportJSON 从 JSON 数据导入配置（支持单个对象或数组）
func (s *Store) ImportJSON(data []byte) (int, error) {
	// 尝试解析为数组
	var configs []Config
	if err := json.Unmarshal(data, &configs); err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range configs {
			cfg := configs[i]
			if cfg.Name != "" {
				s.configs[cfg.Name] = &cfg
			}
		}
		if err := s.save(); err != nil {
			return 0, err
		}
		return len(configs), nil
	}

	// 尝试解析为单个对象
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0, fmt.Errorf("无法解析导入数据")
	}
	if cfg.Name == "" {
		return 0, fmt.Errorf("配置缺少名称")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[cfg.Name] = &cfg
	if err := s.save(); err != nil {
		return 0, err
	}
	return 1, nil
}

// ExportJSON 导出所有配置为 JSON 字节
func (s *Store) ExportJSON() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfgData := configsData{Configs: s.configs}
	data, _ := json.MarshalIndent(cfgData, "", "  ")
	return data
}

// PresetConfig 预设模板定义
type PresetConfig struct {
	Name         string `json:"name"`
	APIType      string `json:"api_type"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	EndpointMode string `json:"endpoint_mode,omitempty"`
}

// Presets 返回预设模板列表
func Presets() []PresetConfig {
	return []PresetConfig{
		{Name: "OpenAI", APIType: "openai", BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini", EndpointMode: "chat_completions"},
		{Name: "DeepSeek", APIType: "openai", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", EndpointMode: "chat_completions"},
		{Name: "Kimi", APIType: "openai", BaseURL: "https://api.moonshot.cn/v1", Model: "moonshot-v1-8k", EndpointMode: "chat_completions"},
		{Name: "GLM-4", APIType: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4-flash", EndpointMode: "chat_completions"},
		{Name: "通义千问", APIType: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-turbo", EndpointMode: "chat_completions"},
		{Name: "Ollama", APIType: "ollama", BaseURL: "http://localhost:11434", Model: "llama3", EndpointMode: ""},
		{Name: "Azure", APIType: "azure", BaseURL: "https://YOUR_RESOURCE.openai.azure.com", Model: "gpt-4", EndpointMode: ""},
		{Name: "Custom", APIType: "custom", BaseURL: "", Model: "", EndpointMode: "chat_completions"},
	}
}

// Validate 校验配置必填字段
func (c *Config) Validate() error {
	var missing []string
	if c.APIType == "" {
		missing = append(missing, "api_type")
	}
	if c.Name == "" {
		missing = append(missing, "name")
	}
	// openai 和 azure 类型需要 BaseURL 和 APIKey
	if c.APIType == "openai" || c.APIType == "azure" {
		if c.BaseURL == "" {
			missing = append(missing, "base_url")
		}
		if c.APIKey == "" {
			missing = append(missing, "api_key")
		}
		if c.Model == "" {
			missing = append(missing, "model")
		}
	}
	if c.APIType == "custom" && c.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("缺少必填字段: %s", strings.Join(missing, ", "))
	}
	return nil
}