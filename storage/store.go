// Package storage 提供 LLM 测试配置的持久化存储。
//
// 存储方式:
// - 文件: ~/.llm_tester/configs.json
// - 格式: JSON（{"configs": {"name": {...}, ...}}）
// - 权限: 目录 0700，文件 0600（仅所有者可读写）
// - 写入: 原子写入（先写临时文件再 rename，防止写入中断导致数据损坏）
//
// 设计决策:
// 1. 使用 JSON 而非数据库 — 配置数量少（通常 < 50 个），不需要数据库的开销
// 2. 使用 map 而非数组 — 配置名唯一，map 的查找和更新更高效
// 3. 原子写入 — 防止程序崩溃时配置文件损坏
// 4. 文件锁 — sync.RWMutex 保证并发安全
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
//
// 字段说明:
// - Name: 配置名称，唯一标识（同时也是 JSON 文件的 key）
// - APIType: 决定了使用哪个 Provider 实现
// - BaseURL: 不应包含 /v1（由代码统一添加）
// - CustomPath: 仅 custom 类型使用，优先级高于 endpoint_mode
// - EndpointMode: 控制请求路径格式
// - HTTPReferer / XTitle: 部分 API 需要，用于通过反爬检测
// - Timeout: 0 表示使用默认值 60s
// - ProxyURL: 空字符串表示不使用代理
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
	ProxyURL      string  `json:"proxy_url,omitempty"`      // HTTP 代理地址，如 http://127.0.0.1:7890
}

// configsData 是配置文件的顶层结构
// 使用嵌套结构而非直接存储 map，便于未来扩展（如增加 schema_version 字段）
type configsData struct {
	Configs map[string]*Config `json:"configs"`
}

// Store 管理配置的读写
//
// 线程安全: 所有公开方法都通过 sync.RWMutex 保护（读锁/写锁）
// 写入策略: 原子写入（WriteFile + Rename），防止程序崩溃时数据损坏
type Store struct {
	mu       sync.RWMutex
	filePath string              // 配置文件路径
	configs  map[string]*Config  // name → Config
}

// NewStore 创建配置存储实例
//
// 初始化流程:
// 1. 解析用户目录（~）
// 2. 创建 ~/.llm_tester/ 目录（权限 0700）
// 3. 设置配置文件路径 ~/.llm_tester/configs.json
// 4. 加载已有配置（文件不存在不是错误，首次使用会创建）
//
// 为什么选择 ~/.llm_tester/ 而非项目目录:
// - 配置文件包含 API Key，放在项目目录中可能被误提交到 git
// - 用户可能在多个项目中使用 LLM Tester，共享配置更方便
func NewStore() (*Store, error) {
	s := &Store{
		configs: make(map[string]*Config),
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户目录失败: %w", err)
	}
	configDir := filepath.Join(home, ".llm_tester")
	// 确保目录存在（仅所有者可读写）
	// 0700: 其他用户无法读取目录内容，保护 API Key
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return nil, fmt.Errorf("创建配置目录失败: %w", err)
	}
	s.filePath = filepath.Join(configDir, "configs.json")
	// 加载已有配置（文件不存在不是错误，首次使用会创建）
	if err := s.load(); err != nil {
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

// save 将配置写入文件（原子写入）
//
// 原子写入流程:
// 1. 序列化配置为 JSON
// 2. 写入临时文件 configs.json.tmp（权限 0600）
// 3. Rename 临时文件为 configs.json（原子操作）
//
// 为什么需要原子写入:
// 如果直接写入 configs.json 且写入过程中程序崩溃，
// 文件可能处于半写状态（部分内容、损坏的 JSON），
// 下次启动时无法解析，导致所有配置丢失。
// Rename 在同一个文件系统内是原子操作。
func (s *Store) save() error {
	cfgData := configsData{Configs: s.configs}
	data, err := json.MarshalIndent(cfgData, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	// 原子写入：先写临时文件，再重命名
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}
	return nil
}

// List 返回所有配置的名称列表（按名称排序）
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

// GetAll 返回所有配置（返回副本，防止外部修改内部状态）
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

// ImportJSON 从 JSON 数据导入配置
//
// 支持两种输入格式:
// 1. 单个对象: {"name": "cfg1", "api_type": "openai", ...}
// 2. 对象数组: [{"name": "cfg1", ...}, {"name": "cfg2", ...}]
//
// 导入规则:
// - 同名配置会被覆盖
// - 配置名称为空时跳过
// - 导入后立即持久化到文件
func (s *Store) ImportJSON(data []byte) (int, error) {
	// 尝试解析为数组（批量导入）
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
//
// 注意: 导出内容包含明文 API Key。
// 用户应妥善保管导出的文件，避免泄露。
func (s *Store) ExportJSON() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfgData := configsData{Configs: s.configs}
	data, _ := json.MarshalIndent(cfgData, "", "  ")
	return data
}

// PresetConfig 预设模板定义
// 用于快速填充配置表单，减少用户手动输入
type PresetConfig struct {
	Name         string `json:"name"`          // 预设名称（同时也是配置名称建议）
	APIType      string `json:"api_type"`       // API 类型
	BaseURL      string `json:"base_url"`       // API 基础地址（不应包含 /v1）
	Model        string `json:"model"`          // 推荐模型
	EndpointMode string `json:"endpoint_mode,omitempty"` // 端点模式
}

// Presets 返回预设模板列表
//
// 预设 URL 说明:
// - OpenAI/DeepSeek/Kimi: base_url 不包含 /v1（代码自动添加）
// - GLM-4: 保留 /api/paas/v4（智谱 API 的固有路径）
// - DashScope(通义千问): 保留 /compatible-mode/v1（兼容模式的固有路径）
// - Azure: 独立路径格式，不适用 /v1 规则
// - Ollama: 本地地址，无 /v1
func Presets() []PresetConfig {
	return []PresetConfig{
		{Name: "OpenAI", APIType: "openai", BaseURL: "https://api.openai.com", Model: "gpt-4o-mini", EndpointMode: "chat_completions"},
		{Name: "DeepSeek", APIType: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-chat", EndpointMode: "chat_completions"},
		{Name: "Kimi", APIType: "openai", BaseURL: "https://api.moonshot.cn", Model: "moonshot-v1-8k", EndpointMode: "chat_completions"},
		{Name: "GLM-4", APIType: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Model: "glm-4-flash", EndpointMode: "chat_completions"},
		{Name: "通义千问", APIType: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-turbo", EndpointMode: "chat_completions"},
		{Name: "Ollama", APIType: "ollama", BaseURL: "http://localhost:11434", Model: "llama3", EndpointMode: ""},
		{Name: "Azure", APIType: "azure", BaseURL: "https://YOUR_RESOURCE.openai.azure.com", Model: "gpt-4", EndpointMode: ""},
		{Name: "Custom", APIType: "custom", BaseURL: "", Model: "", EndpointMode: "chat_completions"},
	}
}

// Validate 校验配置必填字段
//
// 校验规则:
// - APIType 和 Name 始终必填
// - openai/azure 类型: BaseURL + APIKey + Model 必填
// - custom 类型: BaseURL 必填
// - ollama 类型: 无额外必填字段（使用默认地址即可）
//
// 注意: 此校验只检查必填字段是否存在，不验证字段值的合法性
// （如 URL 格式、API Key 格式等）
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