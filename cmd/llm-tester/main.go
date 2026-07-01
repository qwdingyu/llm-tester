// Package main 是 LLM Tester GUI 的主入口。
//
// 架构说明:
// 1. Gin Web 框架处理 HTTP 路由，所有 API 返回 JSON
// 2. 前端 Vue 3 SPA 通过 go:embed 嵌入到二进制中，单文件部署
// 3. 配置持久化到 ~/.llm_tester/configs.json
// 4. 测试结果通过 SSE（Server-Sent Events）流式推送
//
// 路由分组:
// - /api/configs/* — 配置管理（CRUD + 导入导出）
// - /api/test/* — 测试 API（连接/聊天/批量/基准/Burn）
// - /api/logs/* — 操作日志
// - 其他路径 — 返回前端 SPA 的 index.html（SPA 路由）
//
// 基于 token-refresher-gui 的 Gin + Vue 3 SPA 架构模式
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"embed"

	"github.com/user/llm-tester/concurrent"
	"github.com/user/llm-tester/llm"
	"github.com/user/llm-tester/storage"
)

//go:embed static
var staticFS embed.FS

var (
	logLines []string
	logMu    sync.Mutex
	store    *storage.Store
)

// addLog 添加日志行（线程安全）
func addLog(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	logLines = append(logLines, time.Now().Format("15:04:05")+" "+fmt.Sprintf(format, args...))
	if len(logLines) > 500 {
		logLines = logLines[len(logLines)-500:]
	}
}

func main() {
	// 设置 Gin 为 Release 模式，关闭调试日志
	gin.SetMode(gin.ReleaseMode)

	// 初始化配置存储
	// 如果目录创建失败（如权限不足），直接 panic 阻止启动
	var err error
	store, err = storage.NewStore()
	if err != nil {
		panic(fmt.Sprintf("初始化配置存储失败: %v", err))
	}

	// serveIndex 返回嵌入的前端 SPA
	// 所有非 /api/ 路径都返回 index.html（SPA 路由）
	// 读取失败时记录日志并返回 500，而非静默返回空页面
	serveIndex := func(c *gin.Context) {
		data, err := staticFS.ReadFile("static/index.html")
		if err != nil {
			addLog("❌ 读取前端静态文件失败: %v", err)
			c.String(http.StatusInternalServerError, "前端页面加载失败")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	}

	// 创建 Gin 路由器（无默认中间件）
	// 跳过 benchmark 和 burn 测试的访问日志，避免 SSE 推送时日志刷屏
	r := gin.New()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/api/test/benchmark", "/api/test/burn"},
	}))

	// 配置管理 API
	r.GET("/api/configs", handleListConfigs)
	r.POST("/api/configs", handleSaveConfig)
	r.DELETE("/api/configs/:name", handleDeleteConfig)
	r.GET("/api/configs/presets", handleGetPresets)
	r.POST("/api/configs/import", handleImportConfigs)
	r.GET("/api/configs/export", handleExportConfigs)

	// 测试 API
	r.POST("/api/test/connection", handleTestConnection)
	r.POST("/api/test/chat", handleChat)
	r.POST("/api/test/batch", handleBatchTest)
	r.POST("/api/test/benchmark", handleBenchmark)
	r.POST("/api/test/burn", handleBurnTest)
	r.POST("/api/test/models", handleListModels)

	// 日志 API
	r.GET("/api/logs", handleLogs)
	r.DELETE("/api/logs", handleClearLogs)

	// SPA: all non-API routes serve index.html
	r.NoRoute(func(c *gin.Context) {
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") {
			serveIndex(c)
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8912"
	}
	fmt.Printf("🌐 LLM Tester GUI → http://localhost:%s\n", port)
	r.Run(":" + port)
}

// handleListConfigs 获取配置列表
func handleListConfigs(c *gin.Context) {
	names := store.List()
	configs := make(map[string]*storage.Config)
	for _, name := range names {
		configs[name] = store.Get(name)
	}
	c.JSON(http.StatusOK, gin.H{"configs": configs, "names": names})
}

// handleSaveConfig 保存配置
func handleSaveConfig(c *gin.Context) {
	var cfg storage.Config
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := cfg.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := store.Save(cfg.Name, &cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	addLog("💾 已保存配置: %s", cfg.Name)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("已保存配置: %s", cfg.Name)})
}

// handleDeleteConfig 删除配置
func handleDeleteConfig(c *gin.Context) {
	name := c.Param("name")
	if err := store.Delete(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败: " + err.Error()})
		return
	}
	addLog("🗑️ 已删除配置: %s", name)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("已删除配置: %s", name)})
}

// handleGetPresets 获取预设模板
func handleGetPresets(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"presets": storage.Presets()})
}

// handleImportConfigs 导入配置
func handleImportConfigs(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1024*1024))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求体失败"})
		return
	}
	count, err := store.ImportJSON(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	addLog("📥 已导入 %d 个配置", count)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": fmt.Sprintf("已导入%d个配置", count)})
}

// handleExportConfigs 导出配置
func handleExportConfigs(c *gin.Context) {
	data := store.ExportJSON()
	c.Header("Content-Type", "application/json")
	c.Header("Content-Disposition", "attachment; filename=llm_configs.json")
	c.String(http.StatusOK, string(data))
}

// handleTestConnection 测试连接
func handleTestConnection(c *gin.Context) {
	var req struct {
		Config storage.Config `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := req.Config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	result := provider.TestConnection(c.Request.Context())
	addLog("🔗 连接测试 [%s]: %s", req.Config.Name, result.Message)
	c.JSON(http.StatusOK, gin.H{
		"success": result.Success,
		"message": result.Message,
		"models":  result.Models,
	})
}

// handleChat 发送聊天消息
func handleChat(c *gin.Context) {
	var req struct {
		Config      storage.Config `json:"config"`
		Message     string         `json:"message"`
		MaxTokens   int            `json:"max_tokens"`
		Temperature float64        `json:"temperature"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := req.Config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
		return
	}

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	chatReq := &llm.ChatRequest{
		Model:     req.Config.Model,
		Message:   req.Message,
		MaxTokens: req.MaxTokens,
		Temperature: req.Temperature,
	}
	result := provider.Chat(c.Request.Context(), chatReq)
	addLog("💬 聊天测试 [%s]: %s", req.Config.Name, result.Error)
	c.JSON(http.StatusOK, gin.H{
		"success":         result.Success,
		"content":         result.Content,
		"finish_reason":   result.FinishReason,
		"latency_ms":      result.LatencyMs,
		"prompt_tokens":   result.PromptTokens,
		"completion_tokens": result.CompletionToks,
		"total_tokens":    result.TotalTokens,
		"error":           result.Error,
	})
}

// handleBatchTest 批量测试
func handleBatchTest(c *gin.Context) {
	var req struct {
		Configs map[string]storage.Config `json:"configs"`
		Message string                    `json:"message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if len(req.Configs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "至少需要一个配置"})
		return
	}
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
		return
	}

	// 使用 SSE 流式推送结果
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// 按名称排序确保稳定输出
	names := make([]string, 0, len(req.Configs))
	for name := range req.Configs {
		names = append(names, name)
	}
	sort.Strings(names)

	// 使用 RunBenchmark 并发执行，传递请求上下文以支持客户端断开取消
	results := concurrent.RunBenchmark(c.Request.Context(), len(names), len(names), 30*time.Second, func(reqID int) concurrent.TestResult {
		if reqID >= len(names) {
			return concurrent.TestResult{Status: 500, Error: "index out of range"}
		}
		name := names[reqID]
		cfg := req.Configs[name]
		provider := llm.NewProvider(toLLMConfig(&cfg))
		chatReq := &llm.ChatRequest{
			Model:     cfg.Model,
			Message:   req.Message,
			MaxTokens: cfg.MaxTokens,
			Temperature: cfg.Temperature,
		}
		start := time.Now()
		result := provider.Chat(c.Request.Context(), chatReq)
		latency := time.Since(start).Seconds() * 1000

		tr := concurrent.TestResult{
			ReqID:       reqID,
			Model:       name,
			LatencyMs:   latency,
		}
		if result.Success {
			tr.Status = 200
			tr.ContentLength = len(result.Content)
		} else {
			tr.Status = 500
			tr.Error = result.Error
		}
		return tr
	})

	// 发送结果
	successCount := 0
	failCount := 0
	for _, result := range results {
		data, _ := json.Marshal(result)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		c.Writer.Flush()
		if result.Status == 200 {
			successCount++
		} else {
			failCount++
		}
	}
	done := map[string]interface{}{
		"type":    "done",
		"total":   len(names),
		"success": successCount,
		"fail":    failCount,
	}
	data, _ := json.Marshal(done)
	fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	c.Writer.Flush()

	addLog("📊 批量测试完成: 成功%d, 失败%d", successCount, failCount)
}

// handleBenchmark 基准测试
func handleBenchmark(c *gin.Context) {
	var req struct {
		Config      storage.Config `json:"config"`
		Concurrency int            `json:"concurrency"`
		MaxTokens   int            `json:"max_tokens"`
		Prompt      string         `json:"prompt"`
		Rounds      int            `json:"rounds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := req.Config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Prompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "提示词不能为空"})
		return
	}
	if req.Rounds <= 0 {
		req.Rounds = 10
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 5
	}

	// 使用 SSE 流式推送
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	totalResults := req.Rounds
	results := make([]concurrent.TestResult, 0, totalResults)

	// 逐个执行并实时推送，检测客户端断开
	ctx := c.Request.Context()
	for i := 0; i < req.Rounds; i++ {
		// 检查客户端是否已断开
		if err := ctx.Err(); err != nil {
			addLog("📈 基准测试客户端断开，提前终止（已完成 %d/%d 轮）", i, req.Rounds)
			break
		}
		chatReq := &llm.ChatRequest{
			Model:     req.Config.Model,
			Message:   req.Prompt,
			MaxTokens: req.MaxTokens,
			Temperature: req.Config.Temperature,
		}
		start := time.Now()
		chatResp := provider.Chat(c.Request.Context(), chatReq)
		latency := time.Since(start).Seconds() * 1000

		testResult := concurrent.TestResult{
			ReqID:         i,
			Model:         req.Config.Name,
			Status:        200,
			LatencyMs:     latency,
			ContentLength: len(chatResp.Content),
		}
		if chatResp.Success {
			testResult.ContentPreview = chatResp.Content[:min(100, len(chatResp.Content))]
			testResult.PromptTokens = chatResp.PromptTokens
			testResult.CompletionToks = chatResp.CompletionToks
			testResult.TotalTokens = chatResp.TotalTokens
		} else {
			testResult.Error = chatResp.Error
			testResult.Status = 500
		}

		results = append(results, testResult)
		data, _ := json.Marshal(testResult)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		c.Writer.Flush()
	}

	// 计算统计信息
	report := calculateStats(results)
	report["type"] = "report"
	reportData, _ := json.Marshal(report)
	fmt.Fprintf(c.Writer, "data: %s\n\n", reportData)
	c.Writer.Flush()

	addLog("📊 基准测试完成: 总请求%d, 成功%d, 平均延迟%.1fms", req.Rounds, report["success"], report["lat_avg"])
}

// calculateStats 计算基准测试统计数据
func calculateStats(results []concurrent.TestResult) map[string]interface{} {
	total := len(results)
	success := 0
	var latencies []float64
	var minLat, maxLat, sumLat float64

	for _, r := range results {
		if r.Status == 200 {
			success++
			latencies = append(latencies, r.LatencyMs)
			sumLat += r.LatencyMs
			if minLat == 0 || r.LatencyMs < minLat {
				minLat = r.LatencyMs
			}
			if r.LatencyMs > maxLat {
				maxLat = r.LatencyMs
			}
		}
	}

	report := map[string]interface{}{
		"total":   total,
		"success": success,
		"failed":  total - success,
	}

	if len(latencies) > 0 {
		sort.Float64s(latencies)
		avg := sumLat / float64(len(latencies))
		report["lat_avg"] = avg
		report["lat_min"] = minLat
		report["lat_max"] = maxLat
		report["lat_p50"] = latencies[min(int(float64(len(latencies))*0.5), len(latencies)-1)]
		report["lat_p95"] = latencies[min(int(float64(len(latencies))*0.95), len(latencies)-1)]

		// 计算标准差
		var sumSqDiff float64
		for _, l := range latencies {
			diff := l - avg
			sumSqDiff += diff * diff
		}
		report["stdev"] = math.Sqrt(sumSqDiff / float64(len(latencies)))
	}

	return report
}

// handleBurnTest Burn 压力测试
func handleBurnTest(c *gin.Context) {
	var req struct {
		Config    storage.Config `json:"config"`
		Rounds    int            `json:"rounds"`
		MaxTokens int            `json:"max_tokens"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := req.Config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Rounds <= 0 {
		req.Rounds = 10
	}

	// 使用 SSE 流式推送
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	totalTokens := 0
	successCount := 0
	ctx := c.Request.Context()

	for i := 1; i <= req.Rounds; i++ {
		// 检查客户端是否已断开
		if err := ctx.Err(); err != nil {
			addLog("🔥 Burn 压测客户端断开，提前终止（已完成 %d/%d 轮）", i-1, req.Rounds)
			break
		}
		chatReq := &llm.ChatRequest{
			Model:     req.Config.Model,
			Message:   "请简短回复：第" + strconv.Itoa(i) + "轮测试",
			MaxTokens: req.MaxTokens,
			Temperature: 0.7,
		}
		start := time.Now()
		chatResp := provider.Chat(c.Request.Context(), chatReq)
		latency := time.Since(start).Seconds() * 1000

		result := concurrent.TestResult{
			ReqID:          i,
			Model:          req.Config.Name,
			Status:         200,
			LatencyMs:      latency,
			ContentLength:  len(chatResp.Content),
			CompletionToks: chatResp.CompletionToks,
			TotalTokens:    chatResp.TotalTokens,
		}
		if chatResp.Success {
			successCount++
			totalTokens += chatResp.TotalTokens
		} else {
			result.Status = 500
			result.Error = chatResp.Error
		}

		data, _ := json.Marshal(result)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		c.Writer.Flush()
	}

	done := map[string]interface{}{
		"type":         "done",
		"total_rounds": req.Rounds,
		"success":      successCount,
		"total_tokens": totalTokens,
	}
	data, _ := json.Marshal(done)
	fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	c.Writer.Flush()

	addLog("🔥 Burn 测试完成: 总轮次%d, 成功%d, 总tokens%d", req.Rounds, successCount, totalTokens)
}

// handleListModels 获取模型列表
func handleListModels(c *gin.Context) {
	var req struct {
		Config storage.Config `json:"config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据"})
		return
	}
	if err := req.Config.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	models, err := provider.ListModels(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// handleLogs 获取日志
func handleLogs(c *gin.Context) {
	logMu.Lock()
	defer logMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"logs": logLines})
}

// handleClearLogs 清空日志
func handleClearLogs(c *gin.Context) {
	logMu.Lock()
	defer logMu.Unlock()
	logLines = logLines[:0]
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// toLLMConfig 将 storage.Config 转换为 llm.Config
//
// 为什么需要两个 Config 结构体:
// storage 包和 llm 包是独立的（避免循环依赖），
// 各自定义了自己的 Config 结构体。main.go 作为桥梁负责转换。
//
// storage.Config: 面向持久化（JSON tag、Validate 方法）
// llm.Config: 面向 HTTP 调用（GetTimeout、GetHTTPClient 方法）
func toLLMConfig(cfg *storage.Config) *llm.Config {
	if cfg == nil {
		return nil
	}
	return &llm.Config{
		APIType:      cfg.APIType,
		BaseURL:      cfg.BaseURL,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		CustomPath:   cfg.CustomPath,
		EndpointMode: cfg.EndpointMode,
		HTTPReferer:  cfg.HTTPReferer,
		XTitle:       cfg.XTitle,
		Temperature:  cfg.Temperature,
		MaxTokens:    cfg.MaxTokens,
		Timeout:      cfg.Timeout,
		ProxyURL:     cfg.ProxyURL,
	}
}

// 辅助函数
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
