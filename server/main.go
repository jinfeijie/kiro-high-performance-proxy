package main

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
// 格式：prefix_timestamp_randomhex，如 msg_1770269464010833000_02a2633eb6b49c97
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

// Claude 格式请求（完整版，支持 MCP tools 透传）
type ClaudeChatRequest struct {
	Model         string           `json:"model"`
	Messages      []map[string]any `json:"messages"`
	MaxTokens     int              `json:"max_tokens"`
	Stream        bool             `json:"stream"`
	System        any              `json:"system,omitempty"`
	Tools         any              `json:"tools,omitempty"`
	ToolChoice    any              `json:"tool_choice,omitempty"`
	Temperature   *float64         `json:"temperature,omitempty"`
	TopP          float64          `json:"top_p,omitempty"`
	TopK          int              `json:"top_k,omitempty"`
	StopSequences []string         `json:"stop_sequences,omitempty"`
	Metadata      any              `json:"metadata,omitempty"`
	OutputConfig  any              `json:"output_config,omitempty"`
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

// ========== 全局结构化日志记录器 ==========
var logger *StructuredLogger

// ========== IP 黑名单 ==========
var ipBlacklistFile = "ip-blacklist.json"
var ipBlacklist []string
var ipBlacklistMutex sync.RWMutex

// ========== 限流器 ==========
var rateLimitFile = "rate-limit.json"
var rateLimitConfig RateLimitConfig
var rateLimitMutex sync.RWMutex
var requestCounts = make(map[string]*RequestCounter) // IP -> 计数器
var requestCountsMutex sync.RWMutex

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	Enabled        bool `json:"enabled"`
	RequestsPerMin int  `json:"requestsPerMin"` // 每分钟最大请求数
	PenaltySeconds int  `json:"penaltySeconds"` // 超限惩罚延迟秒数
}

// RequestCounter 请求计数器（滑动窗口）
type RequestCounter struct {
	Count     int
	WindowEnd int64 // 窗口结束时间戳
}

// ========== 全局 Token 统计 ==========
var tokenStatsFile = "token-stats.json"
var tokenStats TokenStats
var tokenStatsMutex sync.RWMutex
var tokenStatsChan = make(chan TokenDelta, 1000) // 异步写入通道

// ========== 账号调用统计 ==========
var accountStatsFile = "account-stats.json"
var accountStats = make(map[string]*AccountStats) // accountID -> 统计
var accountStatsMutex sync.RWMutex

// AccountStats 单个账号的统计数据
type AccountStats struct {
	AccountID    string           `json:"accountId"`
	RequestCount int64            `json:"requestCount"`
	SuccessCount int64            `json:"successCount"`
	FailCount    int64            `json:"failCount"`
	StatusCodes  map[int]int64    `json:"statusCodes"` // 状态码 -> 次数
	Errors       map[string]int64 `json:"errors"`      // 错误类型 -> 次数
	UpdatedAt    int64            `json:"updatedAt"`
}

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
		if logger != nil {
			logger.Info("", "Token 统计: 新建", nil)
		}
		return
	}
	if err := json.Unmarshal(data, &tokenStats); err != nil {
		tokenStats = TokenStats{}
	}
	if logger != nil {
		logger.Info("", "Token 统计: 已加载", map[string]any{
			"inputTokens":  tokenStats.InputTokens,
			"outputTokens": tokenStats.OutputTokens,
			"totalTokens":  tokenStats.TotalTokens,
		})
	}
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

// ========== 账号统计函数 ==========

// loadAccountStats 启动时加载账号统计数据
func loadAccountStats() {
	data, err := os.ReadFile(accountStatsFile)
	if err != nil {
		if logger != nil {
			logger.Info("", "账号统计: 新建", nil)
		}
		return
	}
	var stats map[string]*AccountStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return
	}
	accountStatsMutex.Lock()
	accountStats = stats
	accountStatsMutex.Unlock()
	if logger != nil {
		logger.Info("", "账号统计: 已加载", map[string]any{
			"accountCount": len(stats),
		})
	}
}

// saveAccountStats 保存账号统计数据
func saveAccountStats() {
	accountStatsMutex.RLock()
	data, _ := json.MarshalIndent(accountStats, "", "  ")
	accountStatsMutex.RUnlock()
	os.WriteFile(accountStatsFile, data, 0644)
}

// recordAccountRequest 记录账号请求（状态码和错误）
func recordAccountRequest(accountID string, statusCode int, errMsg string) {
	if accountID == "" {
		return
	}

	accountStatsMutex.Lock()
	defer accountStatsMutex.Unlock()

	stats, exists := accountStats[accountID]
	if !exists {
		stats = &AccountStats{
			AccountID:   accountID,
			StatusCodes: make(map[int]int64),
			Errors:      make(map[string]int64),
		}
		accountStats[accountID] = stats
	}

	stats.RequestCount++
	stats.UpdatedAt = time.Now().Unix()

	// 记录状态码
	if stats.StatusCodes == nil {
		stats.StatusCodes = make(map[int]int64)
	}
	stats.StatusCodes[statusCode]++

	// 成功/失败计数
	if statusCode >= 200 && statusCode < 300 {
		stats.SuccessCount++
	} else {
		stats.FailCount++
		// 记录错误类型
		if errMsg != "" {
			if stats.Errors == nil {
				stats.Errors = make(map[string]int64)
			}
			stats.Errors[errMsg]++
		}
	}
}

// getAccountStats 获取所有账号统计
func getAccountStats() map[string]*AccountStats {
	accountStatsMutex.RLock()
	defer accountStatsMutex.RUnlock()
	// 返回副本
	result := make(map[string]*AccountStats)
	for k, v := range accountStats {
		result[k] = v
	}
	return result
}

// handleGetAccountStats 获取账号统计 API
func handleGetAccountStats(c *gin.Context) {
	stats := getAccountStats()

	// 构建 accountId -> email 映射
	emailMap := make(map[string]string)
	if accountsConfig, err := client.Auth.LoadAccountsConfig(); err == nil {
		for _, acc := range accountsConfig.Accounts {
			if acc.Email != "" {
				emailMap[acc.ID] = acc.Email
			}
		}
	}

	// 计算总请求数
	var totalRequests int64
	for _, s := range stats {
		totalRequests += s.RequestCount
	}

	// 构建响应数据
	accounts := make([]map[string]any, 0)
	for id, s := range stats {
		percent := float64(0)
		if totalRequests > 0 {
			percent = float64(s.RequestCount) / float64(totalRequests) * 100
		}
		accounts = append(accounts, map[string]any{
			"accountId":    id,
			"email":        emailMap[id], // 添加邮箱字段
			"requestCount": s.RequestCount,
			"successCount": s.SuccessCount,
			"failCount":    s.FailCount,
			"percent":      percent,
			"statusCodes":  s.StatusCodes,
			"errors":       s.Errors,
			"updatedAt":    s.UpdatedAt,
		})
	}

	c.JSON(200, gin.H{
		"accounts":      accounts,
		"totalRequests": totalRequests,
	})
}

// accountStatsWorker 后台协程定期保存账号统计
func accountStatsWorker() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		saveAccountStats()
	}
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
	if logger != nil {
		logger.Info("", "已加载 API-KEY", map[string]any{
			"count": len(apiKeys),
		})
	}
}

// saveApiKeys 保存 API-KEY 配置到文件
func saveApiKeys() error {
	data, err := json.MarshalIndent(apiKeys, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(apiKeysFile, data, 0644)
}

// loadIpBlacklist 从文件加载 IP 黑名单
func loadIpBlacklist() {
	data, err := os.ReadFile(ipBlacklistFile)
	if err != nil {
		ipBlacklist = []string{}
		return
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		ipBlacklist = []string{}
		return
	}
	ipBlacklist = list
	if logger != nil {
		logger.Info("", "已加载黑名单 IP", map[string]any{
			"count": len(ipBlacklist),
		})
	}
}

// saveIpBlacklist 保存 IP 黑名单到文件
func saveIpBlacklist() error {
	data, err := json.MarshalIndent(ipBlacklist, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ipBlacklistFile, data, 0644)
}

// ipBlacklistMiddleware IP 黑名单中间件
func ipBlacklistMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()

		ipBlacklistMutex.RLock()
		blocked := false
		for _, ip := range ipBlacklist {
			if ip == clientIP {
				blocked = true
				break
			}
		}
		ipBlacklistMutex.RUnlock()

		if blocked {
			c.JSON(403, gin.H{
				"error": map[string]any{
					"message": "IP blocked",
					"type":    "forbidden",
				},
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

// handleGetIpBlacklist 获取 IP 黑名单
func handleGetIpBlacklist(c *gin.Context) {
	ipBlacklistMutex.RLock()
	list := make([]string, len(ipBlacklist))
	copy(list, ipBlacklist)
	data, _ := json.Marshal(ipBlacklist)
	hash := computeHash(data)
	ipBlacklistMutex.RUnlock()

	c.JSON(200, gin.H{"ips": list, "count": len(list), "hash": hash})
}

// handleUpdateIpBlacklist 更新 IP 黑名单
func handleUpdateIpBlacklist(c *gin.Context) {
	var req struct {
		IPs  []string `json:"ips"`
		Hash string   `json:"hash"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	ipBlacklistMutex.Lock()
	defer ipBlacklistMutex.Unlock()

	// 乐观锁校验
	if req.Hash != "" {
		currentData, _ := json.Marshal(ipBlacklist)
		currentHash := computeHash(currentData)
		if req.Hash != currentHash {
			c.JSON(409, gin.H{"error": "配置已被修改，请刷新后重试"})
			return
		}
	}

	// 过滤空值
	var validIPs []string
	for _, ip := range req.IPs {
		if ip != "" {
			validIPs = append(validIPs, ip)
		}
	}

	ipBlacklist = validIPs
	if err := saveIpBlacklist(); err != nil {
		if logger != nil {
			RecordError(c, logger, err, "")
		}
		c.JSON(500, gin.H{"error": "保存失败: " + err.Error()})
		return
	}

	newData, _ := json.Marshal(ipBlacklist)
	newHash := computeHash(newData)
	c.JSON(200, gin.H{"message": "IP 黑名单已更新", "count": len(ipBlacklist), "hash": newHash})
}

// loadRateLimitConfig 加载限流配置
func loadRateLimitConfig() {
	data, err := os.ReadFile(rateLimitFile)
	if err != nil {
		rateLimitConfig = RateLimitConfig{Enabled: false, RequestsPerMin: 60}
		return
	}
	if err := json.Unmarshal(data, &rateLimitConfig); err != nil {
		rateLimitConfig = RateLimitConfig{Enabled: false, RequestsPerMin: 60}
		return
	}
	if logger != nil {
		logger.Info("", "限流配置已加载", map[string]any{
			"enabled":        rateLimitConfig.Enabled,
			"requestsPerMin": rateLimitConfig.RequestsPerMin,
		})
	}
}

// saveRateLimitConfig 保存限流配置
func saveRateLimitConfig() error {
	data, err := json.MarshalIndent(rateLimitConfig, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(rateLimitFile, data, 0644)
}

// rateLimitMiddleware 限流中间件（仅对 /v1/* 生效）
func rateLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rateLimitMutex.RLock()
		enabled := rateLimitConfig.Enabled
		limit := rateLimitConfig.RequestsPerMin
		penalty := rateLimitConfig.PenaltySeconds
		rateLimitMutex.RUnlock()

		if !enabled || limit <= 0 {
			c.Next()
			return
		}

		clientIP := c.ClientIP()
		now := time.Now().Unix()

		requestCountsMutex.Lock()
		counter, exists := requestCounts[clientIP]
		if !exists || now >= counter.WindowEnd {
			// 新窗口
			requestCounts[clientIP] = &RequestCounter{Count: 1, WindowEnd: now + 60}
			requestCountsMutex.Unlock()
			c.Next()
			return
		}

		counter.Count++
		if counter.Count > limit {
			requestCountsMutex.Unlock()
			// 惩罚延迟
			if penalty > 0 {
				time.Sleep(time.Duration(penalty) * time.Second)
			}
			c.JSON(500, gin.H{
				"error": map[string]any{
					"message": "Rate limit exceeded",
					"type":    "rate_limit_error",
				},
			})
			c.Abort()
			return
		}
		requestCountsMutex.Unlock()
		c.Next()
	}
}

// handleGetRateLimit 获取限流配置
func handleGetRateLimit(c *gin.Context) {
	rateLimitMutex.RLock()
	cfg := rateLimitConfig
	rateLimitMutex.RUnlock()
	c.JSON(200, gin.H{
		"enabled":        cfg.Enabled,
		"requestsPerMin": cfg.RequestsPerMin,
		"penaltySeconds": cfg.PenaltySeconds,
	})
}

// handleUpdateRateLimit 更新限流配置
func handleUpdateRateLimit(c *gin.Context) {
	var req struct {
		Enabled        bool `json:"enabled"`
		RequestsPerMin int  `json:"requestsPerMin"`
		PenaltySeconds int  `json:"penaltySeconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	rateLimitMutex.Lock()
	rateLimitConfig.Enabled = req.Enabled
	if req.RequestsPerMin > 0 {
		rateLimitConfig.RequestsPerMin = req.RequestsPerMin
	}
	if req.PenaltySeconds >= 0 {
		rateLimitConfig.PenaltySeconds = req.PenaltySeconds
	}
	rateLimitMutex.Unlock()

	if err := saveRateLimitConfig(); err != nil {
		if logger != nil {
			RecordError(c, logger, err, "")
		}
		c.JSON(500, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "限流配置已更新"})
}

// handleGetLogLevel 获取日志级别配置
func handleGetLogLevel(c *gin.Context) {
	if logger == nil {
		c.JSON(200, gin.H{
			"level":     "INFO",
			"levelName": "INFO",
			"available": []string{"DEBUG", "INFO", "WARN", "ERROR"},
		})
		return
	}
	level := logger.GetLevel()
	c.JSON(200, gin.H{
		"level":     int(level),
		"levelName": level.String(),
		"available": []string{"DEBUG", "INFO", "WARN", "ERROR"},
	})
}

// handleUpdateLogLevel 更新日志级别配置
func handleUpdateLogLevel(c *gin.Context) {
	var req struct {
		Level string `json:"level"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	if req.Level == "" {
		c.JSON(400, gin.H{"error": "level 不能为空"})
		return
	}

	newLevel := ParseLogLevel(req.Level)
	if logger != nil {
		logger.SetLevel(newLevel)
	}

	c.JSON(200, gin.H{
		"message":   "日志级别已更新",
		"level":     int(newLevel),
		"levelName": newLevel.String(),
	})
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
	// 初始化全局结构化日志记录器
	var err error
	logger, err = NewStructuredLogger("", 0)
	if err != nil {
		fmt.Printf("⚠️ 初始化日志记录器失败: %v\n", err)
	} else {
		logger.Info("", "日志系统初始化完成", map[string]any{
			"output": "stdout",
		})
	}

	// 初始化 Kiro 客户端
	client = kiroclient.NewKiroClient()

	// 初始化账号缓存（从文件加载到内存）
	if err := client.Auth.InitAccountsCache(); err != nil {
		if logger != nil {
			logger.Warn("", "初始化账号缓存失败", map[string]any{
				"error": err.Error(),
			})
		}
	} else {
		if logger != nil {
			logger.Info("", "账号缓存初始化完成", nil)
		}
	}

	// 加载模型映射配置
	loadModelMapping()

	// 加载 API-KEY 配置
	loadApiKeys()

	// 加载 IP 黑名单
	loadIpBlacklist()

	// 加载限流配置
	loadRateLimitConfig()

	// 加载 Token 统计数据并启动后台写入协程
	loadTokenStats()
	go tokenStatsWorker()

	// 加载账号统计数据并启动后台写入协程
	loadAccountStats()
	go accountStatsWorker()

	// 启动保活机制（后台自动刷新所有账号的 Token）
	client.Auth.StartKeepAlive()
	if logger != nil {
		logger.Info("", "保活机制已启动", map[string]any{
			"interval": "5分钟",
		})
	}

	r := gin.Default()

	// 注册 pprof 路由
	pprof.Register(r)

	// 注册请求追踪中间件（必须在其他中间件之前）
	if logger != nil {
		r.Use(TraceMiddleware(logger))
	}

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

	// IP 黑名单中间件（全局生效）
	r.Use(ipBlacklistMiddleware())

	// 静态文件服务 - 支持从 server 目录或项目根目录启动
	staticPath := "./static"
	if _, err := os.Stat(staticPath); os.IsNotExist(err) {
		staticPath = "./server/static"
	}
	r.Static("/static", staticPath)
	r.GET("/", func(c *gin.Context) {
		indexPath := staticPath + "/index.html"
		c.File(indexPath)
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

		// IP 黑名单管理
		api.GET("/settings/ip-blacklist", handleGetIpBlacklist)
		api.POST("/settings/ip-blacklist", handleUpdateIpBlacklist)

		// 限流配置
		api.GET("/settings/rate-limit", handleGetRateLimit)
		api.POST("/settings/rate-limit", handleUpdateRateLimit)

		// 日志级别配置
		api.GET("/settings/log-level", handleGetLogLevel)
		api.POST("/settings/log-level", handleUpdateLogLevel)

		// Token 统计
		api.GET("/stats", handleGetStats)

		// 账号统计
		api.GET("/stats/accounts", handleGetAccountStats)

		// Chat 接口
		api.POST("/chat", handleChat)

		// 搜索接口
		api.POST("/search", handleSearch)

		// MCP 工具
		api.GET("/tools", handleToolsList)
		api.POST("/tools/call", handleToolsCall)
	}

	// OpenAI 格式接口（兼容）- 需要 API-KEY 验证 + 限流
	r.POST("/v1/chat/completions", rateLimitMiddleware(), apiKeyAuthMiddleware(), handleOpenAIChat)

	// Claude 格式接口（兼容）- 需要 API-KEY 验证 + 限流
	r.POST("/v1/messages", rateLimitMiddleware(), apiKeyAuthMiddleware(), handleClaudeChat)

	// Claude Code token 计数端点（模拟响应）
	r.POST("/v1/messages/count_tokens", apiKeyAuthMiddleware(), handleCountTokens)

	// Claude Code 遥测端点（直接返回 200 OK）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Anthropic 原生格式接口（兼容）- 需要 API-KEY 验证 + 限流
	r.POST("/anthropic/v1/messages", rateLimitMiddleware(), apiKeyAuthMiddleware(), handleClaudeChat)

	// 从环境变量读取端口，默认 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if logger != nil {
		logger.Info("", "Kiro API Proxy 启动成功", map[string]any{
			"port":      port,
			"webUI":     "http://localhost:" + port,
			"openai":    "POST /v1/chat/completions",
			"claude":    "POST /v1/messages",
			"anthropic": "POST /anthropic/v1/messages",
			"pprof":     "http://localhost:" + port + "/debug/pprof/",
		})
	}

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
		if logger != nil {
			logger.Warn(GetMsgID(c), "获取额度信息失败", map[string]any{
				"error": err.Error(),
			})
		}
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
			err := fmt.Errorf("streaming not supported")
			if logger != nil {
				RecordError(c, logger, err, "")
			}
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
			if logger != nil {
				RecordError(c, logger, err, "")
			}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"results": results})
}

// handleToolsList 获取工具列表
func handleToolsList(c *gin.Context) {
	tools, err := client.MCP.ToolsList()
	if err != nil {
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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

	// 记录 OpenAI 格式请求输入
	if logger != nil {
		logger.Info(GetMsgID(c), "OpenAI 请求输入", map[string]any{
			"model":    req.Model,
			"stream":   req.Stream,
			"msgCount": len(req.Messages),
		})
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

// CountTokensRequest token 计数请求
type CountTokensRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	System   any              `json:"system,omitempty"`
}

// handleCountTokens 处理 Claude Code token 计数请求（模拟响应）
// 参考 Kiro-account-manager 实现：按 4 字符 ≈ 1 token 估算
func handleCountTokens(c *gin.Context) {
	var req CountTokensRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid request body"})
		return
	}

	// 计算总字符数
	totalChars := 0

	// 遍历 messages
	for _, msg := range req.Messages {
		content := msg["content"]
		switch v := content.(type) {
		case string:
			totalChars += len(v)
		case []interface{}:
			for _, part := range v {
				if m, ok := part.(map[string]interface{}); ok {
					if m["type"] == "text" {
						if text, ok := m["text"].(string); ok {
							totalChars += len(text)
						}
					}
				}
			}
		}
	}

	// 计算 system 字符数
	if req.System != nil {
		switch v := req.System.(type) {
		case string:
			totalChars += len(v)
		default:
			// 复杂格式序列化后计算
			data, _ := json.Marshal(v)
			totalChars += len(data)
		}
	}

	// 估算 token 数（4 字符 ≈ 1 token）
	estimatedTokens := (totalChars + 3) / 4
	if estimatedTokens < 1 {
		estimatedTokens = 1
	}

	c.JSON(200, gin.H{"input_tokens": estimatedTokens})
}

// handleClaudeChat 处理 Claude 格式请求
func handleClaudeChat(c *gin.Context) {
	// 调试：记录原始请求体
	bodyBytes, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	if logger != nil {
		logger.Debug(GetMsgID(c), "Claude 原始请求", map[string]any{
			"body": string(bodyBytes),
		})
	}

	var req ClaudeChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// Claude 请求输入日志已禁用（减少日志噪音）

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

	// 转换消息格式（支持 system、tools、tool_use、tool_result）
	messages, tools, toolResults := convertToKiroMessagesWithSystem(req.Messages, req.System, req.Tools)

	if req.Stream {
		handleStreamResponseWithTools(c, messages, tools, toolResults, "claude", req.Model)
	} else {
		handleNonStreamResponseWithTools(c, messages, tools, toolResults, "claude", req.Model)
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

// convertToKiroMessagesWithSystem 转换消息格式（支持 system 和 tools）
// 返回：messages, tools, lastToolResults（只返回最后一条 user 消息的 toolResults）
// 参考 Kiro-account-manager/translator.ts 的 claudeToKiro 实现
func convertToKiroMessagesWithSystem(messages []map[string]any, system any, tools any) ([]kiroclient.ChatMessage, []kiroclient.KiroToolWrapper, []kiroclient.KiroToolResult) {
	var kiroMessages []kiroclient.ChatMessage
	var kiroTools []kiroclient.KiroToolWrapper

	// 提取 system prompt（将合并到最后一条 user 消息）
	systemPrompt := extractSystemPrompt(system)

	// 转换 tools
	kiroTools = convertClaudeTools(tools)

	for _, msg := range messages {
		role, _ := msg["role"].(string)

		var content string
		var images []kiroclient.ImageBlock
		var msgToolResults []kiroclient.KiroToolResult
		var msgToolUses []kiroclient.KiroToolUse

		switch v := msg["content"].(type) {
		case string:
			content = v
		case []interface{}:
			for _, item := range v {
				m, ok := item.(map[string]interface{})
				if !ok {
					continue
				}

				itemType, _ := m["type"].(string)

				switch itemType {
				case "text":
					if text, ok := m["text"].(string); ok {
						content += text
					}

				case "image_url":
					if imgObj, ok := m["image_url"].(map[string]interface{}); ok {
						if url, ok := imgObj["url"].(string); ok {
							format, data, ok := kiroclient.ParseDataURL(url)
							if ok {
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
						format := ""
						if len(mediaType) > 6 && mediaType[:6] == "image/" {
							format = mediaType[6:]
						}
						if format == "" {
							continue
						}
						if format == "jpg" {
							format = "jpeg"
						}
						images = append(images, kiroclient.ImageBlock{
							Format: format,
							Source: kiroclient.ImageSource{Bytes: data},
						})
					}

				case "tool_result":
					// Claude 格式的工具结果（在 user 消息中）
					toolUseId, _ := m["tool_use_id"].(string)
					if toolUseId != "" {
						resultContent := extractToolResultContent(m["content"])
						tr := kiroclient.KiroToolResult{
							ToolUseId: toolUseId,
							Content:   []kiroclient.KiroToolContent{{Text: resultContent}},
							Status:    "success",
						}
						msgToolResults = append(msgToolResults, tr)
					}

				case "tool_use":
					// 提取 assistant 消息中的 tool_use
					toolUseId, _ := m["id"].(string)
					toolName, _ := m["name"].(string)
					toolInput, _ := m["input"].(map[string]interface{})
					if toolUseId != "" && toolName != "" {
						msgToolUses = append(msgToolUses, kiroclient.KiroToolUse{
							ToolUseId: toolUseId,
							Name:      toolName,
							Input:     toolInput,
						})
					}
				}
			}
		}

		// 处理 user 消息中包含 tool_result 的情况
		if role == "user" && len(msgToolResults) > 0 && content == "" {
			content = "Here are the tool results."
		}

		// 跳过空内容的消息（但 assistant 有 tool_use 时不跳过）
		if content == "" && len(images) == 0 && len(msgToolUses) == 0 {
			continue
		}

		kiroMessages = append(kiroMessages, kiroclient.ChatMessage{
			Role:        role,
			Content:     content,
			Images:      images,
			ToolUses:    msgToolUses,
			ToolResults: msgToolResults,
		})
	}

	// 将 system prompt 合并到最后一条 user 消息的 content 开头
	if systemPrompt != "" && len(kiroMessages) > 0 {
		for i := len(kiroMessages) - 1; i >= 0; i-- {
			if kiroMessages[i].Role == "user" {
				kiroMessages[i].Content = "--- SYSTEM PROMPT ---\n" + systemPrompt + "\n--- END SYSTEM PROMPT ---\n\n" + kiroMessages[i].Content
				break
			}
		}
	}

	// 如果没有任何消息但有 system prompt，创建一条
	if len(kiroMessages) == 0 && systemPrompt != "" {
		kiroMessages = append(kiroMessages, kiroclient.ChatMessage{
			Role:    "user",
			Content: "--- SYSTEM PROMPT ---\n" + systemPrompt + "\n--- END SYSTEM PROMPT ---\n\nContinue",
		})
	}

	// 关键修复：只返回最后一条 user 消息的 toolResults
	// 参考 TypeScript translator.ts: currentToolResults 只保存最后一条消息的 toolResults
	var lastToolResults []kiroclient.KiroToolResult
	if len(kiroMessages) > 0 {
		lastMsg := kiroMessages[len(kiroMessages)-1]
		if lastMsg.Role == "user" {
			lastToolResults = lastMsg.ToolResults
		}
	}

	return kiroMessages, kiroTools, lastToolResults
}

// extractSystemPrompt 提取 system prompt
func extractSystemPrompt(system any) string {
	if system == nil {
		return ""
	}

	switch v := system.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// convertClaudeTools 转换 Claude tools 到 Kiro 格式
func convertClaudeTools(tools any) []kiroclient.KiroToolWrapper {
	if tools == nil {
		return nil
	}

	toolsSlice, ok := tools.([]interface{})
	if !ok {
		return nil
	}

	var kiroTools []kiroclient.KiroToolWrapper
	for _, t := range toolsSlice {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := tool["name"].(string)
		description, _ := tool["description"].(string)
		inputSchema, _ := tool["input_schema"].(map[string]interface{})

		if name == "" {
			continue
		}

		// 截断过长的描述（Kiro API 限制）
		if len(description) > 10237 {
			description = description[:10237] + "..."
		}

		// 截断过长的工具名（Kiro API 限制 64 字符）
		if len(name) > 64 {
			name = name[:64]
		}

		kiroTools = append(kiroTools, kiroclient.KiroToolWrapper{
			ToolSpecification: kiroclient.KiroToolSpecification{
				Name:        name,
				Description: description,
				InputSchema: inputSchema,
			},
		})
	}

	return kiroTools
}

// extractToolResultContent 提取工具结果内容
func extractToolResultContent(content any) string {
	if content == nil {
		return ""
	}

	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return fmt.Sprintf("%v", content)
}

// handleStreamResponse 处理流式响应
// handleStreamResponse 处理流式响应
// 使用 ChatStreamWithModelAndUsage 获取 Kiro API 返回的精确 token 使用量
func handleStreamResponse(c *gin.Context, messages []kiroclient.ChatMessage, format string, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		err := fmt.Errorf("streaming not supported")
		if logger != nil {
			RecordError(c, logger, err, "")
		}
		c.JSON(500, gin.H{"error": "Streaming not supported"})
		return
	}

	// 本地估算的 inputTokens（用于 message_start 事件，因为此时还没有 API 返回值）
	estimatedInputTokens := kiroclient.CountMessagesTokens(messages)
	var outputBuilder strings.Builder
	msgID := generateID("msg")
	chatcmplID := generateID("chatcmpl")
	// 保存估算的 outputTokens（用于 SSE 事件，因为回调中无法获取 usage）
	var estimatedOutputTokens int

	// Claude 格式：先发送 message_start 事件（使用估算值）
	if format == "claude" {
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    msgID,
				"type":  "message",
				"role":  "assistant",
				"model": model,
				"usage": map[string]int{
					"input_tokens":  estimatedInputTokens,
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

	// 使用 ChatStreamWithModelAndUsage 获取精确 usage
	usage, err := client.Chat.ChatStreamWithModelAndUsage(messages, model, func(content string, done bool) {
		if done {
			// 使用本地估算值发送 SSE 事件（因为此时 usage 还未返回）
			estimatedOutputTokens = kiroclient.CountTokens(outputBuilder.String())

			if format == "openai" {
				// OpenAI 流式结束前发送带 usage 的 chunk（使用估算值）
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
						"prompt_tokens":     estimatedInputTokens,
						"completion_tokens": estimatedOutputTokens,
						"total_tokens":      estimatedInputTokens + estimatedOutputTokens,
						"prompt_tokens_details": map[string]int{
							"cached_tokens": 0,
							"text_tokens":   estimatedInputTokens,
							"audio_tokens":  0,
							"image_tokens":  0,
						},
						"completion_tokens_details": map[string]int{
							"text_tokens":      estimatedOutputTokens,
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

				// 发送 message_delta 事件（使用估算值）
				msgDelta := map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason":   "end_turn",
						"stop_sequence": nil,
					},
					"usage": map[string]int{
						"output_tokens": estimatedOutputTokens,
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
		// 记录账号请求失败
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 500, err.Error())
		// 记录流式响应错误
		if logger != nil {
			logger.Error(GetMsgID(c), "流式响应失败", map[string]any{
				"format":    format,
				"model":     model,
				"error":     err.Error(),
				"accountId": accountID,
			})
		}
		fmt.Fprintf(c.Writer, "data: {\"error\": \"%s\"}\n\n", err.Error())
		flusher.Flush()
	} else {
		// 记录账号请求成功
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 200, "")

		// 使用精确 usage（如果可用），否则降级使用估算值
		inputTokens := estimatedInputTokens
		outputTokens := estimatedOutputTokens
		if usage != nil {
			inputTokens = usage.InputTokens
			outputTokens = usage.OutputTokens
		}

		// 累加全局统计（使用精确值）
		addTokenStats(inputTokens, outputTokens)

		// 流式响应完成日志已禁用（减少日志噪音）
	}
}

// handleNonStreamResponse 处理非流式响应
// handleNonStreamResponse 处理非流式响应
// 使用 ChatStreamWithModelAndUsage 获取 Kiro API 返回的精确 token 使用量
func handleNonStreamResponse(c *gin.Context, messages []kiroclient.ChatMessage, format string, model string) {
	// 本地估算的 inputTokens（降级使用）
	estimatedInputTokens := kiroclient.CountMessagesTokens(messages)

	// 收集完整响应
	var responseBuilder strings.Builder

	// 使用 ChatStreamWithModelAndUsage 获取精确 usage
	usage, err := client.Chat.ChatStreamWithModelAndUsage(messages, model, func(content string, done bool) {
		if !done {
			responseBuilder.WriteString(content)
		}
	})

	if err != nil {
		// 记录账号请求失败
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 500, err.Error())
		if logger != nil {
			RecordError(c, logger, err, accountID)
			logger.Error(GetMsgID(c), "非流式响应失败", map[string]any{
				"format":    format,
				"model":     model,
				"error":     err.Error(),
				"accountId": accountID,
			})
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	response := responseBuilder.String()

	// 记录账号请求成功
	accountID := client.Auth.GetLastSelectedAccountID()
	recordAccountRequest(accountID, 200, "")

	// 使用精确 usage（如果可用），否则降级使用估算值
	inputTokens := estimatedInputTokens
	outputTokens := kiroclient.CountTokens(response)
	cacheReadTokens := 0
	cacheWriteTokens := 0
	reasoningTokens := 0
	if usage != nil {
		inputTokens = usage.InputTokens
		outputTokens = usage.OutputTokens
		cacheReadTokens = usage.CacheReadTokens
		cacheWriteTokens = usage.CacheWriteTokens
		reasoningTokens = usage.ReasoningTokens
	}

	// 非流式响应完成日志已禁用（减少日志噪音）

	if format == "openai" {
		// OpenAI 格式响应（完整版，对齐 new-api）
		// 使用精确 usage 填充 cache 和 reasoning 信息
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
				PromptTokens:         inputTokens,
				CompletionTokens:     outputTokens,
				TotalTokens:          inputTokens + outputTokens,
				PromptCacheHitTokens: cacheReadTokens,
				PromptTokensDetails: kiroclient.InputTokenDetails{
					CachedTokens: cacheReadTokens,
					TextTokens:   inputTokens - cacheReadTokens,
					AudioTokens:  0,
					ImageTokens:  0,
				},
				CompletionTokenDetails: kiroclient.OutputTokenDetails{
					TextTokens:      outputTokens - reasoningTokens,
					AudioTokens:     0,
					ReasoningTokens: reasoningTokens,
				},
			},
		}
		// 累加全局统计（使用精确值）
		addTokenStats(inputTokens, outputTokens)
		c.JSON(200, resp)
	} else {
		// Claude 格式响应（完整版，对齐 new-api）
		// 使用精确 usage 填充 cache 信息
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
				InputTokens:              inputTokens,
				OutputTokens:             outputTokens,
				CacheCreationInputTokens: cacheWriteTokens,
				CacheReadInputTokens:     cacheReadTokens,
			},
		}
		// 累加全局统计（使用精确值）
		addTokenStats(inputTokens, outputTokens)
		c.JSON(200, resp)
	}
}

// handleStreamResponseWithTools 处理流式响应（支持工具调用）
// 使用 ChatStreamWithToolsAndUsage 获取 Kiro API 返回的精确 token 使用量
func handleStreamResponseWithTools(c *gin.Context, messages []kiroclient.ChatMessage, tools []kiroclient.KiroToolWrapper, toolResults []kiroclient.KiroToolResult, format string, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		err := fmt.Errorf("streaming not supported")
		if logger != nil {
			RecordError(c, logger, err, "")
		}
		c.JSON(500, gin.H{"error": "Streaming not supported"})
		return
	}

	// 本地估算的 inputTokens（用于 message_start 事件，因为此时还没有 API 返回值）
	estimatedInputTokens := kiroclient.CountMessagesTokens(messages)
	var outputBuilder strings.Builder
	msgID := generateID("msg")
	contentBlockIndex := 0

	// Claude 格式：发送 message_start 事件（使用估算值）
	if format == "claude" {
		msgStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    msgID,
				"type":  "message",
				"role":  "assistant",
				"model": model,
				"usage": map[string]int{
					"input_tokens":  estimatedInputTokens,
					"output_tokens": 0,
				},
			},
		}
		data, _ := json.Marshal(msgStart)
		fmt.Fprintf(c.Writer, "event: message_start\ndata: %s\n\n", string(data))
		flusher.Flush()
	}

	// 标记是否已发送文本块开始
	textBlockStarted := false
	// 保存估算的 outputTokens（用于 message_delta 事件）
	var estimatedOutputTokens int

	// 使用 ChatStreamWithToolsAndUsage 获取精确 usage
	usage, err := client.Chat.ChatStreamWithToolsAndUsage(messages, model, tools, toolResults, func(content string, toolUse *kiroclient.KiroToolUse, done bool) {
		if done {
			// 使用本地估算值发送 SSE 事件（因为此时 usage 还未返回）
			estimatedOutputTokens = kiroclient.CountTokens(outputBuilder.String())

			// 关闭文本块（如果已开始）
			if textBlockStarted {
				blockStop := map[string]any{
					"type":  "content_block_stop",
					"index": contentBlockIndex - 1,
				}
				data, _ := json.Marshal(blockStop)
				fmt.Fprintf(c.Writer, "event: content_block_stop\ndata: %s\n\n", string(data))
			}

			// 发送 message_delta 事件
			stopReason := "end_turn"
			if contentBlockIndex > 0 {
				// 如果有工具调用，stop_reason 为 tool_use
				stopReason = "tool_use"
			}
			msgDelta := map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason":   stopReason,
					"stop_sequence": nil,
				},
				"usage": map[string]int{
					"output_tokens": estimatedOutputTokens,
				},
			}
			data, _ := json.Marshal(msgDelta)
			fmt.Fprintf(c.Writer, "event: message_delta\ndata: %s\n\n", string(data))

			// 发送 message_stop 事件
			msgStop := map[string]any{"type": "message_stop"}
			data, _ = json.Marshal(msgStop)
			fmt.Fprintf(c.Writer, "event: message_stop\ndata: %s\n\n", string(data))

			flusher.Flush()
			return
		}

		// 处理文本内容
		if content != "" {
			// 如果还没开始文本块，先发送 content_block_start
			if !textBlockStarted {
				blockStart := map[string]any{
					"type":  "content_block_start",
					"index": contentBlockIndex,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				}
				data, _ := json.Marshal(blockStart)
				fmt.Fprintf(c.Writer, "event: content_block_start\ndata: %s\n\n", string(data))
				textBlockStarted = true
				contentBlockIndex++
			}

			outputBuilder.WriteString(content)

			// 发送 content_block_delta
			chunk := map[string]any{
				"type":  "content_block_delta",
				"index": contentBlockIndex - 1,
				"delta": map[string]string{
					"type": "text_delta",
					"text": content,
				},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(c.Writer, "event: content_block_delta\ndata: %s\n\n", string(data))
			flusher.Flush()
		}

		// 处理工具调用
		if toolUse != nil {
			// 关闭之前的文本块
			if textBlockStarted {
				blockStop := map[string]any{
					"type":  "content_block_stop",
					"index": contentBlockIndex - 1,
				}
				data, _ := json.Marshal(blockStop)
				fmt.Fprintf(c.Writer, "event: content_block_stop\ndata: %s\n\n", string(data))
				textBlockStarted = false
			}

			// 发送 tool_use content_block_start
			blockStart := map[string]any{
				"type":  "content_block_start",
				"index": contentBlockIndex,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    toolUse.ToolUseId,
					"name":  toolUse.Name,
					"input": map[string]any{},
				},
			}
			data, _ := json.Marshal(blockStart)
			fmt.Fprintf(c.Writer, "event: content_block_start\ndata: %s\n\n", string(data))

			// 发送 input_json_delta
			inputJSON, _ := json.Marshal(toolUse.Input)
			inputDelta := map[string]any{
				"type":  "content_block_delta",
				"index": contentBlockIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": string(inputJSON),
				},
			}
			data, _ = json.Marshal(inputDelta)
			fmt.Fprintf(c.Writer, "event: content_block_delta\ndata: %s\n\n", string(data))

			// 发送 content_block_stop
			blockStop := map[string]any{
				"type":  "content_block_stop",
				"index": contentBlockIndex,
			}
			data, _ = json.Marshal(blockStop)
			fmt.Fprintf(c.Writer, "event: content_block_stop\ndata: %s\n\n", string(data))

			contentBlockIndex++
			flusher.Flush()
		}
	})

	if err != nil {
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 500, err.Error())
		// 记录流式响应（带工具）错误
		if logger != nil {
			logger.Error(GetMsgID(c), "流式响应(Tools)失败", map[string]any{
				"format":     format,
				"model":      model,
				"toolsCount": len(tools),
				"error":      err.Error(),
				"accountId":  accountID,
			})
		}
		fmt.Fprintf(c.Writer, "data: {\"error\": \"%s\"}\n\n", err.Error())
		flusher.Flush()
	} else {
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 200, "")

		// 使用 Kiro API 返回的精确 usage 值（如果有），否则降级使用本地估算
		inputTokens := estimatedInputTokens
		outputTokens := estimatedOutputTokens
		if usage != nil && usage.InputTokens > 0 {
			inputTokens = usage.InputTokens
			outputTokens = usage.OutputTokens
		}

		// 累加全局统计（使用精确值）
		addTokenStats(inputTokens, outputTokens)

		// 流式响应(Tools)完成日志已禁用（减少日志噪音）
	}
}

// handleNonStreamResponseWithTools 处理非流式响应（支持工具调用）
// 使用 ChatStreamWithToolsAndUsage 获取 Kiro API 返回的精确 token 使用量
func handleNonStreamResponseWithTools(c *gin.Context, messages []kiroclient.ChatMessage, tools []kiroclient.KiroToolWrapper, toolResults []kiroclient.KiroToolResult, format string, model string) {
	// 本地估算的 inputTokens（降级使用）
	estimatedInputTokens := kiroclient.CountMessagesTokens(messages)

	var responseText strings.Builder
	var toolUses []*kiroclient.KiroToolUse

	// 使用 ChatStreamWithToolsAndUsage 获取精确 usage
	usage, err := client.Chat.ChatStreamWithToolsAndUsage(messages, model, tools, toolResults, func(content string, toolUse *kiroclient.KiroToolUse, done bool) {
		if content != "" {
			responseText.WriteString(content)
		}
		if toolUse != nil {
			toolUses = append(toolUses, toolUse)
		}
	})

	if err != nil {
		accountID := client.Auth.GetLastSelectedAccountID()
		recordAccountRequest(accountID, 500, err.Error())
		if logger != nil {
			RecordError(c, logger, err, accountID)
			logger.Error(GetMsgID(c), "非流式响应(Tools)失败", map[string]any{
				"format":     format,
				"model":      model,
				"toolsCount": len(tools),
				"error":      err.Error(),
				"accountId":  accountID,
			})
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	accountID := client.Auth.GetLastSelectedAccountID()
	recordAccountRequest(accountID, 200, "")

	// 使用 Kiro API 返回的精确 usage 值（如果有），否则降级使用本地估算
	inputTokens := estimatedInputTokens
	outputTokens := kiroclient.CountTokens(responseText.String())
	if usage != nil && usage.InputTokens > 0 {
		inputTokens = usage.InputTokens
		outputTokens = usage.OutputTokens
	}

	// 非流式响应(Tools)完成日志已禁用（减少日志噪音）

	// 构建 content 数组
	var contentBlocks []map[string]any

	// 添加文本块
	if responseText.Len() > 0 {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": responseText.String(),
		})
	}

	// 添加工具调用块
	for _, tu := range toolUses {
		contentBlocks = append(contentBlocks, map[string]any{
			"type":  "tool_use",
			"id":    tu.ToolUseId,
			"name":  tu.Name,
			"input": tu.Input,
		})
	}

	// 确定 stop_reason
	stopReason := "end_turn"
	if len(toolUses) > 0 {
		stopReason = "tool_use"
	}

	resp := map[string]any{
		"id":          generateID("msg"),
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"stop_reason": stopReason,
		"content":     contentBlocks,
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}

	// 累加全局统计（使用精确值）
	addTokenStats(inputTokens, outputTokens)
	c.JSON(200, resp)
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
		if logger != nil {
			RecordError(c, logger, err, "")
		}
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
				if logger != nil {
					logger.Warn(GetMsgID(c), "账号获取额度失败", map[string]any{
						"accountId": acc.ID,
						"error":     err.Error(),
					})
				}
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
		if logger != nil {
			RecordError(c, logger, err, accountID)
		}
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{"message": "账号已删除"})
}

// handleRefreshAccount 刷新账号 Token
func handleRefreshAccount(c *gin.Context) {
	accountID := c.Param("id")

	if err := client.Auth.RefreshAccountToken(accountID); err != nil {
		if logger != nil {
			RecordError(c, logger, err, accountID)
		}
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
		if logger != nil {
			RecordError(c, logger, err, accountID)
		}
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
