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
	"bufio"
	"bytes"
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

	"github.com/qwdingyu/llm-tester/concurrent"
	"github.com/qwdingyu/llm-tester/llm"
	"github.com/qwdingyu/llm-tester/probe"
	"github.com/qwdingyu/llm-tester/storage"
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
	r.POST("/api/test/chat/stream", handleChatStream)
	r.POST("/api/test/batch", handleBatchTest)
	r.POST("/api/test/benchmark", handleBenchmark)
	r.POST("/api/test/burn", handleBurnTest)
	r.POST("/api/test/models", handleListModels)
	r.POST("/api/test/suite", handleTestSuite)

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

// handleChatStream 流式聊天测试（SSE 打字机效果）
// 发送 stream=true 到 LLM API，逐 token 解析 SSE 并转发到前端
func handleChatStream(c *gin.Context) {
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
	if req.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息不能为空"})
		return
	}

	// 设置 SSE 头
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.WriteHeader(http.StatusOK)

	provider := llm.NewProvider(toLLMConfig(&req.Config))
	start := time.Now()

	// 构造流式请求体
	bodyMap := llm.BuildChatBody(req.Config.Model, &llm.ChatRequest{
		Message: req.Message, Temperature: req.Temperature, MaxTokens: req.MaxTokens,
	})
	bodyMap["stream"] = true // 覆盖为流式
	bodyBytes, _ := json.Marshal(bodyMap)

	openAIProvider, ok := provider.(*llm.OpenAIProvider)
	if !ok {
		// 非 OpenAI 兼容 API 回退到非流式
		fallback := provider.Chat(c.Request.Context(), &llm.ChatRequest{
			Message: req.Message, MaxTokens: req.MaxTokens, Temperature: req.Temperature})
		jsonData, _ := json.Marshal(map[string]interface{}{
			"type": "done", "content": fallback.Content,
			"finish_reason": fallback.FinishReason, "latency_ms": fallback.LatencyMs,
			"prompt_tokens": fallback.PromptTokens, "completion_tokens": fallback.CompletionToks,
			"total_tokens": fallback.TotalTokens, "error": fallback.Error,
		})
		fmt.Fprintf(c.Writer, "data: %s\n\n", jsonData)
		c.Writer.Flush()
		return
	}

	_ = openAIProvider // 我们直接构造请求，不调用 OpenAIProvider.Chat
	_ = time.Since(start)

	// 直接发送流式请求到 LLM API
	baseURL := strings.TrimRight(req.Config.BaseURL, "/")
	fullURL := baseURL + "/v1/chat/completions"

	httpReq, _ := http.NewRequestWithContext(c.Request.Context(), "POST", fullURL, bytes.NewReader(bodyBytes))
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	llm.SetCommonHeaders(toLLMConfig(&req.Config), httpReq)

	client := toLLMConfig(&req.Config).GetHTTPClient()
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"content\":\"%s\"}\n\n", err.Error())
		c.Writer.Flush()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(c.Writer, "data: {\"type\":\"error\",\"content\":\"HTTP %d: %s\"}\n\n", resp.StatusCode, string(body))
		c.Writer.Flush()
		return
	}

	// 逐行解析 SSE 流
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	fullContent := ""
	finishReason := ""
	promptTokens := 0
	completionTokens := 0

	for scanner.Scan() {
		line := scanner.Text()

		// SSE 格式: data: {...}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")

		// SSE 结束标记
		if payload == "[DONE]" {
			break
		}

		// 解析 JSON
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		if len(chunk.Choices) > 0 {
			token := chunk.Choices[0].Delta.Content
			fullContent += token

			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}

			// 向前端推送每个 token
			if token != "" {
				jsonData, _ := json.Marshal(map[string]interface{}{
					"type":    "token",
					"content": token,
					"full":    fullContent,
				})
				fmt.Fprintf(c.Writer, "data: %s\n\n", jsonData)
				c.Writer.Flush()
			}
		}

		// usage 信息通常在最后一个 chunk 中
		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
			completionTokens = chunk.Usage.CompletionTokens
		}
	}

	latency := time.Since(start).Seconds() * 1000

	// 推送完成事件
	doneData, _ := json.Marshal(map[string]interface{}{
		"type":             "done",
		"content":          fullContent,
		"finish_reason":    finishReason,
		"latency_ms":       latency,
		"prompt_tokens":    promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":     promptTokens + completionTokens,
	})
	fmt.Fprintf(c.Writer, "data: %s\n\n", doneData)
	c.Writer.Flush()

	addLog("💬 流式 [%s] %d tokens, %.0fms", req.Config.Name, promptTokens+completionTokens, latency)
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
	ctx := c.Request.Context()

	// 预热：发送 1 次请求消除冷启动影响，结果不计入统计
	provider.Chat(ctx, &llm.ChatRequest{
		Model: req.Config.Model, Message: req.Prompt, MaxTokens: req.MaxTokens,
	})

	// 逐个执行并实时推送，检测客户端断开
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
			result.ContentPreview = chatResp.Content[:min(100, len(chatResp.Content))]
			result.FinishReason = chatResp.FinishReason
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

// handleTestSuite 执行测试套件（多配置 × 多探针）
func handleTestSuite(c *gin.Context) {
	var req struct {
		Targets []struct {
			Name   string          `json:"name"`
			Config storage.Config  `json:"config"`
			ProbeCfg probe.Config  `json:"probe_cfg"`
		} `json:"targets"`
		ProbeNames []string `json:"probes"` // 探针名称列表
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求数据: " + err.Error()})
		return
	}
	if len(req.Targets) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "至少需要一个测试目标"})
		return
	}

	// 构建 Suite
	suite := &probe.Suite{}
	for _, t := range req.Targets {
		provider := llm.NewProvider(toLLMConfig(&t.Config))
		if provider == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("配置 %q 创建 Provider 失败", t.Name)})
			return
		}
		suite.Targets = append(suite.Targets, probe.Target{
			Name:     t.Name,
			Provider: provider,
			Config:   t.ProbeCfg,
		})
	}

	// 按名称选择探针
	if len(req.ProbeNames) == 0 {
		// 默认运行所有已注册探针
		for _, p := range probe.Registry {
			suite.Probes = append(suite.Probes, p)
		}
	} else {
		for _, name := range req.ProbeNames {
			p, ok := probe.Registry[name]
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("未知探针: %s", name)})
				return
			}
			suite.Probes = append(suite.Probes, p)
		}
	}

	// 执行套件
	result := suite.Run(c.Request.Context())

	// 记录日志
	for _, item := range result.Items {
		if item.Error != "" {
			addLog("📊 [%s][%s] ❌ %s", item.ConfigName, item.ProbeName, item.Error)
		} else {
			addLog("📊 [%s][%s] %.0f分 %s", item.ConfigName, item.ProbeName, item.Score, item.Summary)
		}
	}

	c.JSON(http.StatusOK, result)
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
