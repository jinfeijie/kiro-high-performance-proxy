package main

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"

	kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

// computeHash 计算数据的 MD5 hash（前8位）
func computeHash(data []byte) string {
	h := md5.Sum(data)
	return hex.EncodeToString(h[:])[:8]
}

// generateID 生成唯一 ID（时间戳 + 随机数，避免并发冲突）
func generateID(prefix string) string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b))
}

// OpenAI 格式请求
type OpenAIChatRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Stream   bool             `json:"stream"`
}

// Claude 格式请求
type ClaudeChatRequest struct {
	Model     string           `json:"model"`
	Messages  []map[string]any `json:"messages"`
	MaxTokens int              `json:"max_tokens"`
	Stream    bool             `json:"stream"`
}

// OpenAI 格式响应（完整版，对齐 new-api）
type OpenAIChatResponse struct {
	ID                string                  `json:"id"`
	Object            string                  `json:"object"`
	Created           int64                   `json:"created"`
	Model             string                  `json:"model"`
	SystemFingerprint *string                 `json:"system_fingerprint"`
	Choices           []OpenAIChatChoice      `json:"choices"`
	Usage             *kiroclient.OpenAIUsage `json:"usage,omitempty"`
}

// OpenAIChatChoice OpenAI 响应的 choice
type OpenAIChatChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

// OpenAIChatMessage OpenAI 响应的 message
type OpenAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Claude 格式响应（完整版，对齐 new-api）
type ClaudeChatResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []ClaudeContentBlock    `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason,omitempty"`
	Usage      *kiroclient.ClaudeUsage `json:"usage,omitempty"`
}

// ClaudeContentBlock Claude 响应的内容块
type ClaudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Token 配置请求
type TokenConfigRequest struct {
	AccessToken string `json:"accessToken"`
	TokenPath   string `json:"tokenPath"`
}

// Token 状态响应
type TokenStatusResponse struct {
	Valid     bool   `json:"valid"`
	Region    string `json:"region"`
	Provider  string `json:"provider"`
	ExpiresAt string `json:"expiresAt"`
	IsExpired bool   `json:"isExpired"`
	Error     string `json:"error,omitempty"`
	// 额度信息
	UsedCredits      float64 `json:"usedCredits"`
	TotalCredits     float64 `json:"totalCredits"`
	DaysUntilReset   int     `json:"daysUntilReset"`
	NextResetDate    string  `json:"nextResetDate"`
	SubscriptionName string  `json:"subscriptionName"`
	// 用户信息
	UserId    string `json:"userId"`
	TokenData string `json:"tokenData"` // 完整的token JSON数据
}

// 搜索请求
type SearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"maxResults"`
}

var client *kiroclient.KiroClient
var modelMapping kiroclient.ModelMapping
var modelMappingFile = "model-mapping.json"
var apiKeysFile = "api-keys.json"
var apiKeys []string // API-KEY 列表（支持 Claude X-API-Key 和 OpenAI Bearer Token）

// ========== 全局 Token 统计 ==========
var tokenStatsFile = "token-stats.json"
var tokenStats TokenStats
var tokenStatsMutex sync.RWMutex
var tokenStatsChan = make(chan TokenDelta, 1000) // 异步写入通道

// TokenStats 全局统计数据
type TokenStats struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
	RequestCount int64 `json:"requestCount"`
	UpdatedAt    int64 `json:"updatedAt"`
}

// TokenDelta 单次请求的 Token 增量
type TokenDelta struct {
	Input  int
	Output int
}

// loadTokenStats 启动时加载统计数据
func loadTokenStats() {
	data, err := os.ReadFile(tokenStatsFile)
	if err != nil {
		tokenStats = TokenStats{}
		fmt.Println("📊 Token 统计: 新建")
		return
	}
	if err := json.Unmarshal(data, &tokenStats); err != nil {
		tokenStats = TokenStats{}
	}
	fmt.Printf("📊 Token 统计: 已加载 (输入:%d 输出:%d 总计:%d)\n",
		tokenStats.InputTokens, tokenStats.OutputTokens, tokenStats.TotalTokens)
}

// saveTokenStats 保存统计数据到文件
func saveTokenStats() {
	tokenStatsMutex.RLock()
	data, _ := json.MarshalIndent(tokenStats, "", "  ")
	tokenStatsMutex.RUnlock()
	os.WriteFile(tokenStatsFile, data, 0644)
}

// addTokenStats 累加 Token 统计（异步）
func addTokenStats(input, output int) {
	select {
	case tokenStatsChan <- TokenDelta{Input: input, Output: output}:
	default:
		// 通道满了直接丢弃，避免阻塞
	}
}

// tokenStatsWorker 后台协程处理统计写入
func tokenStatsWorker() {
	ticker := time.NewTicker(10 * time.Second) // 每10秒落盘一次
	dirty := false
	for {
		select {
		case delta := <-tokenStatsChan:
			tokenStatsMutex.Lock()
			tokenStats.InputTokens += int64(delta.Input)
			tokenStats.OutputTokens += int64(delta.Output)
			tokenStats.TotalTokens += int64(delta.Input + delta.Output)
			tokenStats.RequestCount++
			tokenStats.UpdatedAt = time.Now().Unix()
			tokenStatsMutex.Unlock()
			dirty = true
		case <-ticker.C:
			if dirty {
				saveTokenStats()
				dirty = false
			}
		}
	}
}

// getTokenStats 获取当前统计数据
func getTokenStats() TokenStats {
	tokenStatsMutex.RLock()
	defer tokenStatsMutex.RUnlock()
	return tokenStats
}

// handleGetStats 获取全局 Token 统计
func handleGetStats(c *gin.Context) {
	stats := getTokenStats()
	c.JSON(200, gin.H{
		"inputTokens":  stats.InputTokens,
		"outputTokens": stats.OutputTokens,
		"totalTokens":  stats.TotalTokens,
		"requestCount": stats.RequestCount,
		"updatedAt":    stats.UpdatedAt,
	})
}

// loadApiKeys 从文件加载 API-KEY 配置
func loadApiKeys() {
	data, err := os.ReadFile(apiKeysFile)
	if err != nil {
		apiKeys = []string{}
		return
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		apiKeys = []string{}
		return
	}
	apiKeys = keys
	fmt.Printf("✅ 已加载 %d 个 API-KEY\n", len(apiKeys))
}

// saveApiKeys 保存 API-KEY 配置到文件
func saveApiKeys() error {
	data, err := json.MarshalIndent(apiKeys, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(apiKeysFile, data, 0644)
}

// apiKeyAuthMiddleware API-KEY 验证中间件
// 支持两种格式：
// 1. Claude 格式: X-API-Key: sk-xxx
// 2. OpenAI 格式: Authorization: Bearer sk-xxx
func apiKeyAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 如果没有配置 API-KEY，跳过验证
		if len(apiKeys) == 0 {
			c.Next()
			return
		}

		// 尝试从 X-API-Key 获取（Claude 格式）
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			apiKey = c.GetHeader("x-api-key")
		}

		// 尝试从 Authorization 获取（OpenAI 格式）
		if apiKey == "" {
			auth := c.GetHeader("Authorization")
			if len(auth) > 7 && auth[:7] == "Bearer " {
				apiKey = auth[7:]
			}
		}

		// 验证 API-KEY
		if apiKey == "" {
			c.JSON(401, gin.H{
				"error": map[string]any{
					"message": "Missing API key",
					"type":    "authentication_error",
				},
			})
			c.Abort()
			return
		}

		// 检查 API-KEY 是否有效
		valid := false
		for _, k := range apiKeys {
			if k == apiKey {
				valid = true
				break
			}
		}

		if !valid {
			c.JSON(401, gin.H{
				"error": map[string]any{
					"message": "Invalid API key",
					"type":    "authentication_error",
				},
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// handleGetApiKeys 获取 API-KEY 列表
func handleGetApiKeys(c *gin.Context) {
	// 返回脱敏的 API-KEY 列表
	masked := make([]map[string]string, len(apiKeys))
	for i, k := range apiKeys {
		if len(k) > 8 {
			masked[i] = map[string]string{
				"key":    k[:4] + "..." + k[len(k)-4:],
				"full":   k,
				"prefix": k[:8],
			}
		} else {
			masked[i] = map[string]string{
				"key":    k,
				"full":   k,
				"prefix": k,
			}
		}
	}
	// 计算 hash 用于乐观锁
	data, _ := json.Marshal(apiKeys)
	hash := computeHash(data)
	c.JSON(200, gin.H{"keys": masked, "count": len(apiKeys), "hash": hash})
}

// handleUpdateApiKeys 更新 API-KEY 列表
func handleUpdateApiKeys(c *gin.Context) {
	var req struct {
		Keys []string `json:"keys"`
		Hash string   `json:"hash"` // 乐观锁 hash
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 校验 hash（乐观锁）
	if req.Hash != "" {
		currentData, _ := json.Marshal(apiKeys)
		currentHash := computeHash(currentData)
		if req.Hash != currentHash {
			c.JSON(409, gin.H{"error": "配置已被修改，请刷新后重试"})
			return
		}
	}

	// 过滤空值
	var validKeys []string
	for _, k := range req.Keys {
		if k != "" {
			validKeys = append(validKeys, k)
		}
	}

	apiKeys = validKeys
	if err := saveApiKeys(); err != nil {
		c.JSON(500, gin.H{"error": "保存失败: " + err.Error()})
		return
	}

	// 返回新的 hash
	newData, _ := json.Marshal(apiKeys)
	newHash := computeHash(newData)
	c.JSON(200, gin.H{"message": "API-KEY 配置已更新", "count": len(apiKeys), "hash": newHash})
}

// 登录会话缓存（内存中保存，用于轮询）
var loginSessions = make(map[string]*kiroclient.LoginSession)
var sessionMutex sync.RWMutex

func main() {
	// 初始化 Kiro 客户端
	client = kiroclient.NewKiroClient()

	// 初始化账号缓存（从文件加载到内存）
	if err := client.Auth.InitAccountsCache(); err != nil {
		fmt.Printf("⚠️ 初始化账号缓存失败: %v\n", err)
	} else {
		fmt.Println("✅ 账号缓存初始化完成")
	}

	// 加载模型映射配置
	loadModelMapping()

	// 加载 API-KEY 配置
	loadApiKeys()

	// 加载 Token 统计数据并启动后台写入协程
	loadTokenStats()
	go tokenStatsWorker()

	// 启动保活机制（后台自动刷新所有账号的 Token）
	client.Auth.StartKeepAlive()
	fmt.Println("🔄 保活机制已启动（每5分钟检查一次）")

	r := gin.Default()

	// 注册 pprof 路由
	pprof.Register(r)

	// CORS
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// 静态文件服务 - 从项目根目录运行时的路径
	r.Static("/static", "./server/static")
	r.GET("/", func(c *gin.Context) {
		c.File("./server/static/index.html")
	})

	// API 路由组
	api := r.Group("/api")
	{
		// Token 管理
		api.GET("/token/status", handleTokenStatus)
		api.POST("/token/config", handleTokenConfig)

		// 模型列表
		api.GET("/models", handleModelsList)

		// 模型映射管理
		api.GET("/model-mapping", handleGetModelMapping)
		api.POST("/model-mapping", handleUpdateModelMapping)

		// 账号管理（登录流程）
		api.POST("/auth/start", handleStartLogin)
		api.GET("/auth/poll/:sessionId", handlePollLogin)
		api.POST("/auth/import", handleImportAccount)
		api.GET("/accounts", handleListAccounts)
		api.POST("/accounts/refresh-all", handleRefreshAllAccounts)
		api.DELETE("/accounts/:id", handleDeleteAccount)
		api.POST("/accounts/:id/refresh", handleRefreshAccount)
		api.GET("/accounts/:id/detail", handleAccountDetail)

		// API-KEY 管理
		api.GET("/settings/api-keys", handleGetApiKeys)
		api.POST("/settings/api-keys", handleUpdateApiKeys)

		// Token 统计
		api.GET("/stats", handleGetStats)

		// Chat 接口
		api.POST("/chat", handleChat)

		// 搜索接口
		api.POST("/search", handleSearch)

		// MCP 工具
		api.GET("/tools", handleToolsList)
		api.POST("/tools/call", handleToolsCall)
	}

	// OpenAI 格式接口（兼容）- 需要 API-KEY 验证
	r.POST("/v1/chat/completions", apiKeyAuthMiddleware(), handleOpenAIChat)

	// Claude 格式接口（兼容）- 需要 API-KEY 验证
	r.POST("/v1/messages", apiKeyAuthMiddleware(), handleClaudeChat)

	// Anthropic 原生格式接口（兼容）- 需要 API-KEY 验证
	r.POST("/anthropic/v1/messages", apiKeyAuthMiddleware(), handleClaudeChat)

	// 从环境变量读取端口，默认 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Println("🚀 Kiro API Proxy 启动成功！")
	fmt.Printf("📡 监听端口: %s\n", port)
	fmt.Printf("🌐 Web 界面: http://localhost:%s\n", port)
	fmt.Println("🔗 OpenAI 格式: POST /v1/chat/completions")
	fmt.Println("🔗 Claude 格式: POST /v1/messages")
	fmt.Println("🔗 Anthropic 格式: POST /anthropic/v1/messages")
	fmt.Printf("🔧 pprof: http://localhost:%s/debug/pprof/\n", port)

	r.Run(":" + port)
}

// handleTokenStatus 获取 Token 状态
func handleTokenStatus(c *gin.Context) {
	token, err := client.Auth.ReadToken()
	if err != nil {
		c.JSON(200, TokenStatusResponse{
			Valid: false,
			Error: err.Error(),
		})
		return
	}

	resp := TokenStatusResponse{
		Valid:     true,
		Region:    token.Region,
		Provider:  token.Provider,
		ExpiresAt: token.ExpiresAt,
		IsExpired: token.IsExpired(),
	}

	// 生成完整的 token JSON 数据
	tokenBytes, _ := json.MarshalIndent(token, "", "  ")
	resp.TokenData = string(tokenBytes)

	// 获取额度信息
	usage, err := client.Auth.GetUsageLimits()
	if err != nil {
		fmt.Printf("获取额度信息失败: %v\n", err)
	} else if len(usage.UsageBreakdownList) > 0 {
		// 查找 CREDIT 类型的额度
		for _, item := range usage.UsageBreakdownList {
			if item.ResourceType == "CREDIT" {
				resp.UsedCredits = item.CurrentUsageWithPrecision
				resp.TotalCredits = item.UsageLimitWithPrecision
				break
			}
		}
		// 用 nextDateReset 计算剩余天数（API 的 daysUntilReset 返回 0 是已知 bug）
		if usage.NextDateReset > 0 {
			resetTime := time.Unix(int64(usage.NextDateReset), 0)
			days := int(time.Until(resetTime).Hours() / 24)
			if days < 0 {
				days = 0
			}
			resp.DaysUntilReset = days
			resp.NextResetDate = resetTime.Format("2006-01-02")
		}
		// 去掉 "KIRO " 前缀，只保留 "POWER" 等订阅类型名称
		subName := usage.SubscriptionInfo.SubscriptionTitle
		if len(subName) > 5 && subName[:5] == "KIRO " {
			subName = subName[5:]
		}
		resp.SubscriptionName = subName
		resp.UserId = usage.UserInfo.UserId
	}

	c.JSON(200, resp)
}

// handleTokenConfig 配置 Token
func handleTokenConfig(c *gin.Context) {
	var req TokenConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 如果提供了 AccessToken，设置环境变量
	if req.AccessToken != "" {
		os.Setenv("KIRO_ACCESS_TOKEN", req.AccessToken)
	}

	// 如果提供了 TokenPath，设置环境变量
	if req.TokenPath != "" {
		os.Setenv("KIRO_AUTH_TOKEN_PATH", req.TokenPath)
	}

	// 重新初始化客户端
	client = kiroclient.NewKiroClient()

	c.JSON(200, gin.H{"message": "Token 配置成功"})
}

// handleChat 处理聊天请求
func handleChat(c *gin.Context) {
	var req struct {
		Messages []kiroclient.ChatMessage `json:"messages"`
		Stream   bool                     `json:"stream"`
		Model    string                   `json:"model"` // 模型参数
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 应用模型映射（标准化模型ID）
	if req.Model != "" {
		req.Model = kiroclient.NormalizeModelID(req.Model, modelMapping)
	}

	// 验证模型参数（如果提供）
	if req.Model != "" && !kiroclient.IsValidModel(req.Model) {
		c.JSON(400, gin.H{
			"error": fmt.Sprintf("无效的模型 ID: %s", req.Model),
		})
		return
	}

	if req.Stream {
		// 流式响应
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(500, gin.H{"error": "Streaming not supported"})
			return
		}

		err := client.Chat.ChatStreamWithModel(req.Messages, req.Model, func(content string, done bool) {
			if done {
				c.Writer.WriteString("data: [DONE]\n\n")
				flusher.Flush()
				return
			}

			data := map[string]string{"content": content}
			jsonData, _ := json.Marshal(data)
			c.Writer.WriteString(fmt.Sprintf("data: %s\n\n", string(jsonData)))
			flusher.Flush()
		})

		if err != nil {
			c.Writer.WriteString(fmt.Sprintf("data: {\"error\": \"%s\"}\n\n", err.Error()))
			flusher.Flush()
		}
	} else {
		// 非流式响应
		response, err := client.Chat.ChatWithModel(req.Messages, req.Model)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{"content": response})
	}
}

// handleSearch 处理搜索请求
func handleSearch(c *gin.Context) {
	var req SearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if req.MaxResults == 0 {
		req.MaxResults = 10
	}

	results, err := client.Search.Search(req.Query, req.MaxResults)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"results": results})
}

// handleToolsList 获取工具列表
func handleToolsList(c *gin.Context) {
	tools, err := client.MCP.ToolsList()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"tools": tools})
}

// handleToolsCall 调用工具
func handleToolsCall(c *gin.Context) {
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	content, err := client.MCP.ToolsCall(req.Name, req.Arguments)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"content": content})
}

// handleOpenAIChat 处理 OpenAI 格式请求
func handleOpenAIChat(c *gin.Context) {
	var req OpenAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 应用模型映射（标准化模型ID）
	if req.Model != "" {
		req.Model = kiroclient.NormalizeModelID(req.Model, modelMapping)
	}

	// 验证模型参数
	if req.Model != "" && !kiroclient.IsValidModel(req.Model) {
		c.JSON(400, gin.H{
			"error": fmt.Sprintf("无效的模型 ID: %s", req.Model),
		})
		return
	}

	// 转换消息格式
	messages := convertToKiroMessages(req.Messages)

	if req.Stream {
		handleStreamResponse(c, messages, "openai", req.Model)
	} else {
		handleNonStreamResponse(c, messages, "openai", req.Model)
	}
}

// handleClaudeChat 处理 Claude 格式请求
func handleClaudeChat(c *gin.Context) {
	var req ClaudeChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 应用模型映射（标准化模型ID）
	if req.Model != "" {
		req.Model = kiroclient.NormalizeModelID(req.Model, modelMapping)
	}

	// 验证模型参数
	if req.Model != "" && !kiroclient.IsValidModel(req.Model) {
		c.JSON(400, gin.H{
			"error": fmt.Sprintf("无效的模型 ID: %s", req.Model),
		})
		return
	}

	// 转换消息格式
	messages := convertToKiroMessages(req.Messages)

	if req.Stream {
		handleStreamResponse(c, messages, "claude", req.Model)
	} else {
		handleNonStreamResponse(c, messages, "claude", req.Model)
	}
}

// convertToKiroMessages 转换消息格式（支持多模态）
func convertToKiroMessages(messages []map[string]any) []kiroclient.ChatMessage {
	var kiroMessages []kiroclient.ChatMessage

	for _, msg := range messages {
		role, _ := msg["role"].(string)

		var content string
		var images []kiroclient.ImageBlock

		switch v := msg["content"].(type) {
		case string:
			// 简单字符串格式
			content = v
		case []interface{}:
			// 数组格式（OpenAI/Claude 多模态）
			for _, item := range v {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}

				itemType, _ := m["type"].(string)

				switch itemType {
				case "text":
					// 文本内容
					if text, ok := m["text"].(string); ok {
						content += text
					}

				case "image_url":
					// OpenAI 格式图片
					// {"type": "image_url", "image_url": {"url": "data:image/png;base64,..."}}
					if imgObj, ok := m["image_url"].(map[string]interface{}); ok {
						if url, ok := imgObj["url"].(string); ok {
							format, data, ok := kiroclient.ParseDataURL(url)
							if ok {
								// jpg 统一为 jpeg
								if format == "jpg" {
									format = "jpeg"
								}
								images = append(images, kiroclient.ImageBlock{
									Format: format,
									Source: kiroclient.ImageSource{Bytes: data},
								})
							}
						}
					}

				case "image":
					// Claude 格式图片
					// {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "..."}}
					if source, ok := m["source"].(map[string]interface{}); ok {
						sourceType, _ := source["type"].(string)
						if sourceType != "base64" {
							continue
						}

						mediaType, _ := source["media_type"].(string)
						data, _ := source["data"].(string)

						if mediaType == "" || data == "" {
							continue
						}

						// 从 media_type 提取格式（image/png -> png）
						format := ""
						if len(mediaType) > 6 && mediaType[:6] == "image/" {
							format = mediaType[6:]
						}
						if format == "" {
							continue
						}

						// jpg 统一为 jpeg
						if format == "jpg" {
							format = "jpeg"
						}

						images = append(images, kiroclient.ImageBlock{
							Format: format,
							Source: kiroclient.ImageSource{Bytes: data},
						})
					}
				}
			}
		}

		kiroMessages = append(kiroMessages, kiroclient.ChatMessage{
			Role:    role,
			Content: content,
			Images:  images,
		})
	}

	return kiroMessages
}

// handleStreamResponse 处理流式响应
func handleStreamResponse(c *gin.Context, messages []kiroclient.ChatMessage, format string, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(500, gin.H{"error": "Streaming not supported"})
		return
	}

	// 计算输入 token 数
	inputTokens := kiroclient.CountMessagesTokens(messages)
	var outputBuilder strings.Builder
	msgID := generateID("msg")
	chatcmplID := generateID("chatcmpl")

	// Claude 格式：先发送 message_start 事件（包含 input_tokens）
	if format == "claude" {
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    msgID,
				"type":  "message",
				"role":  "assistant",
				"model": model,
				"usage": map[string]int{
					"input_tokens":  inputTokens,
					"output_tokens": 0,
				},
			},
		}
		data, _ := json.Marshal(msgStart)
		fmt.Fprintf(c.Writer, "event: message_start\ndata: %s\n\n", string(data))

		// 发送 content_block_start 事件
		blockStart := map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}
		data, _ = json.Marshal(blockStart)
		fmt.Fprintf(c.Writer, "event: content_block_start\ndata: %s\n\n", string(data))
		flusher.Flush()
	}

	err := client.Chat.ChatStreamWithModel(messages, model, func(content string, done bool) {
		if done {
			// 计算输出 token 数
			outputTokens := kiroclient.CountTokens(outputBuilder.String())

			if format == "openai" {
				// OpenAI 流式结束前发送带 usage 的 chunk
				stopReason := "stop"
				finalChunk := map[string]any{
					"id":                 chatcmplID,
					"object":             "chat.completion.chunk",
					"created":            time.Now().Unix(),
					"model":              model,
					"system_fingerprint": nil,
					"choices": []map[string]any{
						{
							"index":         0,
							"delta":         map[string]any{},
							"logprobs":      nil,
							"finish_reason": stopReason,
						},
					},
					"usage": map[string]any{
						"prompt_tokens":     inputTokens,
						"completion_tokens": outputTokens,
						"total_tokens":      inputTokens + outputTokens,
						"prompt_tokens_details": map[string]int{
							"cached_tokens": 0,
							"text_tokens":   inputTokens,
							"audio_tokens":  0,
							"image_tokens":  0,
						},
						"completion_tokens_details": map[string]int{
							"text_tokens":      outputTokens,
							"audio_tokens":     0,
							"reasoning_tokens": 0,
						},
					},
				}
				data, _ := json.Marshal(finalChunk)
				fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
				fmt.Fprint(c.Writer, "data: [DONE]\n\n")
			} else {
				// Claude 流式结束：发送 content_block_stop
				blockStop := map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				}
				data, _ := json.Marshal(blockStop)
				fmt.Fprintf(c.Writer, "event: content_block_stop\ndata: %s\n\n", string(data))

				// 发送 message_delta 事件（包含 output_tokens）
				msgDelta := map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason":   "end_turn",
						"stop_sequence": nil,
					},
					"usage": map[string]int{
						"output_tokens": outputTokens,
					},
				}
				data, _ = json.Marshal(msgDelta)
				fmt.Fprintf(c.Writer, "event: message_delta\ndata: %s\n\n", string(data))

				// 发送 message_stop 事件
				msgStop := map[string]any{
					"type": "message_stop",
				}
				data, _ = json.Marshal(msgStop)
				fmt.Fprintf(c.Writer, "event: message_stop\ndata: %s\n\n", string(data))
			}
			// 累加全局统计
			addTokenStats(inputTokens, outputTokens)
			flusher.Flush()
			return
		}

		// 累积输出内容用于计算 token
		outputBuilder.WriteString(content)

		if format == "openai" {
			// OpenAI SSE 格式（finish_reason 在非结束 chunk 中为 null）
			chunk := map[string]any{
				"id":                 chatcmplID,
				"object":             "chat.completion.chunk",
				"created":            time.Now().Unix(),
				"model":              model,
				"system_fingerprint": nil,
				"choices": []map[string]any{
					{
						"index": 0,
						"delta": map[string]any{
							"content": content,
						},
						"logprobs":      nil,
						"finish_reason": nil,
					},
				},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
		} else {
			// Claude SSE 格式：content_block_delta
			chunk := map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]string{
					"type": "text_delta",
					"text": content,
				},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(c.Writer, "event: content_block_delta\ndata: %s\n\n", string(data))
		}

		flusher.Flush()
	})

	if err != nil {
		fmt.Fprintf(c.Writer, "data: {\"error\": \"%s\"}\n\n", err.Error())
		flusher.Flush()
	}
}

// handleNonStreamResponse 处理非流式响应
func handleNonStreamResponse(c *gin.Context, messages []kiroclient.ChatMessage, format string, model string) {
	// 计算输入 token 数
	inputTokens := kiroclient.CountMessagesTokens(messages)

	response, err := client.Chat.ChatWithModel(messages, model)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 计算输出 token 数
	outputTokens := kiroclient.CountTokens(response)

	if format == "openai" {
		// OpenAI 格式响应（完整版，对齐 new-api）
		resp := OpenAIChatResponse{
			ID:                generateID("chatcmpl"),
			Object:            "chat.completion",
			Created:           time.Now().Unix(),
			Model:             model,
			SystemFingerprint: nil,
			Choices: []OpenAIChatChoice{
				{
					Index: 0,
					Message: OpenAIChatMessage{
						Role:    "assistant",
						Content: response,
					},
					FinishReason: "stop",
				},
			},
			Usage: &kiroclient.OpenAIUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      inputTokens + outputTokens,
				PromptTokensDetails: kiroclient.InputTokenDetails{
					CachedTokens: 0,
					TextTokens:   inputTokens,
					AudioTokens:  0,
					ImageTokens:  0,
				},
				CompletionTokenDetails: kiroclient.OutputTokenDetails{
					TextTokens:      outputTokens,
					AudioTokens:     0,
					ReasoningTokens: 0,
				},
			},
		}
		// 累加全局统计
		addTokenStats(inputTokens, outputTokens)
		c.JSON(200, resp)
	} else {
		// Claude 格式响应（完整版，对齐 new-api）
		resp := ClaudeChatResponse{
			ID:         generateID("msg"),
			Type:       "message",
			Role:       "assistant",
			Model:      model,
			StopReason: "end_turn",
			Content: []ClaudeContentBlock{
				{
					Type: "text",
					Text: response,
				},
			},
			Usage: &kiroclient.ClaudeUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			},
		}
		// 累加全局统计
		addTokenStats(inputTokens, outputTokens)
		c.JSON(200, resp)
	}
}

// handleModelsList 获取模型列表
func handleModelsList(c *gin.Context) {
	c.JSON(200, gin.H{
		"models": kiroclient.AvailableModels,
	})
}

// loadModelMapping 从文件加载模型映射配置
func loadModelMapping() {
	// 尝试从文件加载
	data, err := os.ReadFile(modelMappingFile)
	if err != nil {
		// 文件不存在或读取失败，使用默认映射
		modelMapping = make(kiroclient.ModelMapping)
		for k, v := range kiroclient.DefaultModelMapping {
			modelMapping[k] = v
		}
		return
	}

	// 解析JSON
	var mapping kiroclient.ModelMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		// 解析失败，使用默认映射
		modelMapping = make(kiroclient.ModelMapping)
		for k, v := range kiroclient.DefaultModelMapping {
			modelMapping[k] = v
		}
		return
	}

	modelMapping = mapping
}

// saveModelMapping 保存模型映射配置到文件
func saveModelMapping() error {
	data, err := json.MarshalIndent(modelMapping, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(modelMappingFile, data, 0644)
}

// handleGetModelMapping 获取当前模型映射配置
func handleGetModelMapping(c *gin.Context) {
	// 计算 hash 用于乐观锁
	data, _ := json.Marshal(modelMapping)
	hash := computeHash(data)
	c.JSON(200, gin.H{
		"mapping": modelMapping,
		"default": kiroclient.DefaultModelMapping,
		"hash":    hash,
	})
}

// handleUpdateModelMapping 更新模型映射配置
func handleUpdateModelMapping(c *gin.Context) {
	var req struct {
		Mapping map[string]string `json:"mapping"`
		Hash    string            `json:"hash"` // 乐观锁 hash
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 校验 hash（乐观锁）
	if req.Hash != "" {
		currentData, _ := json.Marshal(modelMapping)
		currentHash := computeHash(currentData)
		if req.Hash != currentHash {
			c.JSON(409, gin.H{"error": "配置已被修改，请刷新后重试"})
			return
		}
	}

	// 更新映射
	modelMapping = req.Mapping

	// 保存到文件
	if err := saveModelMapping(); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("保存映射配置失败: %s", err.Error())})
		return
	}

	// 返回新的 hash
	newData, _ := json.Marshal(modelMapping)
	newHash := computeHash(newData)
	c.JSON(200, gin.H{"message": "模型映射配置已更新", "hash": newHash})
}

// handleStartLogin 开始登录流程
func handleStartLogin(c *gin.Context) {
	var req struct {
		Region   string `json:"region"`
		StartUrl string `json:"startUrl"` // 企业 SSO URL，空表示 Builder ID
	}
	c.ShouldBindJSON(&req)

	if req.Region == "" {
		req.Region = "us-east-1"
	}

	// 开始登录流程
	session, err := client.Auth.StartLogin(req.Region, req.StartUrl)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 保存会话到内存缓存
	sessionMutex.Lock()
	loginSessions[session.SessionID] = session
	sessionMutex.Unlock()

	c.JSON(200, gin.H{
		"sessionId": session.SessionID,
		"userCode":  session.UserCode,
		"verifyUrl": session.VerifyURL,
		"expiresAt": session.ExpiresAt,
		"interval":  session.Interval,
		"authType":  session.AuthType,
	})
}

// handleImportAccount 导入账号（支持企业 SSO Token）
func handleImportAccount(c *gin.Context) {
	var req struct {
		TokenJSON     string `json:"tokenJson"`
		ClientRegJSON string `json:"clientRegJson"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if req.TokenJSON == "" {
		c.JSON(400, gin.H{"error": "tokenJson 不能为空"})
		return
	}

	account, err := client.Auth.ImportAccount(req.TokenJSON, req.ClientRegJSON)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"message": "账号导入成功",
		"account": account,
	})
}

// handlePollLogin 轮询登录状态
func handlePollLogin(c *gin.Context) {
	sessionID := c.Param("sessionId")

	// 从缓存获取会话
	sessionMutex.RLock()
	session, exists := loginSessions[sessionID]
	sessionMutex.RUnlock()

	if !exists {
		c.JSON(404, gin.H{"error": "会话不存在或已过期"})
		return
	}

	// 检查会话是否过期
	if time.Now().Unix() > session.ExpiresAt {
		sessionMutex.Lock()
		delete(loginSessions, sessionID)
		sessionMutex.Unlock()
		c.JSON(400, gin.H{"error": "会话已过期，请重新登录"})
		return
	}

	// 尝试完成登录
	account, err := client.Auth.CompleteLogin(session)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// account 为 nil 表示需要继续轮询
	if account == nil {
		c.JSON(200, gin.H{
			"status":  "pending",
			"message": "等待用户授权...",
		})
		return
	}

	// 登录成功，清理会话缓存
	sessionMutex.Lock()
	delete(loginSessions, sessionID)
	sessionMutex.Unlock()

	// 重新初始化客户端以加载新 Token
	client = kiroclient.NewKiroClient()

	c.JSON(200, gin.H{
		"status":  "success",
		"message": "登录成功",
		"account": account,
	})
}

// AccountWithUsage 带额度信息的账号
type AccountWithUsage struct {
	kiroclient.AccountInfo
	UsedCredits      float64 `json:"usedCredits"`
	TotalCredits     float64 `json:"totalCredits"`
	DaysUntilReset   int     `json:"daysUntilReset"`
	NextResetDate    string  `json:"nextResetDate"`
	SubscriptionName string  `json:"subscriptionName"`
	TokenExpiresAt   string  `json:"tokenExpiresAt"`
	TokenMinutesLeft int     `json:"tokenMinutesLeft"`
}

// handleListAccounts 获取账号列表（含额度信息）
func handleListAccounts(c *gin.Context) {
	config, err := client.Auth.LoadAccountsConfig()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 为每个账号获取额度信息
	result := make([]AccountWithUsage, 0, len(config.Accounts))
	for _, acc := range config.Accounts {
		item := AccountWithUsage{AccountInfo: acc}

		// 计算 Token 过期时间
		if acc.Token != nil && acc.Token.ExpiresAt != "" {
			item.TokenExpiresAt = acc.Token.ExpiresAt
			expTime, err := time.Parse(time.RFC3339, acc.Token.ExpiresAt)
			if err == nil {
				minLeft := int(time.Until(expTime).Minutes())
				if minLeft < 0 {
					minLeft = 0
				}
				item.TokenMinutesLeft = minLeft
			}
		}

		// 尝试获取该账号的额度（使用账号的 Token 和 ProfileArn）
		if acc.Token != nil && acc.Token.AccessToken != "" {
			usage, err := client.Auth.GetUsageLimitsWithToken(acc.Token.AccessToken, acc.Token.Region, acc.ProfileArn)
			if err != nil {
				fmt.Printf("[账号 %s] 获取额度失败: %v\n", acc.ID, err)
			} else if len(usage.UsageBreakdownList) > 0 {
				for _, u := range usage.UsageBreakdownList {
					if u.ResourceType == "CREDIT" {
						item.UsedCredits = u.CurrentUsageWithPrecision
						item.TotalCredits = u.UsageLimitWithPrecision
						break
					}
				}
				if usage.NextDateReset > 0 {
					resetTime := time.Unix(int64(usage.NextDateReset), 0)
					days := int(time.Until(resetTime).Hours() / 24)
					if days < 0 {
						days = 0
					}
					item.DaysUntilReset = days
					item.NextResetDate = resetTime.Format("2006-01-02")
				}
				subName := usage.SubscriptionInfo.SubscriptionTitle
				if len(subName) > 5 && subName[:5] == "KIRO " {
					subName = subName[5:]
				}
				item.SubscriptionName = subName
				// 同时更新 userId 和 email（如果原来为空）
				if item.UserId == "" && usage.UserInfo.UserId != "" {
					item.UserId = usage.UserInfo.UserId
				}
				if item.Email == "" && usage.UserInfo.Email != "" {
					item.Email = usage.UserInfo.Email
				}
			}
		}
		result = append(result, item)
	}

	c.JSON(200, gin.H{
		"accounts": result,
	})
}

// handleDeleteAccount 删除账号
func handleDeleteAccount(c *gin.Context) {
	accountID := c.Param("id")

	if err := client.Auth.DeleteAccount(accountID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "账号已删除"})
}

// handleRefreshAccount 刷新账号 Token
func handleRefreshAccount(c *gin.Context) {
	accountID := c.Param("id")

	if err := client.Auth.RefreshAccountToken(accountID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 重新初始化客户端
	client = kiroclient.NewKiroClient()

	c.JSON(200, gin.H{"message": "Token 已刷新"})
}

// handleRefreshAllAccounts 刷新所有账号的 Token
func handleRefreshAllAccounts(c *gin.Context) {
	client.Auth.RefreshAllAccounts()
	c.JSON(200, gin.H{"message": "已触发全部账号刷新"})
}

// AccountDetailResponse 账号详情响应
type AccountDetailResponse struct {
	// 基本信息
	ID          string `json:"id"`
	Email       string `json:"email"`
	UserId      string `json:"userId"`
	Provider    string `json:"provider"`
	Region      string `json:"region"`
	CreatedAt   string `json:"createdAt"`
	TokenExpiry string `json:"tokenExpiry"`
	IsExpired   bool   `json:"isExpired"`
	MinutesLeft int    `json:"minutesLeft"`

	// 订阅信息
	SubscriptionName string `json:"subscriptionName"`
	ResourceType     string `json:"resourceType"`
	OverageRate      string `json:"overageRate"`
	CanUpgrade       bool   `json:"canUpgrade"`

	// 额度信息
	UsedCredits    float64 `json:"usedCredits"`
	TotalCredits   float64 `json:"totalCredits"`
	DaysUntilReset int     `json:"daysUntilReset"`
	NextResetDate  string  `json:"nextResetDate"`

	// 额度明细（主配额、免费试用、奖励）
	MainQuota  QuotaDetail `json:"mainQuota"`
	FreeQuota  QuotaDetail `json:"freeQuota"`
	BonusQuota QuotaDetail `json:"bonusQuota"`

	// 可用模型
	Models []kiroclient.Model `json:"models"`
}

// QuotaDetail 额度明细
type QuotaDetail struct {
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
}

// handleAccountDetail 获取账号详情
func handleAccountDetail(c *gin.Context) {
	accountID := c.Param("id")

	config, err := client.Auth.LoadAccountsConfig()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// 查找账号
	var account *kiroclient.AccountInfo
	for i := range config.Accounts {
		if config.Accounts[i].ID == accountID {
			account = &config.Accounts[i]
			break
		}
	}

	if account == nil {
		c.JSON(404, gin.H{"error": "账号不存在"})
		return
	}

	// 构建响应
	resp := AccountDetailResponse{
		ID:        account.ID,
		Email:     account.Email,
		UserId:    account.UserId,
		CreatedAt: account.CreatedAt,
	}

	// Token 信息
	if account.Token != nil {
		resp.Provider = account.Token.Provider
		resp.Region = account.Token.Region
		resp.TokenExpiry = account.Token.ExpiresAt
		resp.IsExpired = account.Token.IsExpired()

		if account.Token.ExpiresAt != "" {
			expTime, _ := time.Parse(time.RFC3339, account.Token.ExpiresAt)
			minLeft := int(time.Until(expTime).Minutes())
			if minLeft < 0 {
				minLeft = 0
			}
			resp.MinutesLeft = minLeft
		}

		// 获取额度信息
		usage, err := client.Auth.GetUsageLimitsWithToken(account.Token.AccessToken, account.Token.Region, account.ProfileArn)
		if err == nil && usage != nil {
			// 订阅信息
			subName := usage.SubscriptionInfo.SubscriptionTitle
			if len(subName) > 5 && subName[:5] == "KIRO " {
				subName = subName[5:]
			}
			resp.SubscriptionName = subName

			// 额度明细
			for _, u := range usage.UsageBreakdownList {
				if u.ResourceType == "CREDIT" {
					resp.UsedCredits = u.CurrentUsageWithPrecision
					resp.TotalCredits = u.UsageLimitWithPrecision
					resp.ResourceType = u.ResourceType
					resp.OverageRate = "$0.04/INVOCATIONS"
					resp.MainQuota = QuotaDetail{
						Used:  u.CurrentUsageWithPrecision,
						Total: u.UsageLimitWithPrecision,
					}
				}
			}

			// 重置时间
			if usage.NextDateReset > 0 {
				resetTime := time.Unix(int64(usage.NextDateReset), 0)
				days := int(time.Until(resetTime).Hours() / 24)
				if days < 0 {
					days = 0
				}
				resp.DaysUntilReset = days
				resp.NextResetDate = resetTime.Format("2006-01-02")
			}

			// 更新 userId
			if resp.UserId == "" && usage.UserInfo.UserId != "" {
				resp.UserId = usage.UserInfo.UserId
			}
			if resp.Email == "" && usage.UserInfo.Email != "" {
				resp.Email = usage.UserInfo.Email
			}
		}
	}

	// 获取可用模型
	resp.Models = kiroclient.AvailableModels

	c.JSON(200, resp)
}
