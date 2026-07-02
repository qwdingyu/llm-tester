package probe

import (
	"context"
	"fmt"

	"github.com/qwdingyu/llm-tester/llm"
)

func init() { Register(&MultiTurnProbe{}) }

// MultiTurnProbe 多轮对话测试探针
//
// 测试内容:
// 1. 第一轮: 发送"我的名字是张三"，验证响应
// 2. 第二轮: 发送"我叫什么名字？"，验证模型是否能记住上文的姓名
// 3. 第三轮: 发送"我刚才说了什么？"，验证长程记忆
//
// 评分基准:
// - 能正确回答姓名: 60 分
// - 能正确复述上文: +40 分
// - 延迟 > 20s 扣 20 分
type MultiTurnProbe struct{}

func (p *MultiTurnProbe) Name() string        { return "multi_turn" }
func (p *MultiTurnProbe) Description() string { return "多轮对话记忆一致性" }

func (p *MultiTurnProbe) Run(ctx context.Context, provider llm.Provider, cfg Config) *Result {
	r := &Result{Metrics: map[string]float64{}}

	// 第1轮: 自我介绍
	chatReq := MakeChatRequest(cfg)
	chatReq.Message = "你好！我的名字是张三，很高兴认识你。"
	resp1 := provider.Chat(ctx, chatReq)
	if resp1 == nil || resp1.Error != "" {
		errMsg := "空响应"
		if resp1 != nil {
			errMsg = resp1.Error
		}
		r.Error = fmt.Sprintf("第1轮失败: %s", errMsg)
		return r
	}
	r.LatencyMs += resp1.LatencyMs * 0.3

	// 第2轮: 测试记忆（带上第1轮的上下文）
	chatReq2 := MakeChatRequest(cfg)
	chatReq2.Messages = []llm.Message{
		{Role: "user", Content: "你好！我的名字是张三，很高兴认识你。"},
		{Role: "assistant", Content: resp1.Content},
		{Role: "user", Content: "我叫什么名字？请你只回答名字，不要其他内容。"},
	}
	resp2 := provider.Chat(ctx, chatReq2)
	if resp2 == nil || resp2.Error != "" {
		errMsg := "空响应"
		if resp2 != nil {
			errMsg = resp2.Error
		}
		r.Error = fmt.Sprintf("第2轮失败: %s", errMsg)
		return r
	}
	r.LatencyMs += resp2.LatencyMs * 0.3

	// 第3轮: 测试长程记忆
	chatReq3 := MakeChatRequest(cfg)
	chatReq3.Messages = []llm.Message{
		{Role: "user", Content: "你好！我的名字是张三，很高兴认识你。"},
		{Role: "assistant", Content: resp1.Content},
		{Role: "user", Content: "我叫什么名字？请你只回答名字，不要其他内容。"},
		{Role: "assistant", Content: resp2.Content},
		{Role: "user", Content: "我刚才说的第一句话是什么？请简短复述核心内容。"},
	}
	resp3 := provider.Chat(ctx, chatReq3)
	if resp3 == nil || resp3.Error != "" {
		errMsg := "空响应"
		if resp3 != nil {
			errMsg = resp3.Error
		}
		r.Error = fmt.Sprintf("第3轮失败: %s", errMsg)
		return r
	}
	r.LatencyMs += resp3.LatencyMs * 0.4

	// 评分
	score := 0.0
	rememberName := containsAny(resp2.Content, []string{"张三", "zhangsan", "Zhang San"})
	rememberContext := containsAny(resp3.Content, []string{"张三", "名字", "认识"})

	remembered := 0
	if rememberName {
		remembered++
		score += 60
	} else {
		r.Metrics["name_remembered"] = 0
	}
	if rememberContext {
		remembered++
		score += 40
	}

	r.Metrics["turns_memory"] = float64(remembered)
	r.Metrics["turns_total"] = 2
	r.Metrics["turns_ratio"] = float64(remembered) / 2.0 * 100

	if r.LatencyMs > 20000 {
		score -= 20
	}

	r.Score = score
	r.Success = score > 0
	r.Summary = fmt.Sprintf("记忆轮次 %d/2", remembered)

	// 原始响应供前端展示
	r.RawData = map[string]string{
		"round1": truncate(resp1.Content, 100),
		"round2": truncate(resp2.Content, 100),
		"round3": truncate(resp3.Content, 100),
	}
	r.Summary = fmt.Sprintf("记忆轮次 %d/2", remembered)
	return r
}

func containsAny(s string, keywords []string) bool {
	lower := toLower(s)
	for _, k := range keywords {
		if contains(lower, toLower(k)) {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsInner(s, substr)
}

func containsInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
