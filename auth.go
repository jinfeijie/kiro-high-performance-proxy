package kiroclient

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuthManager Token 管理器
type AuthManager struct {
	cachedModels   []Model   // 缓存的模型列表
	modelsLoadedAt time.Time // 模型列表加载时间
	mu             sync.RWMutex
	httpClient     *http.Client

	// ========== 内存缓存层 ==========
	accountsCache  *AccountsConfig // 账号配置缓存
	accountsLoaded bool            // 是否已加载
	cacheMu        sync.RWMutex    // 缓存专用锁（与 mu 分离，避免死锁）
	fileMu         sync.Mutex      // 文件读写锁

	// ========== 熔断器层 ==========
	circuitBreakers map[string]*CircuitBreaker // 账号熔断器
	circuitConfig   CircuitBreakerConfig       // 熔断器配置
	circuitMu       sync.RWMutex               // 熔断器锁

	// ========== 负载均衡层 ==========
	usageCache      map[string]*AccountUsageCache // 账号额度缓存
	usageMu         sync.RWMutex                  // 额度缓存锁
	roundRobinIndex uint64                        // 轮询索引
	smoothWeights   map[string]int                // 平滑加权轮询的当前权重

	// ========== 保活相关 ==========
	keepAliveStop chan struct{}
	keepAliveWg   sync.WaitGroup

	// ========== 账号追踪 ==========
	lastSelectedAccountID string // 上一次选中的账号ID（用于统计）
}

// NewAuthManager 创建 AuthManager
func NewAuthManager() *AuthManager {
	return &AuthManager{
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		circuitBreakers: make(map[string]*CircuitBreaker),
		circuitConfig:   DefaultCircuitBreakerConfig,
		smoothWeights:   make(map[string]int),
		usageCache:      make(map[string]*AccountUsageCache),
	}
}

// ========== 内存缓存层 ==========

// InitAccountsCache 初始化账号缓存（启动时调用）
func (m *AuthManager) InitAccountsCache() error {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	if m.accountsLoaded {
		return nil
	}

	config, err := m.loadAccountsFromFile()
	if err != nil {
		// 文件不存在时创建空配置
		if os.IsNotExist(err) {
			m.accountsCache = &AccountsConfig{Accounts: []AccountInfo{}}
			m.accountsLoaded = true
			return nil
		}
		return fmt.Errorf("初始化账号缓存失败: %w", err)
	}

	m.accountsCache = config
	m.accountsLoaded = true
	// 线上环境已禁用调试日志

	// 异步初始化额度缓存（不阻塞启动）
	go m.refreshAllUsageCache()

	return nil
}

// refreshAllUsageCache 刷新所有账号的额度缓存
func (m *AuthManager) refreshAllUsageCache() {
	config := m.getAccountsFromCache()
	if config == nil || len(config.Accounts) == 0 {
		return
	}

	for i := range config.Accounts {
		acc := &config.Accounts[i]
		if acc.Token == nil || acc.Token.AccessToken == "" {
			continue
		}

		usage, err := m.GetUsageLimitsWithToken(acc.Token.AccessToken, acc.Token.Region, acc.ProfileArn)
		if err != nil {
			continue
		}

		// 提取 CREDIT 类型的额度
		for _, u := range usage.UsageBreakdownList {
			if u.ResourceType == "CREDIT" {
				m.updateUsageCache(acc.ID, u.CurrentUsageWithPrecision, u.UsageLimitWithPrecision)
				break
			}
		}
	}
}

// loadAccountsFromFile 从文件加载账号配置（纯读文件，加锁）
func (m *AuthManager) loadAccountsFromFile() (*AccountsConfig, error) {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	path := m.getAccountsConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config AccountsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析账号配置失败: %w", err)
	}

	return &config, nil
}

// saveAccountsToFile 保存账号配置到文件（纯写文件，加锁）
func (m *AuthManager) saveAccountsToFile(config *AccountsConfig) error {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	path := m.getAccountsConfigPath()

	// 确保目录存在
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化账号配置失败: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("保存账号配置失败: %w", err)
	}

	return nil
}

// getAccountsFromCache 从缓存获取账号配置（优先缓存）
func (m *AuthManager) getAccountsFromCache() *AccountsConfig {
	m.cacheMu.RLock()
	defer m.cacheMu.RUnlock()

	if m.accountsLoaded && m.accountsCache != nil {
		return m.accountsCache
	}
	return nil
}

// updateCache 更新内存缓存
func (m *AuthManager) updateCache(config *AccountsConfig) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	m.accountsCache = config
	m.accountsLoaded = true
}

// ========== 熔断器层 ==========

// getCircuitBreaker 获取账号的熔断器（不存在则创建）
func (m *AuthManager) getCircuitBreaker(accountID string) *CircuitBreaker {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		cb = &CircuitBreaker{State: CircuitClosed}
		m.circuitBreakers[accountID] = cb
	}
	return cb
}

// recordSuccess 记录请求成功
func (m *AuthManager) recordSuccess(accountID string) {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		return
	}

	switch cb.State {
	case CircuitHalfOpen:
		cb.SuccessCount++
		if cb.SuccessCount >= m.circuitConfig.HalfOpenMaxSuccess {
			// 半开状态下连续成功，关闭熔断器
			cb.State = CircuitClosed
			cb.FailureCount = 0
			cb.SuccessCount = 0
		}
	case CircuitClosed:
		// 正常状态下成功，重置失败计数
		cb.FailureCount = 0
	}
}

// recordFailure 记录请求失败
func (m *AuthManager) recordFailure(accountID string) {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		cb = &CircuitBreaker{State: CircuitClosed}
		m.circuitBreakers[accountID] = cb
	}

	now := time.Now()
	// 保存上次失败时间用于窗口判断(在更新前读取旧值)
	prevFailureTime := cb.LastFailureTime
	// 更新为当前时间
	cb.LastFailureTime = now

	switch cb.State {
	case CircuitClosed:
		// 检查上次失败是否在窗口内（用旧的 LastFailureTime 判断）
		// 如果超出窗口，说明是新一轮失败，重置计数
		if prevFailureTime.IsZero() || now.Sub(prevFailureTime) > m.circuitConfig.FailureWindow {
			cb.FailureCount = 1
		} else {
			cb.FailureCount++
		}

		// 达到阈值，触发熔断
		if cb.FailureCount >= m.circuitConfig.FailureThreshold {
			cb.State = CircuitOpen
			cb.OpenedAt = now
		}

	case CircuitHalfOpen:
		// 半开状态下失败，重新熔断
		cb.State = CircuitOpen
		cb.OpenedAt = now
		cb.SuccessCount = 0
	}
}

// isAccountAvailable 检查账号是否可用（未熔断）
func (m *AuthManager) isAccountAvailable(accountID string) bool {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		return true // 没有熔断器记录，视为可用
	}

	now := time.Now()

	switch cb.State {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// 检查是否可以进入半开状态
		if now.Sub(cb.OpenedAt) >= m.circuitConfig.OpenDuration {
			cb.State = CircuitHalfOpen
			cb.HalfOpenAt = now
			cb.SuccessCount = 0
			return true
		}
		return false

	case CircuitHalfOpen:
		return true
	}

	return true
}

// ========== 负载均衡层 ==========

// updateUsageCache 更新账号额度缓存
func (m *AuthManager) updateUsageCache(accountID string, used, total float64) {
	m.usageMu.Lock()
	defer m.usageMu.Unlock()

	m.usageCache[accountID] = &AccountUsageCache{
		UsedCredits:  used,
		TotalCredits: total,
		LastUpdated:  time.Now(),
		UpdateFailed: false,
	}
}

// getUsageCache 获取账号额度缓存
func (m *AuthManager) getUsageCache(accountID string) *AccountUsageCache {
	m.usageMu.RLock()
	defer m.usageMu.RUnlock()

	return m.usageCache[accountID]
}

// calculateWeight 计算账号权重（基于剩余额度）
// 返回 0-100 的权重值，剩余额度越多权重越高
func (m *AuthManager) calculateWeight(account *AccountInfo) int {
	cache := m.getUsageCache(account.ID)
	if cache == nil || cache.TotalCredits <= 0 {
		return 50 // 无额度信息，给默认权重
	}

	// 权重 = 剩余比例 * 100
	remainingRatio := 1 - cache.GetUsageRatio()
	weight := int(remainingRatio * 100)

	// 最小权重为 1（只要有额度就有机会被选中）
	if weight < 1 && cache.GetRemainingCredits() > 0 {
		weight = 1
	}

	return weight
}

// selectAccount 选择一个可用账号（平滑加权轮询）
// 使用 Nginx 的平滑加权轮询算法，既考虑权重又保证交替
// 返回选中的账号，如果没有可用账号返回 nil
func (m *AuthManager) selectAccount() (*AccountInfo, error) {
	config := m.getAccountsFromCache()
	if config == nil {
		// 缓存未初始化，尝试加载
		if err := m.InitAccountsCache(); err != nil {
			return nil, fmt.Errorf("加载账号缓存失败: %w", err)
		}
		config = m.getAccountsFromCache()
	}

	if config == nil || len(config.Accounts) == 0 {
		return nil, fmt.Errorf("没有可用账号")
	}

	// 构建可用账号列表（过滤掉过期、熔断、额度耗尽的账号）
	type weightedAccount struct {
		account *AccountInfo
		weight  int
	}

	var candidates []weightedAccount
	var totalWeight int

	for i := range config.Accounts {
		acc := &config.Accounts[i]

		// 跳过无 Token 的账号
		if acc.Token == nil {
			continue
		}

		// 跳过已过期的账号
		if acc.Token.IsExpired() {
			continue
		}

		// 跳过熔断中的账号
		if !m.isAccountAvailable(acc.ID) {
			continue
		}

		// 跳过额度耗尽的账号
		cache := m.getUsageCache(acc.ID)
		if cache != nil && cache.GetRemainingCredits() <= 0 {
			continue
		}

		weight := m.calculateWeight(acc)
		if weight > 0 {
			candidates = append(candidates, weightedAccount{account: acc, weight: weight})
			totalWeight += weight
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("没有可用账号（所有账号已过期、熔断或额度耗尽）")
	}

	// 只有一个候选，直接返回
	if len(candidates) == 1 {
		return candidates[0].account, nil
	}

	// ========== 平滑加权轮询算法 (Nginx SWRR) ==========
	// 1. 每个候选的 currentWeight += weight
	// 2. 选择 currentWeight 最大的候选
	// 3. 被选中的候选 currentWeight -= totalWeight
	m.usageMu.Lock()
	defer m.usageMu.Unlock()

	var selected *weightedAccount
	maxCurrent := -1 << 31 // 最小 int

	for i := range candidates {
		wa := &candidates[i]
		accID := wa.account.ID

		// 初始化或获取当前权重
		if _, exists := m.smoothWeights[accID]; !exists {
			m.smoothWeights[accID] = 0
		}

		// 步骤1: currentWeight += weight
		m.smoothWeights[accID] += wa.weight

		// 找最大 currentWeight
		if m.smoothWeights[accID] > maxCurrent {
			maxCurrent = m.smoothWeights[accID]
			selected = wa
		}
	}

	// 步骤3: 被选中的 currentWeight -= totalWeight
	if selected != nil {
		m.smoothWeights[selected.account.ID] -= totalWeight
		// 保存选中的账号ID（用于统计追踪）
		m.lastSelectedAccountID = selected.account.ID
	}

	return selected.account, nil
}

// GetAccessToken 获取有效的 Access Token（加权轮询选择账号）
func (m *AuthManager) GetAccessToken() (string, error) {
	// 多账号加权轮询
	account, err := m.selectAccount()
	if err != nil {
		return "", err
	}
	if account == nil || account.Token == nil {
		return "", fmt.Errorf("没有可用账号")
	}
	return account.Token.AccessToken, nil
}

// GetAccessTokenWithAccountID 获取指定账号的 Token（用于需要追踪账号的场景）
func (m *AuthManager) GetAccessTokenWithAccountID() (string, string, error) {
	account, err := m.selectAccount()
	if err != nil {
		return "", "", err
	}

	if account == nil || account.Token == nil {
		return "", "", fmt.Errorf("没有可用账号")
	}

	return account.Token.AccessToken, account.ID, nil
}

// GetCurrentAccountInfo 获取当前选中账号的信息（用于 debug header）
// 注意：此方法会递增轮询索引，应该在实际发送请求前调用一次
func (m *AuthManager) GetCurrentAccountInfo() (userId string, accountID string) {
	account, err := m.selectAccount()
	if err != nil || account == nil {
		return "", ""
	}
	// 提取 userId 后半截的 UUID
	userId = account.UserId
	if userId != "" {
		parts := strings.Split(userId, ".")
		if len(parts) == 2 {
			userId = parts[1]
		}
	}
	return userId, account.ID
}

// GetLastSelectedAccountID 获取上一次选中的账号ID（不递增计数器）
// 用于在请求完成后获取实际使用的账号信息
func (m *AuthManager) GetLastSelectedAccountID() string {
	m.usageMu.RLock()
	defer m.usageMu.RUnlock()
	return m.lastSelectedAccountID
}

// GetLastSelectedAccountInfo 获取上一次选中账号的 ID 和 Email
// 用于统计记录时同时写入 email，避免读取时再查询
func (m *AuthManager) GetLastSelectedAccountInfo() (accountID string, email string) {
	m.usageMu.RLock()
	accountID = m.lastSelectedAccountID
	m.usageMu.RUnlock()

	if accountID == "" {
		return "", ""
	}

	// 从缓存中查找对应账号的 email
	config := m.getAccountsFromCache()
	if config == nil {
		return accountID, ""
	}

	for _, acc := range config.Accounts {
		if acc.ID == accountID {
			return accountID, acc.Email
		}
	}

	return accountID, ""
}

// RecordRequestResult 记录请求结果（用于熔断器）
func (m *AuthManager) RecordRequestResult(accountID string, success bool) {
	if accountID == "" {
		return
	}

	if success {
		m.recordSuccess(accountID)
	} else {
		m.recordFailure(accountID)
	}
}

// GetRegion 获取区域
func (m *AuthManager) GetRegion() string {
	// 从多账号中获取 region，不再依赖旧的单 Token 文件
	account, err := m.selectAccount()
	if err != nil || account == nil || account.Token == nil {
		return "us-east-1"
	}
	if account.Token.Region == "" {
		return "us-east-1"
	}
	return account.Token.Region
}

// ListAvailableModels 调用 Kiro API 获取账号可用的模型列表
func (m *AuthManager) ListAvailableModels() ([]Model, error) {
	// 获取有效的 Access Token
	accessToken, err := m.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("获取 access token 失败: %w", err)
	}

	// 获取区域
	region := m.GetRegion()

	// 构建请求
	reqBody := ListAvailableModelsRequest{
		Origin: "AI_EDITOR",
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 构建 URL
	url := fmt.Sprintf("https://q.%s.amazonaws.com/ListAvailableModels?origin=AI_EDITOR", region)

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// 发送请求
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var apiResp ListAvailableModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return apiResp.Models, nil
}

// GetAvailableModels 获取当前账号可用的模型列表（带缓存）
// 优先从缓存读取，缓存过期（1小时）或为空时调用 API
func (m *AuthManager) GetAvailableModels() []Model {
	m.mu.RLock()
	// 检查缓存是否有效（1小时内）
	if len(m.cachedModels) > 0 && time.Since(m.modelsLoadedAt) < time.Hour {
		models := m.cachedModels
		m.mu.RUnlock()
		return models
	}
	m.mu.RUnlock()

	// 缓存失效，调用 API 获取
	models, err := m.ListAvailableModels()
	if err != nil {
		// API 调用失败，返回预定义的模型列表作为降级方案
		// 创建副本以避免返回 nil
		fallbackModels := make([]Model, len(AvailableModels))
		copy(fallbackModels, AvailableModels)
		return fallbackModels
	}

	// API 返回空列表时，也使用预定义列表
	if len(models) == 0 {
		fallbackModels := make([]Model, len(AvailableModels))
		copy(fallbackModels, AvailableModels)
		return fallbackModels
	}

	// 更新缓存
	m.mu.Lock()
	m.cachedModels = models
	m.modelsLoadedAt = time.Now()
	m.mu.Unlock()

	return models
}

// generateMachineID 生成机器 ID
func generateMachineID() string {
	hostname, _ := os.Hostname()
	h := sha256.New()
	h.Write([]byte(hostname))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ListAvailableProfiles 调用 AWS API 获取当前账号可用的 Profile 列表
// 返回第一个 Profile 的 ARN（通常每个账号只有一个 Profile）
func (m *AuthManager) ListAvailableProfiles(accessToken, region string) (string, error) {
	if region == "" {
		region = "us-east-1"
	}

	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/ListAvailableProfiles", region)

	req, err := http.NewRequest("POST", url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API 请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Profiles []struct {
			Arn  string `json:"arn"`
			Name string `json:"name"`
		} `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(result.Profiles) == 0 {
		return "", fmt.Errorf("没有可用的 Profile")
	}

	return result.Profiles[0].Arn, nil
}

// InitAllProfileArns 初始化所有账号的 profileArn
// 遍历所有账号，如果 profileArn 为空则调用 API 获取
func (m *AuthManager) InitAllProfileArns() (int, int, error) {
	config, err := m.LoadAccountsConfig()
	if err != nil {
		return 0, 0, fmt.Errorf("加载账号配置失败: %w", err)
	}

	if len(config.Accounts) == 0 {
		return 0, 0, nil
	}

	successCount := 0
	failCount := 0
	updated := false

	for i := range config.Accounts {
		acc := &config.Accounts[i]

		// 跳过已有 profileArn 的账号
		if acc.ProfileArn != "" {
			continue
		}

		// 跳过无 Token 的账号
		if acc.Token == nil || acc.Token.AccessToken == "" {
			failCount++
			continue
		}

		// 获取 profileArn
		region := acc.Token.Region
		if region == "" {
			region = "us-east-1"
		}

		profileArn, err := m.ListAvailableProfiles(acc.Token.AccessToken, region)
		if err != nil {
			failCount++
			continue
		}

		acc.ProfileArn = profileArn
		updated = true
		successCount++
	}

	// 保存更新后的配置
	if updated {
		if err := m.SaveAccountsConfig(config); err != nil {
			return successCount, failCount, fmt.Errorf("保存配置失败: %w", err)
		}
	}

	return successCount, failCount, nil
}

// GetProfileArn 获取 Profile ARN
// 优先从账号配置读取（服务器部署），降级到本地 Kiro IDE 文件（本地开发）
func (m *AuthManager) GetProfileArn() (string, error) {
	// 优先从账号配置读取（服务器部署场景）
	config := m.getAccountsFromCache()
	if config != nil && len(config.Accounts) > 0 {
		for _, acc := range config.Accounts {
			if acc.ProfileArn != "" {
				return acc.ProfileArn, nil
			}
		}
	}

	// 降级：从本地 Kiro IDE 的 profile.json 读取（本地开发场景）
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	// macOS 路径
	profilePath := filepath.Join(homeDir, "Library", "Application Support", "Kiro", "User", "globalStorage", "kiro.kiroagent", "profile.json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return "", fmt.Errorf("读取 profile 文件失败: %w", err)
	}

	var profile struct {
		Arn string `json:"arn"`
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		return "", fmt.Errorf("解析 profile 失败: %w", err)
	}

	return profile.Arn, nil
}

// GetUsageLimits 获取额度使用情况
// 注意：此功能依赖 profileArn，服务器部署时可能无法获取，会返回 nil
func (m *AuthManager) GetUsageLimits() (*UsageLimitsResponse, error) {
	// 获取有效的 Access Token
	accessToken, err := m.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("获取 access token 失败: %w", err)
	}

	// 获取 Profile ARN（服务器部署时可能失败，优雅降级）
	profileArn, err := m.GetProfileArn()
	if err != nil || profileArn == "" {
		// profileArn 不可用，返回空响应而不是错误
		return nil, nil
	}

	// 获取区域
	region := m.GetRegion()

	// 构建 URL（带查询参数）
	// isEmailRequired=true 让 API 返回用户邮箱
	url := fmt.Sprintf(
		"https://q.%s.amazonaws.com/getUsageLimits?profileArn=%s&origin=AI_EDITOR&isEmailRequired=true",
		region,
		profileArn,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	// 发送请求
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var usageResp UsageLimitsResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return &usageResp, nil
}

// GetUsageLimitsWithToken 使用指定 Token 和 profileArn 获取额度（用于多账号场景）
func (m *AuthManager) GetUsageLimitsWithToken(accessToken, region, profileArn string) (*UsageLimitsResponse, error) {
	if region == "" {
		region = "us-east-1"
	}

	// profileArn 为空时，尝试从本地文件获取（兼容本地开发）
	if profileArn == "" {
		profileArn, _ = m.GetProfileArn()
	}
	if profileArn == "" {
		return nil, fmt.Errorf("profileArn 不可用")
	}

	// isEmailRequired=true 让 API 返回用户邮箱
	url := fmt.Sprintf(
		"https://q.%s.amazonaws.com/getUsageLimits?profileArn=%s&origin=AI_EDITOR&isEmailRequired=true",
		region, profileArn,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var usageResp UsageLimitsResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, err
	}
	return &usageResp, nil
}

// ========== AWS SSO OIDC 登录流程 ==========

// 多账号配置文件路径（保存到项目根目录）
func (m *AuthManager) getAccountsConfigPath() string {
	return "./kiro-accounts.json"
}

// LoadAccountsConfigFromFile 强制从文件加载账号配置（绕过缓存）
// 用于新增/删除账号等需要最新数据的场景，避免缓存导致数据丢失
func (m *AuthManager) LoadAccountsConfigFromFile() (*AccountsConfig, error) {
	config, err := m.loadAccountsFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			return &AccountsConfig{Accounts: []AccountInfo{}}, nil
		}
		return nil, fmt.Errorf("读取账号配置失败: %w", err)
	}
	// 更新缓存
	m.updateCache(config)
	return config, nil
}

// LoadAccountsConfig 加载多账号配置（优先从缓存读取）
// 如果配置文件不存在但有 Token，自动将当前 Token 作为第一个账号
func (m *AuthManager) LoadAccountsConfig() (*AccountsConfig, error) {
	// 优先从缓存读取
	if cached := m.getAccountsFromCache(); cached != nil {
		return cached, nil
	}

	// 缓存未命中，从文件加载
	config, err := m.loadAccountsFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			config = &AccountsConfig{Accounts: []AccountInfo{}}
			m.updateCache(config)
			return config, nil
		}
		return nil, fmt.Errorf("读取账号配置失败: %w", err)
	}

	// 更新缓存
	m.updateCache(config)

	return config, nil
}

// SaveAccountsConfig 保存多账号配置（同时更新缓存和文件）
func (m *AuthManager) SaveAccountsConfig(config *AccountsConfig) error {
	// 先更新缓存
	m.updateCache(config)

	// 再写入文件
	if err := m.saveAccountsToFile(config); err != nil {
		return err
	}

	return nil
}

// RegisterClient 注册 OIDC 客户端
// 这是 AWS SSO OIDC 登录流程的第一步
func (m *AuthManager) RegisterClient(region string) (*RegisterClientResponse, error) {
	if region == "" {
		region = "us-east-1"
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/client/register", region)

	reqBody := map[string]interface{}{
		"clientName": "Kiro IDE",
		"clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("注册客户端失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var result RegisterClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return &result, nil
}

// StartDeviceAuthorization 开始设备授权流程
// 这是 AWS SSO OIDC 登录流程的第二步
// startUrl: Builder ID 使用 "https://view.awsapps.com/start"，企业 SSO 使用自定义 URL
func (m *AuthManager) StartDeviceAuthorization(clientID, clientSecret, region, startUrl string) (*StartDeviceAuthResponse, error) {
	if region == "" {
		region = "us-east-1"
	}
	if startUrl == "" {
		startUrl = "https://view.awsapps.com/start"
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/device_authorization", region)

	reqBody := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     startUrl,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("设备授权失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var result StartDeviceAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	return &result, nil
}

// PollForToken 轮询获取 Token
// 这是 AWS SSO OIDC 登录流程的第三步
// 返回值: token, 是否继续轮询, 错误
func (m *AuthManager) PollForToken(clientID, clientSecret, deviceCode, region string) (*CreateTokenResponse, bool, error) {
	if region == "" {
		region = "us-east-1"
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	reqBody := map[string]interface{}{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
		"deviceCode":   deviceCode,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, false, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	// 检查是否需要继续轮询
	if resp.StatusCode == http.StatusBadRequest {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(bodyBytes, &errResp) == nil {
			// authorization_pending 表示用户还未完成授权，继续轮询
			if errResp.Error == "authorization_pending" {
				return nil, true, nil
			}
			// slow_down 表示轮询太快，继续轮询但需要降速
			if errResp.Error == "slow_down" {
				return nil, true, nil
			}
			// expired_token 表示设备码已过期
			if errResp.Error == "expired_token" {
				return nil, false, fmt.Errorf("设备码已过期，请重新登录")
			}
			// access_denied 表示用户拒绝授权
			if errResp.Error == "access_denied" {
				return nil, false, fmt.Errorf("用户拒绝授权")
			}
		}
		return nil, false, fmt.Errorf("获取 Token 失败: %s", string(bodyBytes))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("获取 Token 失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, false, fmt.Errorf("解析响应失败: %w", err)
	}

	return &result, false, nil
}

// StartLogin 开始登录流程
// 返回登录会话信息，包含验证 URL 供用户访问
// startUrl: 空字符串表示 Builder ID，否则为企业 SSO URL
func (m *AuthManager) StartLogin(region, startUrl string) (*LoginSession, error) {
	if region == "" {
		region = "us-east-1"
	}

	// 确定认证类型
	authType := "BuilderId"
	if startUrl == "" {
		startUrl = "https://view.awsapps.com/start"
	} else {
		authType = "Enterprise"
	}

	// 第一步：注册客户端
	clientResp, err := m.RegisterClient(region)
	if err != nil {
		return nil, fmt.Errorf("注册客户端失败: %w", err)
	}

	// 第二步：开始设备授权
	deviceResp, err := m.StartDeviceAuthorization(clientResp.ClientID, clientResp.ClientSecret, region, startUrl)
	if err != nil {
		return nil, fmt.Errorf("设备授权失败: %w", err)
	}

	// 生成会话 ID（使用设备码的 hash）
	h := sha256.New()
	h.Write([]byte(deviceResp.DeviceCode))
	sessionID := hex.EncodeToString(h.Sum(nil))[:16]

	session := &LoginSession{
		SessionID:    sessionID,
		DeviceCode:   deviceResp.DeviceCode,
		UserCode:     deviceResp.UserCode,
		ClientID:     clientResp.ClientID,
		ClientSecret: clientResp.ClientSecret,
		VerifyURL:    deviceResp.VerificationURIComplete,
		ExpiresAt:    time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second).Unix(),
		Interval:     deviceResp.Interval,
		Region:       region,
		StartUrl:     startUrl,
		AuthType:     authType,
	}

	return session, nil
}

// CompleteLogin 完成登录流程（轮询获取 Token 并保存账号）
func (m *AuthManager) CompleteLogin(session *LoginSession) (*AccountInfo, error) {
	// 轮询获取 Token
	tokenResp, shouldContinue, err := m.PollForToken(
		session.ClientID,
		session.ClientSecret,
		session.DeviceCode,
		session.Region,
	)

	if err != nil {
		return nil, err
	}

	if shouldContinue {
		return nil, nil // 返回 nil 表示需要继续轮询
	}

	// 构建 Token 对象
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	// 计算 clientIdHash（与 Kiro IDE 保持一致）
	// IDE 使用: crypto.createHash("sha1").update(JSON.stringify({ startUrl })).digest("hex")
	// 注意：startUrl 末尾不能有斜杠
	startUrlForHash := strings.TrimSuffix(session.StartUrl, "/")
	h := sha1.New()
	h.Write([]byte(fmt.Sprintf(`{"startUrl":"%s"}`, startUrlForHash)))
	clientIDHash := hex.EncodeToString(h.Sum(nil))

	token := &KiroAuthToken{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		ClientIDHash: clientIDHash,
		AuthMethod:   session.AuthType,
		Provider:     session.AuthType,
		Region:       session.Region,
	}

	// 生成账号 ID
	accountID := session.SessionID

	// 创建账号信息（clientID/clientSecret 直接存在 AccountInfo 中，不再写 sso cache）
	account := &AccountInfo{
		ID:           accountID,
		DeviceCode:   session.DeviceCode,
		UserCode:     session.UserCode,
		Token:        token,
		ClientID:     session.ClientID,
		ClientSecret: session.ClientSecret,
		CreatedAt:    time.Now().Format(time.RFC3339),
		LastUsedAt:   time.Now().Format(time.RFC3339),
	}

	// 获取 profileArn（登录后自动获取）
	profileArn, err := m.ListAvailableProfiles(token.AccessToken, session.Region)
	if err == nil {
		account.ProfileArn = profileArn
	}

	// 获取用户信息（userId）- 需要 profileArn
	if account.ProfileArn != "" {
		usage, err := m.GetUsageLimitsWithToken(token.AccessToken, session.Region, account.ProfileArn)
		if err == nil && usage != nil {
			account.UserId = usage.UserInfo.UserId
			account.Email = usage.UserInfo.Email
		}
	}

	// 加载现有账号配置（强制从文件读取，避免缓存导致数据丢失）
	config, err := m.LoadAccountsConfigFromFile()
	if err != nil {
		config = &AccountsConfig{Accounts: []AccountInfo{}}
	}

	// 添加新账号
	config.Accounts = append(config.Accounts, *account)

	// 保存账号配置
	if err := m.SaveAccountsConfig(config); err != nil {
		return nil, fmt.Errorf("保存账号配置失败: %w", err)
	}

	return account, nil
}

// DeleteAccount 删除账号
func (m *AuthManager) DeleteAccount(accountID string) error {
	// 强制从文件读取，避免缓存导致数据丢失
	config, err := m.LoadAccountsConfigFromFile()
	if err != nil {
		return fmt.Errorf("加载账号配置失败: %w", err)
	}

	// 查找并删除账号
	newAccounts := make([]AccountInfo, 0)
	for _, acc := range config.Accounts {
		if acc.ID != accountID {
			newAccounts = append(newAccounts, acc)
		}
	}

	if len(newAccounts) == len(config.Accounts) {
		return fmt.Errorf("账号不存在: %s", accountID)
	}

	config.Accounts = newAccounts

	return m.SaveAccountsConfig(config)
}

// SwitchAccount 切换当前账号（将指定账号的 Token 设置为当前使用的 Token）
func (m *AuthManager) SwitchAccount(accountID string) error {
	config, err := m.LoadAccountsConfig()
	if err != nil {
		return fmt.Errorf("加载账号配置失败: %w", err)
	}

	// 查找账号
	var targetAccount *AccountInfo
	var targetIndex int
	for i := range config.Accounts {
		if config.Accounts[i].ID == accountID {
			targetAccount = &config.Accounts[i]
			targetIndex = i
			break
		}
	}

	if targetAccount == nil {
		return fmt.Errorf("账号不存在: %s", accountID)
	}

	// 更新最后使用时间
	targetAccount.LastUsedAt = time.Now().Format(time.RFC3339)
	config.Accounts[targetIndex] = *targetAccount

	// 保存配置
	if err := m.SaveAccountsConfig(config); err != nil {
		return fmt.Errorf("保存账号配置失败: %w", err)
	}

	return nil
}

// RefreshAccountToken 刷新指定账号的 Token
func (m *AuthManager) RefreshAccountToken(accountID string) error {
	config, err := m.LoadAccountsConfig()
	if err != nil {
		return fmt.Errorf("加载账号配置失败: %w", err)
	}

	// 查找账号
	var targetAccount *AccountInfo
	var targetIndex int
	for i := range config.Accounts {
		if config.Accounts[i].ID == accountID {
			targetAccount = &config.Accounts[i]
			targetIndex = i
			break
		}
	}

	if targetAccount == nil {
		return fmt.Errorf("账号不存在: %s", accountID)
	}

	if targetAccount.Token == nil {
		return fmt.Errorf("账号 Token 为空")
	}

	// 使用账号自己的 ClientID 和 ClientSecret 刷新 Token
	region := targetAccount.Token.Region
	if region == "" {
		region = "us-east-1"
	}

	// 检查 ClientID 和 ClientSecret 是否存在（导入的账号可能缺失）
	if targetAccount.ClientID == "" || targetAccount.ClientSecret == "" {
		return fmt.Errorf("账号 %s 缺少 ClientID/ClientSecret，无法刷新", accountID)
	}

	// 检查 RefreshToken 是否存在
	if targetAccount.Token.RefreshToken == "" {
		return fmt.Errorf("账号 %s 缺少 RefreshToken，无法刷新", accountID)
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	reqBody := TokenRefreshRequest{
		ClientID:     targetAccount.ClientID,
		ClientSecret: targetAccount.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: targetAccount.Token.RefreshToken,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("刷新 Token 失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var refreshResp TokenRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	// 更新 Token
	expiresAt := time.Now().Add(time.Duration(refreshResp.ExpiresIn) * time.Second)
	targetAccount.Token.AccessToken = refreshResp.AccessToken
	targetAccount.Token.RefreshToken = refreshResp.RefreshToken
	targetAccount.Token.ExpiresAt = expiresAt.Format(time.RFC3339)
	targetAccount.LastUsedAt = time.Now().Format(time.RFC3339)

	// 更新配置
	config.Accounts[targetIndex] = *targetAccount

	// 保存配置（同时更新缓存和文件）
	if err := m.SaveAccountsConfig(config); err != nil {
		return fmt.Errorf("保存账号配置失败: %w", err)
	}

	// 刷新成功后，尝试更新该账号的额度缓存
	go func(accID, accessToken, region, profileArn string) {
		usage, err := m.GetUsageLimitsWithToken(accessToken, region, profileArn)
		if err != nil {
			return
		}
		for _, item := range usage.UsageBreakdownList {
			if item.ResourceType == "CREDIT" {
				m.updateUsageCache(accID, item.CurrentUsageWithPrecision, item.UsageLimitWithPrecision)
				break
			}
		}
	}(accountID, refreshResp.AccessToken, region, targetAccount.ProfileArn)

	return nil
}

// ========== 保活机制 ==========

// StartKeepAlive 启动后台保活 goroutine
// 每 5 分钟检查一次所有账号，对即将过期的 Token 进行刷新
func (m *AuthManager) StartKeepAlive() {
	m.mu.Lock()
	if m.keepAliveStop != nil {
		m.mu.Unlock()
		return // 已经在运行
	}
	m.keepAliveStop = make(chan struct{})
	m.mu.Unlock()

	m.keepAliveWg.Add(1)
	go func() {
		defer m.keepAliveWg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// 启动时立即执行一次
		m.RefreshAllAccounts()

		for {
			select {
			case <-ticker.C:
				m.RefreshAllAccounts()
			case <-m.keepAliveStop:
				return
			}
		}
	}()
}

// StopKeepAlive 停止保活 goroutine
func (m *AuthManager) StopKeepAlive() {
	m.mu.Lock()
	if m.keepAliveStop == nil {
		m.mu.Unlock()
		return
	}
	close(m.keepAliveStop)
	m.keepAliveStop = nil
	m.mu.Unlock()

	m.keepAliveWg.Wait()
}

// RefreshAllAccounts 刷新所有账号的 Token
// 只刷新即将过期（60分钟内）的 Token，同时更新额度缓存
func (m *AuthManager) RefreshAllAccounts() {
	config, err := m.LoadAccountsConfig()
	if err != nil {
		return
	}

	if len(config.Accounts) == 0 {
		return
	}

	// 遍历所有账号，检查并刷新即将过期的 Token
	for _, acc := range config.Accounts {
		if acc.Token == nil {
			continue
		}

		// 检查是否即将过期（60分钟内）
		if !m.isTokenExpiringSoon(acc.Token, 60*time.Minute) {
			// Token 未过期，但仍然更新额度缓存（异步）
			go func(accID, accessToken, region, profileArn string) {
				usage, err := m.GetUsageLimitsWithToken(accessToken, region, profileArn)
				if err != nil {
					return
				}
				for _, item := range usage.UsageBreakdownList {
					if item.ResourceType == "CREDIT" {
						m.updateUsageCache(accID, item.CurrentUsageWithPrecision, item.UsageLimitWithPrecision)
						break
					}
				}
			}(acc.ID, acc.Token.AccessToken, acc.Token.Region, acc.ProfileArn)
			continue
		}

		// 使用该账号自己的 ClientID/ClientSecret 刷新
		// 失败时重试一次（网络抖动等临时问题）
		if err := m.RefreshAccountToken(acc.ID); err != nil {
			// 等待 3 秒后重试一次
			time.Sleep(3 * time.Second)
			_ = m.RefreshAccountToken(acc.ID)
		}
	}
}

// isTokenExpiringSoon 检查 Token 是否即将过期
func (m *AuthManager) isTokenExpiringSoon(token *KiroAuthToken, threshold time.Duration) bool {
	if token == nil || token.ExpiresAt == "" {
		return true
	}

	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		return true
	}

	return time.Until(expiresAt) < threshold
}

// ImportAccount 导入账号（支持企业 SSO Token）
// tokenJSON: Token JSON 字符串（必需）
// clientRegJSON: ClientRegistration JSON 字符串（企业 SSO 必需，个人账号可选）
func (m *AuthManager) ImportAccount(tokenJSON, clientRegJSON string) (*AccountInfo, error) {
	// 解析 Token
	var token KiroAuthToken
	if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
		return nil, fmt.Errorf("解析 Token 失败: %w", err)
	}

	// 验证必要字段
	if token.AccessToken == "" {
		return nil, fmt.Errorf("Token 缺少 accessToken 字段")
	}

	// 解析 ClientRegistration（可选）
	var clientID, clientSecret string
	if clientRegJSON != "" {
		var clientReg ClientRegistration
		if err := json.Unmarshal([]byte(clientRegJSON), &clientReg); err != nil {
			return nil, fmt.Errorf("解析 ClientRegistration 失败: %w", err)
		}
		clientID = clientReg.ClientID
		clientSecret = clientReg.ClientSecret
	}

	// 生成账号 ID（使用 clientIdHash 或 accessToken 的 hash）
	var accountID string
	if token.ClientIDHash != "" {
		h := sha256.New()
		h.Write([]byte(token.ClientIDHash + time.Now().String()))
		accountID = hex.EncodeToString(h.Sum(nil))[:16]
	} else {
		h := sha256.New()
		h.Write([]byte(token.AccessToken[:32] + time.Now().String()))
		accountID = hex.EncodeToString(h.Sum(nil))[:16]
	}

	// 创建账号信息（clientID/clientSecret 直接存在 AccountInfo 中，不再写 sso cache）
	account := &AccountInfo{
		ID:           accountID,
		Token:        &token,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		CreatedAt:    time.Now().Format(time.RFC3339),
		LastUsedAt:   time.Now().Format(time.RFC3339),
	}

	// 获取 profileArn（导入时自动获取）
	region := token.Region
	if region == "" {
		region = "us-east-1"
	}
	profileArn, err := m.ListAvailableProfiles(token.AccessToken, region)
	if err == nil {
		account.ProfileArn = profileArn
	}

	// 尝试获取用户信息（需要 profileArn）
	if account.ProfileArn != "" {
		usage, err := m.GetUsageLimitsWithToken(token.AccessToken, region, account.ProfileArn)
		if err == nil && usage != nil {
			account.UserId = usage.UserInfo.UserId
			account.Email = usage.UserInfo.Email
		}
	}

	// 加载现有账号配置（强制从文件读取，避免缓存导致数据丢失）
	config, err := m.LoadAccountsConfigFromFile()
	if err != nil {
		config = &AccountsConfig{Accounts: []AccountInfo{}}
	}

	// 添加新账号
	config.Accounts = append(config.Accounts, *account)

	// 保存账号配置
	if err := m.SaveAccountsConfig(config); err != nil {
		return nil, fmt.Errorf("保存账号配置失败: %w", err)
	}

	return account, nil
}

// GetAccountsStatus 获取所有账号的状态（用于前端显示）
func (m *AuthManager) GetAccountsStatus() ([]map[string]interface{}, error) {
	config, err := m.LoadAccountsConfig()
	if err != nil {
		return nil, err
	}

	result := make([]map[string]interface{}, 0, len(config.Accounts))
	for _, acc := range config.Accounts {
		status := map[string]interface{}{
			"id":         acc.ID,
			"createdAt":  acc.CreatedAt,
			"lastUsedAt": acc.LastUsedAt,
		}

		if acc.Token != nil {
			status["region"] = acc.Token.Region
			status["expiresAt"] = acc.Token.ExpiresAt
			status["isExpired"] = acc.Token.IsExpired()
			status["expiringSoon"] = m.isTokenExpiringSoon(acc.Token, 30*time.Minute)
		}

		result = append(result, status)
	}

	return result, nil
}

// ========== 熔断管理面板方法 ==========

// IsAccountAvailable 检查账号是否可用（导出版，供 server 包调用）
func (m *AuthManager) IsAccountAvailable(accountID string) bool {
	return m.isAccountAvailable(accountID)
}

// IsAccountHalfOpen 检查账号是否处于半开状态（供 server 包判断是否跳过错误率检查）
func (m *AuthManager) IsAccountHalfOpen(accountID string) bool {
	m.circuitMu.RLock()
	defer m.circuitMu.RUnlock()
	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		return false
	}
	return cb.State == CircuitHalfOpen
}

// GetCircuitConfig 获取熔断器配置（供 server 包读取阈值）
func (m *AuthManager) GetCircuitConfig() CircuitBreakerConfig {
	return m.circuitConfig
}

// TryAutoTrip 尝试自动熔断(原子操作,消除TOCTOU竞态)
// 在持有锁的情况下检查状态并触发熔断,避免竞态条件
// 返回: 是否触发了熔断
func (m *AuthManager) TryAutoTrip(accountID string, errorRate float64, totalReqs int64) bool {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		cb = &CircuitBreaker{State: CircuitClosed}
		m.circuitBreakers[accountID] = cb
	}

	// 只在Closed状态检查(HalfOpen状态正在试探恢复,不能用旧错误率数据打回去)
	if cb.State != CircuitClosed {
		return false
	}

	// 检查阈值
	if totalReqs >= m.circuitConfig.ErrorRateMinReqs &&
		errorRate >= m.circuitConfig.ErrorRateThreshold {
		cb.State = CircuitOpen
		cb.OpenedAt = time.Now()
		return true
	}

	return false
}

// ManualTrip 手动熔断指定账号
// 将熔断器状态设为 Open，刷新 OpenedAt
// 如果账号不存在于熔断器 map 中，先检查账号配置是否存在
func (m *AuthManager) ManualTrip(accountID string) error {
	// 先检查账号是否存在于配置中
	if !m.accountExists(accountID) {
		return fmt.Errorf("账号不存在: %s", accountID)
	}

	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		// 账号存在但没有熔断器记录，创建一个
		cb = &CircuitBreaker{}
		m.circuitBreakers[accountID] = cb
	}

	// 无论当前什么状态，都设为 Open 并刷新时间
	cb.State = CircuitOpen
	cb.OpenedAt = time.Now()
	return nil
}

// ManualReset 手动解除熔断
// 将熔断器状态设为 Closed，重置 FailureCount 和 SuccessCount 为 0
func (m *AuthManager) ManualReset(accountID string) error {
	// 先检查账号是否存在于配置中
	if !m.accountExists(accountID) {
		return fmt.Errorf("账号不存在: %s", accountID)
	}

	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	cb, exists := m.circuitBreakers[accountID]
	if !exists {
		// 账号存在但没有熔断器记录，已经是 Closed 状态，幂等返回
		return nil
	}

	cb.State = CircuitClosed
	cb.FailureCount = 0
	cb.SuccessCount = 0
	return nil
}

// GetCircuitBreakerStates 获取所有账号的熔断器状态副本
// 返回深拷贝，避免外部修改导致竞态
func (m *AuthManager) GetCircuitBreakerStates() map[string]CircuitBreaker {
	m.circuitMu.RLock()
	defer m.circuitMu.RUnlock()

	result := make(map[string]CircuitBreaker, len(m.circuitBreakers))
	for id, cb := range m.circuitBreakers {
		// 值拷贝，断开指针引用
		result[id] = CircuitBreaker{
			State:           cb.State,
			FailureCount:    cb.FailureCount,
			SuccessCount:    cb.SuccessCount,
			LastFailureTime: cb.LastFailureTime,
			OpenedAt:        cb.OpenedAt,
			HalfOpenAt:      cb.HalfOpenAt,
		}
	}
	return result
}

// GetLoadDistribution 获取所有账号的负载分布（权重和占比）
// 遍历所有账号，计算每个账号的权重，然后算出百分比
func (m *AuthManager) GetLoadDistribution() []AccountLoadInfo {
	config := m.getAccountsFromCache()
	if config == nil || len(config.Accounts) == 0 {
		return nil
	}

	// 第一遍：收集所有账号的权重
	type entry struct {
		id     string
		email  string
		weight int
	}
	entries := make([]entry, 0, len(config.Accounts))
	totalWeight := 0

	for i := range config.Accounts {
		acc := &config.Accounts[i]
		w := m.calculateWeight(acc)
		// 熔断中的账号权重归零，与 selectAccount 的过滤逻辑保持一致
		if !m.isAccountAvailable(acc.ID) {
			w = 0
		}
		entries = append(entries, entry{
			id:     acc.ID,
			email:  acc.Email,
			weight: w,
		})
		totalWeight += w
	}

	// 第二遍：计算百分比
	result := make([]AccountLoadInfo, len(entries))
	for i, e := range entries {
		var pct float64
		if totalWeight > 0 {
			pct = float64(e.weight) / float64(totalWeight) * 100
		}
		result[i] = AccountLoadInfo{
			AccountID: e.id,
			Email:     e.email,
			Weight:    e.weight,
			Percent:   pct,
		}
	}

	return result
}

// accountExists 检查账号是否存在于配置中（内部辅助方法）
func (m *AuthManager) accountExists(accountID string) bool {
	config := m.getAccountsFromCache()
	if config == nil {
		return false
	}
	for _, acc := range config.Accounts {
		if acc.ID == accountID {
			return true
		}
	}
	return false
}
