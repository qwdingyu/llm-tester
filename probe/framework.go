// Package probe 提供可扩展的 LLM API 测试探针框架。
//
// 架构:
//   Probe 接口 — 单个测试探针（如 json_mode 测试）
//   Suite — 多配置 × 多探针的执行引擎
//   Result — 统一测试结果
//
// 使用方式:
//
//	suite := &probe.Suite{
//	    Targets: []probe.Target{{Name: "cfg1", Provider: p1, Config: probe.Config{...}}},
//	    Probes:  []probe.Probe{&probe.BenchmarkProbe{}, &probe.JsonModeProbe{}},
//	}
//	result := suite.Run(ctx)
//
// 设计原则:
// - Probe 不依赖 HTTP/Gin，只依赖 llm.Provider 接口
// - Suite 自动管理多个 Target 和多个 Probe 的笛卡尔积执行
// - 结果自动对比分析（最佳配置、差异百分比）
// - 新增探针只需实现 Probe 接口并注册到 Registry
package probe

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/qwdingyu/llm-tester/llm"
)

// ─── 核心类型 ───────────────────────────────────────

// Result 一个配置 × 一个探针的测试结果
type Result struct {
	ProbeName  string             `json:"probe_name"`   // 探针名称
	ConfigName string             `json:"config_name"`  // 配置名称
	Success    bool               `json:"success"`      // 测试是否通过
	Score      float64            `json:"score"`        // 评分 0-100, -1=不可用
	LatencyMs  float64            `json:"latency_ms"`   // 平均延迟（毫秒）
	Metrics    map[string]float64 `json:"metrics"`      // 自定义指标
	Summary    string             `json:"summary"`      // 一句话结论
	RawData    interface{}        `json:"raw,omitempty"` // 原始数据（前端展示用）
	Error      string             `json:"error,omitempty"`
}

// Config 探针执行参数
type Config struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	Prompt    string                 `json:"prompt"`
	Rounds    int                    `json:"rounds"`    // 基准测试轮次
	Concurrency int                  `json:"concurrency"` // 基准测试并发数
	Params    map[string]interface{} `json:"params,omitempty"` // 探针特定参数
}

// Target 一个待测配置
type Target struct {
	Name     string      // 配置显示名称
	Provider llm.Provider // LLM API provider
	Config   Config      // 探针参数
}

// ─── Probe 接口 ─────────────────────────────────────

// Probe 定义单个测试探针
// 实现此接口即可添加新的测试类型，无需修改框架代码
type Probe interface {
	// Name 返回探针唯一标识（如 "benchmark"、"json_mode"）
	Name() string

	// Description 返回人类可读的描述
	Description() string

	// Run 执行探针测试，返回统一结果
	Run(ctx context.Context, provider llm.Provider, cfg Config) *Result
}

// ─── Registry 探针注册表 ────────────────────────────

// Registry 是全局探针注册表
// 新增探针在 init() 中注册，或由 Suite 显式指定
var Registry = map[string]Probe{}

// Register 注册探针到全局 Registry
func Register(p Probe) {
	Registry[p.Name()] = p
}

// ─── Suite 执行引擎 ─────────────────────────────────

// Suite 测试套件 = Targets × Probes
// 自动并发执行所有组合，汇总结果，计算对比
type Suite struct {
	Targets []Target // 待测配置列表
	Probes  []Probe  // 探针列表
}

// SuiteResult 套件执行结果
type SuiteResult struct {
	Items []*Result `json:"items"` // 每个 Target × Probe 的结果
	Comparison *Comparison `json:"comparison,omitempty"` // 自动对比分析
	DurationMs float64 `json:"duration_ms"` // 总耗时
}

// Comparison 对比分析摘要
type Comparison struct {
	// 每个指标的最佳配置名
	BestOf map[string]string `json:"best_of"`
	// 配置间差异百分比 (probe_name → 相对差异)
	DiffPct map[string]float64 `json:"diff_pct"`
}

// Run 执行套件中的所有探针
//
// 执行策略:
// 1. 如果只有一个 Target，顺序执行所有 Probes
// 2. 如果有多个 Targets，并发执行 Targets（每个 Target 顺序执行 Probes）
// 3. 所有结果收集完毕后，自动计算对比分析
// 4. 单个 Probe 失败不影响其他 Probe 执行
func (s *Suite) Run(ctx context.Context) *SuiteResult {
	start := time.Now()
	totalResults := make([]*Result, 0, len(s.Targets)*len(s.Probes))

	var mu sync.Mutex
	var wg sync.WaitGroup

	// 对每个 Target 并发执行（Target 内部的 Probes 顺序执行）
	for _, target := range s.Targets {
		wg.Add(1)
		go func(t Target) {
			defer wg.Done()
			for _, p := range s.Probes {
				select {
				case <-ctx.Done():
					return
				default:
					result := p.Run(ctx, t.Provider, t.Config)
					if result != nil {
						result.ConfigName = t.Name
						result.ProbeName = p.Name()
						// 默认值填充
						if result.Metrics == nil {
							result.Metrics = map[string]float64{}
						}
						mu.Lock()
						totalResults = append(totalResults, result)
						mu.Unlock()
					}
				}
			}
		}(target)
	}

	wg.Wait()
	duration := time.Since(start).Seconds() * 1000

	// 计算对比分析（至少需要 2 个 Target）
	var comp *Comparison
	if len(s.Targets) >= 2 && len(totalResults) > 0 {
		comp = computeComparison(totalResults)
	}

	return &SuiteResult{
		Items:      totalResults,
		Comparison: comp,
		DurationMs: duration,
	}
}

// computeComparison 计算多配置对比分析
func computeComparison(results []*Result) *Comparison {
	comp := &Comparison{
		BestOf: make(map[string]string),
		DiffPct: make(map[string]float64),
	}

	// 按 probe_name 分组
	type probeGroup struct {
		name    string
		results []*Result
	}
	groups := map[string][]*Result{}
	for _, r := range results {
		groups[r.ProbeName] = append(groups[r.ProbeName], r)
	}

	for probeName, rs := range groups {
		if len(rs) < 2 {
			continue
		}
		// 按 Score 降序排列，取最高分
		sort.Slice(rs, func(i, j int) bool {
			return rs[i].Score > rs[j].Score
		})
		best := rs[0]
		second := rs[1]
		comp.BestOf[probeName] = best.ConfigName + fmt.Sprintf(" (%.0f分)", best.Score)

		// 计算差异百分比
		if second.Score > 0 {
			comp.DiffPct[probeName] = math.Round((best.Score-second.Score)/second.Score*100) / 100
		}
	}

	return comp
}

// ─── 辅助函数 ───────────────────────────────────────

// MakeChatRequest 从 ProbeConfig 创建 ChatRequest
// 所有探针通过此函数统一构造 ChatRequest，避免重复代码
func MakeChatRequest(cfg Config) *llm.ChatRequest {
	return &llm.ChatRequest{
		Model:       cfg.Model,
		Message:     cfg.Prompt,
		MaxTokens:   cfg.MaxTokens,
		Temperature: 0.0,
	}
}