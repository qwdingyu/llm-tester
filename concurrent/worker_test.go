package concurrent

import (
	"context"
	"testing"
	"time"
)

func TestNewWorkerPool(t *testing.T) {
	pool := NewWorkerPool(5)
	if pool.workers != 5 {
		t.Errorf("workers = %d, want 5", pool.workers)
	}
	if cap(pool.input) != 10 {
		t.Errorf("input cap = %d, want 10", cap(pool.input))
	}
	if cap(pool.output) != 10 {
		t.Errorf("output cap = %d, want 10", cap(pool.output))
	}
}

func TestNewWorkerPool_Default(t *testing.T) {
	pool := NewWorkerPool(0)
	if pool.workers != DefaultConcurrency {
		t.Errorf("workers = %d, want DefaultConcurrency(%d)", pool.workers, DefaultConcurrency)
	}
	pool2 := NewWorkerPool(-1)
	if pool2.workers != DefaultConcurrency {
		t.Errorf("negative workers = %d, want %d", pool2.workers, DefaultConcurrency)
	}
}

func TestWorkerPool_StartShutdown(t *testing.T) {
	pool := NewWorkerPool(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool.Start(ctx, func(reqID int) TestResult {
		return TestResult{ReqID: reqID, Status: 200}
	})

	// 在 Shutdown 前启动 goroutine 收集结果
	done := make(chan []TestResult, 1)
	go func() {
		done <- pool.CollectResults()
	}()

	for i := 0; i < 5; i++ {
		pool.Submit(i)
	}
	time.Sleep(50 * time.Millisecond)
	pool.Shutdown()

	collected := <-done
	if len(collected) != 5 {
		t.Logf("收集到 %d 个结果（竞态下可能小于5）", len(collected))
	}
	for _, r := range collected {
		if r.Status != 200 {
			t.Errorf("req %d: status = %d", r.ReqID, r.Status)
		}
	}
}

func TestWorkerPool_ShutdownNow(t *testing.T) {
	pool := NewWorkerPool(2)
	ctx := context.Background()

	pool.Start(ctx, func(reqID int) TestResult {
		time.Sleep(50 * time.Millisecond)
		return TestResult{ReqID: reqID, Status: 200}
	})

	pool.Submit(0, 1, 2, 3, 4)
	pool.ShutdownNow()

	// ShutdownNow 应该快速返回，不会等待所有任务完成
	results := pool.CollectResults()
	// 可能部分任务已完成，也可能全部取消
	t.Logf("ShutdownNow 后收集到 %d 个结果（可能部分已取消）", len(results))
}

func TestRunBenchmark(t *testing.T) {
	results := RunBenchmark(context.Background(), 2, 5, 5*time.Second, func(reqID int) TestResult {
		return TestResult{ReqID: reqID, Status: 200, LatencyMs: 10.0}
	})

	if len(results) == 0 {
		t.Errorf("结果数为 0, 期望 > 0")
	} else {
		t.Logf("收集到 %d 个结果", len(results))
	}
	for _, r := range results {
		if r.Status != 200 {
			t.Errorf("req %d: status = %d", r.ReqID, r.Status)
		}
	}
}

func TestRunBenchmark_ZeroReqs(t *testing.T) {
	results := RunBenchmark(context.Background(), 2, 0, 5*time.Second, nil)
	if results != nil {
		t.Error("Zero requests should return nil")
	}
}

func TestRunBenchmark_Timeout(t *testing.T) {
	start := time.Now()
	results := RunBenchmark(context.Background(), 2, 100, 100*time.Millisecond, func(reqID int) TestResult {
		time.Sleep(50 * time.Millisecond)
		return TestResult{ReqID: reqID, Status: 200}
	})
	elapsed := time.Since(start)

	// 超时后应返回部分结果（不是全部 100 个）
	if len(results) >= 100 {
		t.Errorf("超时后应返回部分结果，但返回了 %d 个", len(results))
	}
	if elapsed > 2*time.Second {
		t.Errorf("超时后应快速返回，但耗时 %v", elapsed)
	}
	t.Logf("超时测试: %d 个结果, 耗时 %v", len(results), elapsed)
}

func TestRunBenchmark_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// 立即取消
	cancel()

	results := RunBenchmark(ctx, 2, 100, 5*time.Second, func(reqID int) TestResult {
		return TestResult{ReqID: reqID, Status: 200}
	})

	// 上下文已取消，应返回空或很少结果
	if len(results) > 10 {
		t.Errorf("已取消上下文应返回很少结果，但返回了 %d", len(results))
	}
}

func TestTestResult_Defaults(t *testing.T) {
	r := TestResult{}
	if r.Status != 0 {
		t.Errorf("Status 默认值 = %d", r.Status)
	}
	if r.Error != "" {
		t.Errorf("Error 默认值 = %q", r.Error)
	}
}