// Package concurrent 提供并发工作池，用于并发执行 LLM API 测试。
// 复用 token-refresher-gui/concurrent 的设计模式。
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
type TestResult struct {
	ReqID          int     `json:"req_id"`
	Model          string  `json:"model"`
	Status         int     `json:"status"`
	LatencyMs      float64 `json:"latency_ms"`
	FinishReason   string  `json:"finish_reason"`
	ContentLength  int     `json:"content_length"`
	ContentPreview string  `json:"content_preview"`
	PromptTokens   int     `json:"prompt_tokens"`
	CompletionToks int     `json:"completion_tokens"`
	TotalTokens    int     `json:"total_tokens"`
	Error          string  `json:"error,omitempty"`
}

// WorkerPool 管理一组并发 worker，用于执行 LLM 测试任务
type WorkerPool struct {
	workers int
	input   chan int // req_id
	output  chan TestResult
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	ctx     context.Context
}

// NewWorkerPool 创建指定并发数的 worker 池
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
					// 用 select + ctx.Done 保护 output 写入，防止超时后阻塞泄漏
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
func (p *WorkerPool) Results() <-chan TestResult {
	return p.output
}

// Shutdown 优雅关闭 worker 池
func (p *WorkerPool) Shutdown() {
	close(p.input)
	p.wg.Wait()
	close(p.output)
}

// ShutdownNow 立即取消所有 worker
func (p *WorkerPool) ShutdownNow() {
	if p.cancel != nil {
		p.cancel()
	}
	p.Shutdown()
}

// CollectResults 收集所有结果到切片
func (p *WorkerPool) CollectResults() []TestResult {
	var results []TestResult
	for result := range p.output {
		results = append(results, result)
	}
	return results
}

// RunBenchmark 并发执行基准测试，返回所有结果。
// ctx 用于传播上游取消信号（如 HTTP 客户端断开），timeout 为整体超时。
func RunBenchmark(ctx context.Context, concurrency int, totalReqs int, timeout time.Duration, fn func(reqID int) TestResult) []TestResult {
	if totalReqs <= 0 {
		return nil
	}

	pool := NewWorkerPool(concurrency)

	// 用父 ctx 创建带超时的子 ctx，父 ctx 取消也会传播至此
	benchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool.Start(benchCtx, fn)

	// 提交所有任务，若 ctx 已取消则提前退出
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