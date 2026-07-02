package probe

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/qwdingyu/llm-tester/concurrent"
	"github.com/qwdingyu/llm-tester/llm"
)

func init() { Register(&BenchmarkProbe{}) }

// BenchmarkProbe 基准测试探针
//
// 测试内容: 多轮并发请求，统计 P50/P95/标准差/成功率的延迟分布
// 评分基准: P95 延迟 ≤ 2s → 100分, ≥ 30s → 0分, 线性插值
// 首批请求为预热轮次（不计入统计），消除冷启动影响
type BenchmarkProbe struct{}

func (p *BenchmarkProbe) Name() string        { return "benchmark" }
func (p *BenchmarkProbe) Description() string { return "并发多轮测速（P50/P95/成功率）" }

func (p *BenchmarkProbe) Run(ctx context.Context, provider llm.Provider, cfg Config) *Result {
	rounds := cfg.Rounds
	if rounds <= 0 {
		rounds = 5
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	timeout := 60 * time.Second

	// 预热：发送 1 次请求，结果不计入统计
	_ = provider.Chat(ctx, MakeChatRequest(cfg))

	// 正式测试
	results := concurrent.RunBenchmark(ctx, concurrency, rounds, timeout, func(reqID int) concurrent.TestResult {
		chatResp := provider.Chat(ctx, MakeChatRequest(cfg))
		return toTestResult(reqID, chatResp)
	})

	return analyzeBenchmark(results)
}

// toTestResult 将 ChatResponse 转换为 TestResult
func toTestResult(reqID int, resp *llm.ChatResponse) concurrent.TestResult {
	r := concurrent.TestResult{ReqID: reqID, Status: 200, LatencyMs: 0}
	if resp == nil {
		r.Status = 500
		r.Error = "空响应"
		return r
	}
	r.LatencyMs = resp.LatencyMs
	r.PromptTokens = resp.PromptTokens
	r.CompletionToks = resp.CompletionToks
	r.TotalTokens = resp.TotalTokens
	r.FinishReason = resp.FinishReason
	r.ContentLength = len(resp.Content)
	if resp.Error != "" {
		r.Status = 500
		r.Error = resp.Error
	}
	return r
}

// analyzeBenchmark 分析基准测试结果
func analyzeBenchmark(results []concurrent.TestResult) *Result {
	r := &Result{
		Metrics: map[string]float64{},
	}

	// 按状态分组
	var successLatencies []float64
	successCount := 0
	for _, res := range results {
		if res.Status == 200 {
			successCount++
			successLatencies = append(successLatencies, res.LatencyMs)
		}
	}

	total := len(results)
	if total == 0 {
		r.Summary = "无有效结果"
		r.Score = 0
		return r
	}

	// 成功率和延迟统计
	successRate := float64(successCount) / float64(total) * 100
	r.Metrics["success_rate"] = math.Round(successRate*100) / 100
	r.Metrics["total"] = float64(total)
	r.Metrics["success"] = float64(successCount)
	r.Metrics["failed"] = float64(total - successCount)

	if successCount == 0 {
		r.Score = 0
		r.Summary = fmt.Sprintf("共 %d 次请求，全部失败", total)
		return r
	}

	// 延迟统计
	sort.Float64s(successLatencies)
	avg := average(successLatencies)
	min := successLatencies[0]
	max := successLatencies[len(successLatencies)-1]
	p50 := percentile(successLatencies, 50)
	p95 := percentile(successLatencies, 95)

	r.LatencyMs = avg
	r.Metrics["avg"] = math.Round(avg*100) / 100
	r.Metrics["min"] = math.Round(min*100) / 100
	r.Metrics["max"] = math.Round(max*100) / 100
	r.Metrics["p50"] = math.Round(p50*100) / 100
	r.Metrics["p95"] = math.Round(p95*100) / 100
	r.Metrics["stdev"] = math.Round(stddev(successLatencies, avg)*100) / 100

	// 评分：P95 ≤ 2s → 100分, ≥ 30s → 0分, 线性插值
	score := 100.0
	if p95 > 2000 {
		score = math.Max(0, 100-(p95-2000)/28000*100)
	}
	// 成功率扣分
	if successRate < 100 {
		score *= successRate / 100
	}
	r.Score = math.Round(score*100) / 100
	r.Summary = fmt.Sprintf("P50=%.0fms P95=%.0fms 成功率%.0f%%", p50, p95, successRate)

	return r
}

func average(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stddev(v []float64, avg float64) float64 {
	s := 0.0
	for _, x := range v {
		s += (x - avg) * (x - avg)
	}
	return math.Sqrt(s / float64(len(v)))
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * pct / 100)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}