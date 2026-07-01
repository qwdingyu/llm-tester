// Package concurrent 提供并发工作池，用于并发执行 LLM API 测试。
//
// 设计说明:
// - WorkerPool 管理一组 goroutine worker，通过 channel 分发任务
// - 支持超时控制（context.WithTimeout）和优雅关闭
// - 所有 channel 操作都使用 select + ctx.Done() 保护，防止 goroutine 泄漏
//
// 使用场景:
// - 批量测试（多个配置同时测试）
// - 基准测试（单个配置多轮并发）
//
// 注意:
// - RunBenchmark 接受 context.Context 参数，支持 HTTP 请求取消传播
// - 不要在 RunBenchmark 之外直接使用 WorkerPool，除非你对 goroutine 生命周期有完全控制
package concurrent

import (
	"context"
	"sync"
	"time"
)

const (
	// DefaultConcurrency 默认并发数
	DefaultConcurrency = 5
)

// TestResult 表示一次 LLM 测试的结果
//
// 所有耗时字段使用毫秒（ms）为单位，与前端展示一致。
// 当 Status != 200 时，Error 字段包含错误描述。
// ContentPreview 是回复内容的前 100 个字符，用于快速预览。
type TestResult struct {
	ReqID          int     `json:"req_id"`          // 请求序号，从 0 开始
	Model          string  `json:"model"`            // 模型名称（或配置名）
	Status         int     `json:"status"`           // HTTP 状态码，200=成功
	LatencyMs      float64 `json:"latency_ms"`       // 延迟（毫秒）
	FinishReason   string  `json:"finish_reason"`    // 结束原因: stop / length
	ContentLength  int     `json:"content_length"`   // 回复内容长度（字符数）
	ContentPreview string  `json:"content_preview"`  // 内容预览（前 100 字）
	PromptTokens   int     `json:"prompt_tokens"`    // 输入 token 数
	CompletionToks int     `json:"completion_tokens"` // 输出 token 数
	TotalTokens    int     `json:"total_tokens"`     // 总 token 数
	Error          string  `json:"error,omitempty"`  // 错误信息（仅失败时）
}

// WorkerPool 管理一组并发 worker，用于执行 LLM 测试任务
//
// 架构:
//   - input channel: 接收任务 ID（int）
//   - output channel: 发送测试结果（TestResult）
//   - N 个 goroutine: 从 input 读取 ID，执行 fn，结果写入 output
//
// 生命周期:
//   Start / StartWithTimeout → Submit → Shutdown / ShutdownNow
//   Shutdown 后不能再 Start，需要重新 NewWorkerPool
type WorkerPool struct {
	workers int
	input   chan int          // 任务队列，buffer 大小为 concurrency*2
	output  chan TestResult   // 结果队列，buffer 大小为 concurrency*2
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	ctx     context.Context
}

// NewWorkerPool 创建指定并发数的 worker 池
//
// concurrency 为 0 或负数时使用 DefaultConcurrency（5）。
// input 和 output 的 buffer 设为 concurrency*2，
// 避免在提交/消费速度不匹配时频繁阻塞。
func NewWorkerPool(concurrency int) *WorkerPool {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	return &WorkerPool{
		workers: concurrency,
		input:   make(chan int, concurrency*2),
		output:  make(chan TestResult, concurrency*2),
	}
}

// Start 启动 worker 池，fn 接收 req_id 返回测试结果
//
// ctx 用于控制 worker 的生命周期:
// - ctx 取消时，worker 在完成当前任务后退出
// - 不会中断正在执行的任务（fn 内部需要自行处理 ctx）
func (p *WorkerPool) Start(ctx context.Context, fn func(reqID int) TestResult) {
	p.ctx, p.cancel = context.WithCancel(ctx)

	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for reqID := range p.input {
				select {
				case <-p.ctx.Done():
					return
				default:
					result := fn(reqID)
					// 用 select + ctx.Done 保护 output 写入
					// 防止超时后 output buffer 满导致 worker 永久阻塞
					// 这是 WorkerPool 防止 goroutine 泄漏的关键设计
					select {
					case p.output <- result:
					case <-p.ctx.Done():
						return
					}
				}
			}
		}()
	}
}

// Submit 提交任务到工作队列
//
// 使用 select + ctx.Done 保护:
// - 如果 ctx 已取消，不会阻塞写入
// - 调用方可以通过 ctx 取消来提前终止提交
func (p *WorkerPool) Submit(reqIDs ...int) {
	for _, id := range reqIDs {
		select {
		case p.input <- id:
		case <-p.ctx.Done():
			return
		}
	}
}

// Results 返回结果 channel
//
// 调用方通过 range 读取结果:
//
//	for result := range pool.Results() {
//	    // 处理结果
//	}
func (p *WorkerPool) Results() <-chan TestResult {
	return p.output
}

// Shutdown 优雅关闭 worker 池
//
// 关闭 input channel → worker 退出 range 循环 → 等待所有 worker 完成 → 关闭 output channel
// 调用方应通过 range Results() 读取所有结果
func (p *WorkerPool) Shutdown() {
	close(p.input)
	p.wg.Wait()
	close(p.output)
}

// ShutdownNow 立即取消所有 worker
//
// 与 Shutdown 的区别:
// - Shutdown: 等待所有正在执行的任务完成
// - ShutdownNow: 取消 ctx，worker 在下次检查 ctx.Done() 时退出
//
// 注意: ShutdownNow 不会中断正在执行的 fn（Go 的 goroutine 无法强制终止）
func (p *WorkerPool) ShutdownNow() {
	if p.cancel != nil {
		p.cancel()
	}
	p.Shutdown()
}

// CollectResults 收集所有结果到切片
//
// 在 Shutdown 后调用，output channel 已关闭。
// 如果 output 未关闭，range 会永久阻塞。
func (p *WorkerPool) CollectResults() []TestResult {
	var results []TestResult
	for result := range p.output {
		results = append(results, result)
	}
	return results
}

// RunBenchmark 并发执行基准测试，返回所有结果。
//
// 参数:
//   - ctx: 用于传播上游取消信号（如 HTTP 客户端断开）
//   - concurrency: 并发数
//   - totalReqs: 总请求数
//   - timeout: 整体超时（从调用开始计时）
//   - fn: 执行函数，接收 reqID 返回 TestResult
//
// 生命周期:
// 1. 创建 WorkerPool
// 2. 用父 ctx 创建带超时的子 ctx（父 ctx 取消也会传播至此）
// 3. 启动 worker
// 4. 提交所有任务（若 ctx 已取消则提前退出）
// 5. 优雅关闭 → 收集结果 → 返回
//
// 超时处理:
// 如果 timeout 超时或 ctx 取消，已提交但未完成的任务不会返回结果。
// 调用方可以通过检查 results 数量与 totalReqs 的差异来判断是否有任务被取消。
func RunBenchmark(ctx context.Context, concurrency int, totalReqs int, timeout time.Duration, fn func(reqID int) TestResult) []TestResult {
	if totalReqs <= 0 {
		return nil
	}

	pool := NewWorkerPool(concurrency)

	// 用父 ctx 创建带超时的子 ctx
	// 父 ctx 取消（客户端断开）也会传播至此
	benchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool.Start(benchCtx, fn)

	// 提交所有任务，若 ctx 已取消则提前退出
	// 使用 select + ctx.Done 保护，防止超时后向已无消费者的 input 写入导致永久阻塞
	for i := 0; i < totalReqs; i++ {
		select {
		case pool.input <- i:
		case <-benchCtx.Done():
			// 超时或客户端断开，停止提交
			pool.Shutdown()
			return pool.CollectResults()
		}
	}

	pool.Shutdown()
	return pool.CollectResults()
}