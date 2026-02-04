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
	tokenPath      string
	clientRegPath  string
	token          *KiroAuthToken
	clientReg      *ClientRegistration
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
}

// NewAuthManager 创建 AuthManager
func NewAuthManager() *AuthManager {
	homeDir, _ := os.UserHomeDir()
	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")

	return &AuthManager{
		tokenPath:       filepath.Join(cacheDir, "kiro-auth-token.json"),
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
	fmt.Printf("[缓存] 已加载 %d 个账号到内存\n", len(config.Accounts))

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

		usage, err := m.GetUsageLimitsWithToken(acc.Token.AccessToken, acc.Token.Region)
		if err != nil {
			fmt.Printf("[额度缓存] 账号 %s 获取失败: %v\n", acc.ID[:8], err)
			continue
		}

		// 提取 CREDIT 类型的额度
		for _, u := range usage.UsageBreakdownList {
			if u.ResourceType == "CREDIT" {
				m.updateUsageCache(acc.ID, u.CurrentUsageWithPrecision, u.UsageLimitWithPrecision)
				fmt.Printf("[额度缓存] 账号 %s: %.2f/%.2f\n", acc.ID[:8], u.CurrentUsageWithPrecision, u.UsageLimitWithPrecision)
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
			fmt.Printf("[熔断器] 账号 %s 恢复正常\n", accountID)
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
	cb.LastFailureTime = now

	switch cb.State {
	case CircuitClosed:
		// 检查是否在失败窗口内
		if time.Since(cb.LastFailureTime) > m.circuitConfig.FailureWindow {
			cb.FailureCount = 1
		} else {
			cb.FailureCount++
		}

		// 达到阈值，触发熔断
		if cb.FailureCount >= m.circuitConfig.FailureThreshold {
			cb.State = CircuitOpen
			cb.OpenedAt = now
			fmt.Printf("[熔断器] 账号 %s 触发熔断（失败 %d 次）\n", accountID, cb.FailureCount)
		}

	case CircuitHalfOpen:
		// 半开状态下失败，重新熔断
		cb.State = CircuitOpen
		cb.OpenedAt = now
		cb.SuccessCount = 0
		fmt.Printf("[熔断器] 账号 %s 半开状态失败，重新熔断\n", accountID)
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
			fmt.Printf("[熔断器] 账号 %s 进入半开状态\n", accountID)
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
		fmt.Printf("[轮询] 仅1个可用账号: %s\n", candidates[0].account.ID[:8])
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
	}

	// 调试日志
	fmt.Printf("[轮询] 平滑加权: ")
	for _, wa := range candidates {
		fmt.Printf("%s(w=%d,c=%d) ", wa.account.ID[:8], wa.weight, m.smoothWeights[wa.account.ID])
	}
	fmt.Printf("-> 选中: %s\n", selected.account.ID[:8])

	return selected.account, nil
}

// ReadToken 读取 Token
func (m *AuthManager) ReadToken() (*KiroAuthToken, error) {
	m.mu.RLock()
	if m.token != nil {
		defer m.mu.RUnlock()
		return m.token, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("读取 token 文件失败: %w", err)
	}

	var token KiroAuthToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("解析 token 失败: %w", err)
	}

	m.token = &token

	// 设置 clientRegPath
	if token.ClientIDHash != "" {
		homeDir, _ := os.UserHomeDir()
		cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
		m.clientRegPath = filepath.Join(cacheDir, token.ClientIDHash+".json")
	}

	return &token, nil
}

// ReadClientRegistration 读取客户端注册信息
func (m *AuthManager) ReadClientRegistration() (*ClientRegistration, error) {
	m.mu.RLock()
	if m.clientReg != nil {
		defer m.mu.RUnlock()
		return m.clientReg, nil
	}
	m.mu.RUnlock()

	if m.clientRegPath == "" {
		token, err := m.ReadToken()
		if err != nil {
			return nil, err
		}
		homeDir, _ := os.UserHomeDir()
		cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
		m.clientRegPath = filepath.Join(cacheDir, token.ClientIDHash+".json")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.clientRegPath)
	if err != nil {
		return nil, fmt.Errorf("读取客户端注册文件失败: %w", err)
	}

	var reg ClientRegistration
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("解析客户端注册信息失败: %w", err)
	}

	m.clientReg = &reg
	return &reg, nil
}

// RefreshToken 刷新 Token
func (m *AuthManager) RefreshToken() error {
	token, err := m.ReadToken()
	if err != nil {
		return err
	}

	clientReg, err := m.ReadClientRegistration()
	if err != nil {
		return err
	}

	// 构建刷新请求
	reqBody := TokenRefreshRequest{
		ClientID:     clientReg.ClientID,
		ClientSecret: clientReg.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: token.RefreshToken,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	// 确定 OIDC endpoint
	region := token.Region
	if region == "" {
		region = "us-east-1"
	}
	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

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
		return fmt.Errorf("刷新 token 失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var refreshResp TokenRefreshResponse
	if err := json.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	// 更新 token
	m.mu.Lock()
	token.AccessToken = refreshResp.AccessToken
	token.RefreshToken = refreshResp.RefreshToken
	expiresAt := time.Now().Add(time.Duration(refreshResp.ExpiresIn) * time.Second)
	token.ExpiresAt = expiresAt.Format(time.RFC3339)
	m.token = token
	m.mu.Unlock()

	// 保存到文件
	return m.SaveToken(token)
}

// SaveToken 保存 Token
func (m *AuthManager) SaveToken(token *KiroAuthToken) error {
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 token 失败: %w", err)
	}

	if err := os.WriteFile(m.tokenPath, data, 0600); err != nil {
		return fmt.Errorf("保存 token 失败: %w", err)
	}

	return nil
}

// GetAccessToken 获取有效的 Access Token（加权轮询选择账号）
func (m *AuthManager) GetAccessToken() (string, error) {
	// 优先使用多账号轮询
	account, err := m.selectAccount()
	if err == nil && account != nil && account.Token != nil {
		return account.Token.AccessToken, nil
	}

	// 降级：使用旧的单 Token 逻辑
	token, err := m.ReadToken()
	if err != nil {
		return "", err
	}

	// 检查是否过期
	if token.IsExpired() {
		if err := m.RefreshToken(); err != nil {
			return "", fmt.Errorf("刷新 token 失败: %w", err)
		}
		token, err = m.ReadToken()
		if err != nil {
			return "", err
		}
	}

	return token.AccessToken, nil
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
	config := m.getAccountsFromCache()
	if config == nil || len(config.Accounts) == 0 {
		return ""
	}

	// 使用当前索引（不递增）计算上一次选中的账号
	// 因为 selectAccount 已经递增过了，所以用当前值
	idx := int(m.roundRobinIndex) % len(config.Accounts)

	// 找到可用账号列表
	var candidates []*AccountInfo
	for i := range config.Accounts {
		acc := &config.Accounts[i]
		if acc.Token == nil || acc.Token.IsExpired() {
			continue
		}
		if !m.isAccountAvailable(acc.ID) {
			continue
		}
		cache := m.getUsageCache(acc.ID)
		if cache != nil && cache.GetRemainingCredits() <= 0 {
			continue
		}
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		return ""
	}

	// 返回上一次选中的账号（索引-1，因为已经递增过了）
	prevIdx := idx
	if prevIdx < 0 {
		prevIdx = len(candidates) - 1
	}
	if prevIdx >= len(candidates) {
		prevIdx = prevIdx % len(candidates)
	}
	return candidates[prevIdx].ID
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
	token, err := m.ReadToken()
	if err != nil {
		return "us-east-1"
	}
	if token.Region == "" {
		return "us-east-1"
	}
	return token.Region
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
func (m *AuthManager) GetUsageLimits() (*UsageLimitsResponse, error) {
	// 获取有效的 Access Token
	accessToken, err := m.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("获取 access token 失败: %w", err)
	}

	// 获取 Profile ARN
	profileArn, err := m.GetProfileArn()
	if err != nil {
		return nil, fmt.Errorf("获取 profile ARN 失败: %w", err)
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

// GetUsageLimitsWithToken 使用指定 Token 获取额度（用于多账号场景）
func (m *AuthManager) GetUsageLimitsWithToken(accessToken, region string) (*UsageLimitsResponse, error) {
	if region == "" {
		region = "us-east-1"
	}

	// 尝试从本地文件获取 profileArn（所有账号共用同一个 profile）
	profileArn, _ := m.GetProfileArn()
	if profileArn == "" {
		return nil, fmt.Errorf("无法获取 profileArn")
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
			// 配置文件不存在，检查是否有现有 Token
			config = &AccountsConfig{Accounts: []AccountInfo{}}
			m.migrateExistingToken(config)
			// 更新缓存
			m.updateCache(config)
			return config, nil
		}
		return nil, fmt.Errorf("读取账号配置失败: %w", err)
	}

	// 如果账号列表为空但有 Token，自动迁移
	if len(config.Accounts) == 0 {
		m.migrateExistingToken(config)
	}

	// 更新缓存
	m.updateCache(config)

	return config, nil
}

// migrateExistingToken 将现有 Token 迁移为第一个账号
func (m *AuthManager) migrateExistingToken(config *AccountsConfig) {
	token, err := m.ReadToken()
	if err != nil || token == nil {
		return
	}

	// 读取客户端注册信息
	clientReg, _ := m.ReadClientRegistration()

	// 生成账号 ID
	h := sha256.New()
	h.Write([]byte(token.ClientIDHash))
	accountID := hex.EncodeToString(h.Sum(nil))[:16]

	account := AccountInfo{
		ID:         accountID,
		Token:      token,
		CreatedAt:  time.Now().Format(time.RFC3339),
		LastUsedAt: time.Now().Format(time.RFC3339),
	}

	if clientReg != nil {
		account.ClientID = clientReg.ClientID
		account.ClientSecret = clientReg.ClientSecret
	}

	config.Accounts = append(config.Accounts, account)

	// 保存配置
	m.SaveAccountsConfig(config)
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

	// 创建账号信息
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

	// 保存客户端注册信息到标准位置（兼容 Kiro IDE）
	homeDir, _ := os.UserHomeDir()
	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
	clientRegPath := filepath.Join(cacheDir, clientIDHash+".json")

	clientReg := &ClientRegistration{
		ClientID:     session.ClientID,
		ClientSecret: session.ClientSecret,
	}
	clientRegData, _ := json.MarshalIndent(clientReg, "", "  ")
	os.WriteFile(clientRegPath, clientRegData, 0600)

	// 先保存 Token 到标准路径，以便 GetUsageLimits 可以使用
	if err := m.SaveToken(token); err != nil {
		return nil, fmt.Errorf("保存 Token 失败: %w", err)
	}

	// 清除缓存，强制使用新 Token
	m.mu.Lock()
	m.token = nil
	m.clientReg = nil
	m.mu.Unlock()

	// 获取用户信息（userId）
	usage, err := m.GetUsageLimits()
	if err == nil && usage != nil {
		account.UserId = usage.UserInfo.UserId
		account.Email = usage.UserInfo.Email
	}

	// 加载现有账号配置
	config, err := m.LoadAccountsConfig()
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
	config, err := m.LoadAccountsConfig()
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

	// 将该账号的 Token 设置为当前 Token
	if targetAccount.Token != nil {
		if err := m.SaveToken(targetAccount.Token); err != nil {
			return fmt.Errorf("保存 Token 失败: %w", err)
		}
		// 清除缓存，强制重新加载
		m.mu.Lock()
		m.token = nil
		m.clientReg = nil
		m.mu.Unlock()
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
	go func(accID, accessToken, region string) {
		usage, err := m.GetUsageLimitsWithToken(accessToken, region)
		if err != nil {
			return
		}
		for _, item := range usage.UsageBreakdownList {
			if item.ResourceType == "CREDIT" {
				m.updateUsageCache(accID, item.CurrentUsageWithPrecision, item.UsageLimitWithPrecision)
				break
			}
		}
	}(accountID, refreshResp.AccessToken, region)

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
// 只刷新即将过期（30分钟内）的 Token，同时更新额度缓存
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

		// 检查是否即将过期（30分钟内）
		if !m.isTokenExpiringSoon(acc.Token, 30*time.Minute) {
			// Token 未过期，但仍然更新额度缓存（异步）
			go func(accID, accessToken, region string) {
				usage, err := m.GetUsageLimitsWithToken(accessToken, region)
				if err != nil {
					return
				}
				for _, item := range usage.UsageBreakdownList {
					if item.ResourceType == "CREDIT" {
						m.updateUsageCache(accID, item.CurrentUsageWithPrecision, item.UsageLimitWithPrecision)
						break
					}
				}
			}(acc.ID, acc.Token.AccessToken, acc.Token.Region)
			continue
		}

		// 使用该账号自己的 ClientID/ClientSecret 刷新
		err := m.RefreshAccountToken(acc.ID)
		if err != nil {
			// 刷新失败，记录日志但继续处理其他账号
			fmt.Printf("[保活] 账号 %s 刷新失败: %v\n", acc.ID, err)
		} else {
			fmt.Printf("[保活] 账号 %s Token 已刷新\n", acc.ID)
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

	// 创建账号信息
	account := &AccountInfo{
		ID:           accountID,
		Token:        &token,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		CreatedAt:    time.Now().Format(time.RFC3339),
		LastUsedAt:   time.Now().Format(time.RFC3339),
	}

	// 如果有 clientIdHash 和 clientReg，保存到标准位置
	if token.ClientIDHash != "" && clientID != "" && clientSecret != "" {
		homeDir, _ := os.UserHomeDir()
		cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
		os.MkdirAll(cacheDir, 0700)
		clientRegPath := filepath.Join(cacheDir, token.ClientIDHash+".json")
		clientRegData, _ := json.MarshalIndent(&ClientRegistration{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}, "", "  ")
		os.WriteFile(clientRegPath, clientRegData, 0600)
	}

	// 尝试获取用户信息
	region := token.Region
	if region == "" {
		region = "us-east-1"
	}
	usage, err := m.GetUsageLimitsWithToken(token.AccessToken, region)
	if err == nil && usage != nil {
		account.UserId = usage.UserInfo.UserId
		account.Email = usage.UserInfo.Email
	}

	// 加载现有账号配置
	config, err := m.LoadAccountsConfig()
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
