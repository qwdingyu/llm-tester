package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qwdingyu/llm-tester/llm"
)

func init() { Register(&JsonModeProbe{}) }

// JsonModeProbe JSON 模式测试探针
//
// 测试内容:
// 1. 发送一个要求 JSON 输出的请求，设置 response_format={type:"json_object"}
// 2. 验证响应是否为合法 JSON
// 3. 验证是否包含预期字段
// 4. 验证响应时间
//
// 评分基准:
// - 合法 JSON: 60 分（基础）; 非法 JSON: 0 分
// - 包含预期 key: +40 分
// - 延迟扣分: > 10s 扣 20 分
type JsonModeProbe struct{}

func (p *JsonModeProbe) Name() string        { return "json_mode" }
func (p *JsonModeProbe) Description() string { return "JSON 结构输出可靠性" }

func (p *JsonModeProbe) Run(ctx context.Context, provider llm.Provider, cfg Config) *Result {
	r := &Result{Metrics: map[string]float64{}}

	// JSON 模式请求
	chatReq := MakeChatRequest(cfg)
	chatReq.ResponseFormat = "json_object"
	chatReq.Message = `请生成一份 JSON，包含以下字段: name(string), age(number), city(string)，只返回 JSON 不要其他内容。`

	resp := provider.Chat(ctx, chatReq)
	if resp == nil {
		r.Error = "provider.Chat 返回 nil"
		return r
	}
	if resp.Error != "" {
		r.Error = resp.Error
		return r
	}

	r.LatencyMs = resp.LatencyMs

	// 验证是否为合法 JSON
	content := strings.TrimSpace(resp.Content)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		// 有些 API 会在 JSON 外包 ```json 标记
		cleaned := stripJSONFence(content)
		if err2 := json.Unmarshal([]byte(cleaned), &parsed); err2 != nil {
			r.Score = 0
			r.Summary = "返回内容不是合法 JSON"
			r.RawData = map[string]string{"raw": truncate(content, 200)}
			return r
		}
		content = cleaned
	}

	// 验证预期字段
	expectedKeys := []string{"name", "age", "city"}
	foundKeys := 0
	for _, k := range expectedKeys {
		if _, ok := parsed[k]; ok {
			foundKeys++
		}
	}

	r.Metrics["expected_keys_found"] = float64(foundKeys)
	r.Metrics["expected_keys_total"] = float64(len(expectedKeys))
	r.RawData = map[string]interface{}{
		"parsed": parsed,
		"raw":    truncate(content, 200),
	}

	// 评分
	baseScore := 60.0 // 合法 JSON 基础分
	if foundKeys == len(expectedKeys) {
		baseScore += 40 // 全部字段匹配
	} else {
		baseScore += float64(foundKeys) / float64(len(expectedKeys)) * 40
	}
	// 延迟扣分
	if r.LatencyMs > 10000 {
		baseScore -= 20
	}
	if baseScore < 0 {
		baseScore = 0
	}
	r.Score = baseScore

	r.Success = true
	r.Summary = fmt.Sprintf("JSON 合法 ✅ 字段匹配 %d/%d", foundKeys, len(expectedKeys))
	return r
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}