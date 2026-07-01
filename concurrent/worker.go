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
func (p *WorkerPool) Start(fn func(reqID int) TestResult) {
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	p.cancel = cancel

	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for reqID := range p.input {
				select {
				case <-ctx.Done():
					return
				default:
					result := fn(reqID)
					p.output <- result
				}
			}
		}()
	}
}

// StartWithTimeout 启动 worker 池并设置超时
func (p *WorkerPool) StartWithTimeout(timeout time.Duration, fn func(reqID int) TestResult) *WorkerPool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	p.ctx = ctx
	p.cancel = cancel

	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for reqID := range p.input {
				select {
				case <-ctx.Done():
					return
				default:
					result := fn(reqID)
					p.output <- result
				}
			}
		}()
	}

	return p
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

// RunBenchmark 并发执行基准测试，返回所有结果
func RunBenchmark(concurrency int, totalReqs int, timeout time.Duration, fn func(reqID int) TestResult) []TestResult {
	if totalReqs <= 0 {
		return nil
	}

	pool := NewWorkerPool(concurrency)
	pool.StartWithTimeout(timeout, fn)

	// 提交所有任务
	for i := 0; i < totalReqs; i++ {
		pool.input <- i
	}

	pool.Shutdown()
	return pool.CollectResults()
}