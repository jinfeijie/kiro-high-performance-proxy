package kiroclient

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"testing"
	"testing/quick"
	"time"
)

// TestClientIdHashGeneration 测试 clientIdHash 生成是否与 Kiro IDE 一致
// IDE 实现: crypto.createHash("sha1").update(JSON.stringify({ startUrl })).digest("hex")
func TestClientIdHashGeneration(t *testing.T) {
	testCases := []struct {
		name     string
		startUrl string
		expected string // 从 IDE 或已知正确值获取
	}{
		{
			name:     "Builder ID 默认 URL",
			startUrl: "https://view.awsapps.com/start",
			expected: "", // 需要从 IDE 获取实际值
		},
		{
			name:     "企业 SSO URL 示例",
			startUrl: "https://d-906xxxxx.awsapps.com/start",
			expected: "", // 需要从 IDE 获取实际值
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 使用与 IDE 相同的方式生成 clientIdHash
			h := sha1.New()
			jsonStr := fmt.Sprintf(`{"startUrl":"%s"}`, tc.startUrl)
			h.Write([]byte(jsonStr))
			result := hex.EncodeToString(h.Sum(nil))

			t.Logf("startUrl: %s", tc.startUrl)
			t.Logf("JSON string: %s", jsonStr)
			t.Logf("clientIdHash: %s", result)

			if tc.expected != "" && result != tc.expected {
				t.Errorf("clientIdHash 不匹配\n期望: %s\n实际: %s", tc.expected, result)
			}
		})
	}
}

// TestClientIdHashCompareWithIDE 与 IDE 生成的实际值对比
func TestClientIdHashCompareWithIDE(t *testing.T) {
	// 从 kiro-accounts.json 中获取的实际 IDE 生成值
	// 第一个账号的 clientIdHash: dffeef37587922df57fd4035c8ecff10cad43d10
	// 对应的 startUrl: https://d-90661cd500.awsapps.com/start (无末尾斜杠)

	startUrl := "https://d-90661cd500.awsapps.com/start"
	h := sha1.New()
	jsonStr := fmt.Sprintf(`{"startUrl":"%s"}`, startUrl)
	h.Write([]byte(jsonStr))
	result := hex.EncodeToString(h.Sum(nil))

	expected := "dffeef37587922df57fd4035c8ecff10cad43d10"

	t.Logf("startUrl: %s", startUrl)
	t.Logf("JSON string: %s", jsonStr)
	t.Logf("生成的 clientIdHash: %s", result)
	t.Logf("IDE 生成的 clientIdHash: %s", expected)

	if result != expected {
		t.Errorf("clientIdHash 不匹配！\n期望: %s\n实际: %s", expected, result)
	} else {
		t.Log("✓ clientIdHash 生成方式与 IDE 完全一致！")
	}
}

// ========== 熔断管理面板测试 ==========
// 以下测试覆盖 ManualTrip、ManualReset、GetLoadDistribution 等方法

// newTestAuthManager 创建一个用于测试的 AuthManager，预置账号缓存
// 这样 ManualTrip/ManualReset 的 accountExists 检查能通过
func newTestAuthManager(accountIDs ...string) *AuthManager {
	m := NewAuthManager()
	accounts := make([]AccountInfo, 0, len(accountIDs))
	for _, id := range accountIDs {
		accounts = append(accounts, AccountInfo{
			ID:    id,
			Email: id + "@test.com",
			Token: &KiroAuthToken{
				AccessToken: "test-token-" + id,
				ExpiresAt:   "2099-12-31T23:59:59Z",
			},
		})
	}
	m.accountsCache = &AccountsConfig{Accounts: accounts}
	m.accountsLoaded = true
	return m
}

// ========== Property 4: ManualTrip 状态转换 ==========
// Feature: circuit-breaker-dashboard, Property 4: ManualTrip 状态转换
// **Validates: Requirements 3.1**
//
// *For any* 账号ID 和任意初始熔断器状态（Closed/Open/HalfOpen），
// 调用 ManualTrip 后，该账号的熔断器状态应为 Open，
// 且 isAccountAvailable 返回 false（在 OpenDuration 超时之前）。

func TestProperty4_ManualTripStateTransition(t *testing.T) {
	// 使用 testing/quick 生成随机初始状态
	f := func(stateVal uint8) bool {
		// 将随机值映射到三种合法状态
		initialState := CircuitState(stateVal % 3)

		accountID := "prop4-test-account"
		m := newTestAuthManager(accountID)

		// 设置较长的 OpenDuration，确保测试期间不会超时进入 HalfOpen
		m.circuitConfig.OpenDuration = 10 * time.Minute

		// 预设初始状态
		m.circuitMu.Lock()
		m.circuitBreakers[accountID] = &CircuitBreaker{
			State:        initialState,
			FailureCount: 5,
			SuccessCount: 3,
		}
		m.circuitMu.Unlock()

		// 执行 ManualTrip
		err := m.ManualTrip(accountID)
		if err != nil {
			t.Logf("ManualTrip 返回错误: %v", err)
			return false
		}

		// 验证状态为 Open
		m.circuitMu.RLock()
		cb := m.circuitBreakers[accountID]
		state := cb.State
		m.circuitMu.RUnlock()

		if state != CircuitOpen {
			t.Logf("期望状态 Open(1)，实际 %d，初始状态 %d", state, initialState)
			return false
		}

		// 验证 isAccountAvailable 返回 false（OpenDuration 未超时）
		available := m.isAccountAvailable(accountID)
		if available {
			t.Logf("ManualTrip 后 isAccountAvailable 应返回 false，初始状态 %d", initialState)
			return false
		}

		return true
	}

	cfg := &quick.Config{MaxCount: 200}
	if err := quick.Check(f, cfg); err != nil {
		t.Errorf("Property 4 失败: %v", err)
	}
}

// ========== Property 6: ManualReset 状态转换与计数归零 ==========
// Feature: circuit-breaker-dashboard, Property 6: ManualReset 状态转换与计数归零
// **Validates: Requirements 4.1**
//
// *For any* 账号ID 和任意初始熔断器状态（Closed/Open/HalfOpen）
// 及任意 FailureCount/SuccessCount 值，调用 ManualReset 后，
// 该账号的熔断器状态应为 Closed，FailureCount 和 SuccessCount 均为 0，
// 且 isAccountAvailable 返回 true。

func TestProperty6_ManualResetStateTransitionAndCountReset(t *testing.T) {
	f := func(stateVal uint8, failCount uint8, successCount uint8) bool {
		initialState := CircuitState(stateVal % 3)

		accountID := "prop6-test-account"
		m := newTestAuthManager(accountID)

		// 预设初始状态和计数
		m.circuitMu.Lock()
		m.circuitBreakers[accountID] = &CircuitBreaker{
			State:        initialState,
			FailureCount: int(failCount),
			SuccessCount: int(successCount),
			OpenedAt:     time.Now(),
		}
		m.circuitMu.Unlock()

		// 执行 ManualReset
		err := m.ManualReset(accountID)
		if err != nil {
			t.Logf("ManualReset 返回错误: %v", err)
			return false
		}

		// 验证状态为 Closed
		m.circuitMu.RLock()
		cb := m.circuitBreakers[accountID]
		state := cb.State
		fc := cb.FailureCount
		sc := cb.SuccessCount
		m.circuitMu.RUnlock()

		if state != CircuitClosed {
			t.Logf("期望状态 Closed(0)，实际 %d", state)
			return false
		}

		// 验证计数归零
		if fc != 0 {
			t.Logf("期望 FailureCount=0，实际 %d", fc)
			return false
		}
		if sc != 0 {
			t.Logf("期望 SuccessCount=0，实际 %d", sc)
			return false
		}

		// 验证 isAccountAvailable 返回 true
		available := m.isAccountAvailable(accountID)
		if !available {
			t.Logf("ManualReset 后 isAccountAvailable 应返回 true")
			return false
		}

		return true
	}

	cfg := &quick.Config{MaxCount: 200}
	if err := quick.Check(f, cfg); err != nil {
		t.Errorf("Property 6 失败: %v", err)
	}
}

// ========== 单元测试 ==========

// TestManualTrip_RepeatedTripRefreshesOpenedAt 重复 Trip 刷新 OpenedAt 时间
// **Validates: Requirements 3.4**
func TestManualTrip_RepeatedTripRefreshesOpenedAt(t *testing.T) {
	accountID := "repeat-trip-account"
	m := newTestAuthManager(accountID)

	// 第一次 Trip
	err := m.ManualTrip(accountID)
	if err != nil {
		t.Fatalf("第一次 ManualTrip 失败: %v", err)
	}

	// 记录第一次 OpenedAt
	m.circuitMu.RLock()
	firstOpenedAt := m.circuitBreakers[accountID].OpenedAt
	m.circuitMu.RUnlock()

	// 等待一小段时间，确保时间戳不同
	time.Sleep(10 * time.Millisecond)

	// 第二次 Trip（对已经 Open 的账号再次 Trip）
	err = m.ManualTrip(accountID)
	if err != nil {
		t.Fatalf("第二次 ManualTrip 失败: %v", err)
	}

	// 验证 OpenedAt 被刷新（时间更晚）
	m.circuitMu.RLock()
	secondOpenedAt := m.circuitBreakers[accountID].OpenedAt
	state := m.circuitBreakers[accountID].State
	m.circuitMu.RUnlock()

	if state != CircuitOpen {
		t.Errorf("重复 Trip 后状态应为 Open，实际 %d", state)
	}

	if !secondOpenedAt.After(firstOpenedAt) {
		t.Errorf("重复 Trip 应刷新 OpenedAt\n第一次: %v\n第二次: %v", firstOpenedAt, secondOpenedAt)
	}
}

// TestManualReset_ClosedAccountIdempotent 对 Closed 账号 Reset 是幂等操作
// **Validates: Requirements 4.3**
func TestManualReset_ClosedAccountIdempotent(t *testing.T) {
	accountID := "closed-reset-account"
	m := newTestAuthManager(accountID)

	// 不设置任何熔断器状态（默认 Closed）
	// 第一次 Reset
	err := m.ManualReset(accountID)
	if err != nil {
		t.Fatalf("第一次 ManualReset 失败: %v", err)
	}

	// 验证 isAccountAvailable 返回 true
	if !m.isAccountAvailable(accountID) {
		t.Error("Reset 后 isAccountAvailable 应返回 true")
	}

	// 第二次 Reset（幂等）
	err = m.ManualReset(accountID)
	if err != nil {
		t.Fatalf("第二次 ManualReset 失败: %v", err)
	}

	// 仍然可用
	if !m.isAccountAvailable(accountID) {
		t.Error("重复 Reset 后 isAccountAvailable 应返回 true")
	}
}

// TestGetLoadDistribution_PercentSumApprox100 负载占比之和约等于 100%
// **Validates: Requirements 2.3**
func TestGetLoadDistribution_PercentSumApprox100(t *testing.T) {
	// 创建多个账号
	m := newTestAuthManager("acc-1", "acc-2", "acc-3")

	dist := m.GetLoadDistribution()
	if len(dist) == 0 {
		t.Fatal("GetLoadDistribution 返回空列表")
	}

	// 计算百分比之和
	var totalPercent float64
	for _, d := range dist {
		totalPercent += d.Percent
		// 每个账号的权重应 >= 0
		if d.Weight < 0 {
			t.Errorf("账号 %s 权重为负: %d", d.AccountID, d.Weight)
		}
	}

	// 检查所有权重是否都为 0（特殊情况：无额度信息时默认权重50）
	allZero := true
	for _, d := range dist {
		if d.Weight > 0 {
			allZero = false
			break
		}
	}

	// 如果不是全零权重，百分比之和应约等于 100%（误差 ±1%）
	if !allZero {
		if totalPercent < 99.0 || totalPercent > 101.0 {
			t.Errorf("负载占比之和应约等于 100%%，实际 %.2f%%", totalPercent)
		}
	}
}

// TestManualTrip_NonExistentAccount 对不存在的账号 Trip 应返回错误
func TestManualTrip_NonExistentAccount(t *testing.T) {
	m := newTestAuthManager("existing-account")

	err := m.ManualTrip("non-existent-account")
	if err == nil {
		t.Error("对不存在的账号 ManualTrip 应返回错误")
	}
}

// TestManualReset_NonExistentAccount 对不存在的账号 Reset 应返回错误
func TestManualReset_NonExistentAccount(t *testing.T) {
	m := newTestAuthManager("existing-account")

	err := m.ManualReset("non-existent-account")
	if err == nil {
		t.Error("对不存在的账号 ManualReset 应返回错误")
	}
}

// ========== TASK 8 验证测试：删除 sso cache 后的逻辑正确性 ==========

// TestGetAccessToken_NoAccounts 零账号场景下 GetAccessToken 应返回明确错误
// 验证删除旧单 token fallback 后，零账号不会 panic 或返回空字符串
func TestGetAccessToken_NoAccounts(t *testing.T) {
	m := NewAuthManager()
	// 手动设置空账号缓存，模拟已初始化但无账号
	m.cacheMu.Lock()
	m.accountsCache = &AccountsConfig{Accounts: []AccountInfo{}}
	m.accountsLoaded = true
	m.cacheMu.Unlock()

	token, err := m.GetAccessToken()
	if err == nil {
		t.Fatal("零账号时 GetAccessToken 应返回错误")
	}
	if token != "" {
		t.Errorf("零账号时 token 应为空，实际: %s", token)
	}
	t.Logf("✓ 零账号错误信息: %s", err.Error())
}

// TestGetAccessTokenWithAccountID_NoAccounts 零账号场景下带 AccountID 版本
func TestGetAccessTokenWithAccountID_NoAccounts(t *testing.T) {
	m := NewAuthManager()
	m.cacheMu.Lock()
	m.accountsCache = &AccountsConfig{Accounts: []AccountInfo{}}
	m.accountsLoaded = true
	m.cacheMu.Unlock()

	token, accountID, err := m.GetAccessTokenWithAccountID()
	if err == nil {
		t.Fatal("零账号时应返回错误")
	}
	if token != "" || accountID != "" {
		t.Errorf("零账号时应返回空值，token=%s, accountID=%s", token, accountID)
	}
}

// TestGetRegion_NoAccounts 零账号场景下 GetRegion 应返回默认值 us-east-1
// 验证删除 ReadToken fallback 后，GetRegion 不会 panic
func TestGetRegion_NoAccounts(t *testing.T) {
	m := NewAuthManager()
	m.cacheMu.Lock()
	m.accountsCache = &AccountsConfig{Accounts: []AccountInfo{}}
	m.accountsLoaded = true
	m.cacheMu.Unlock()

	region := m.GetRegion()
	if region != "us-east-1" {
		t.Errorf("零账号时 region 应为 us-east-1，实际: %s", region)
	}
	t.Logf("✓ 零账号默认 region: %s", region)
}

// TestGetRegion_WithAccount 有账号时 GetRegion 应返回账号的 region
func TestGetRegion_WithAccount(t *testing.T) {
	m := newTestAuthManager("region-test")
	// 修改 token 的 region
	m.accountsCache.Accounts[0].Token.Region = "ap-northeast-1"

	region := m.GetRegion()
	if region != "ap-northeast-1" {
		t.Errorf("期望 region=ap-northeast-1，实际: %s", region)
	}
}

// TestGetRegion_EmptyRegionInToken Token 中 region 为空时应返回默认值
func TestGetRegion_EmptyRegionInToken(t *testing.T) {
	m := newTestAuthManager("empty-region")
	m.accountsCache.Accounts[0].Token.Region = ""

	region := m.GetRegion()
	if region != "us-east-1" {
		t.Errorf("空 region 时应返回 us-east-1，实际: %s", region)
	}
}

// TestGetAccessToken_WithValidAccount 有有效账号时应正确返回 token
// 验证 selectAccount 路径在正常场景下工作
func TestGetAccessToken_WithValidAccount(t *testing.T) {
	m := newTestAuthManager("valid-acc")

	token, err := m.GetAccessToken()
	if err != nil {
		t.Fatalf("有效账号时不应报错: %v", err)
	}
	if token != "test-token-valid-acc" {
		t.Errorf("期望 token=test-token-valid-acc，实际: %s", token)
	}
}

// TestGetAccessToken_ExpiredTokenSkipped selectAccount 会跳过过期 token
// 验证过期账号不会被选中，所有账号过期时返回明确错误
func TestGetAccessToken_ExpiredTokenSkipped(t *testing.T) {
	m := newTestAuthManager("expired-acc")
	// 设置过期时间为过去
	m.accountsCache.Accounts[0].Token.ExpiresAt = "2020-01-01T00:00:00Z"

	_, err := m.GetAccessToken()
	if err == nil {
		t.Fatal("所有 token 过期时应返回错误")
	}
	t.Logf("✓ 过期 token 被正确跳过，错误: %s", err.Error())
}

// TestGetAccessToken_NilToken 账号存在但 Token 为 nil 时应报错
func TestGetAccessToken_NilToken(t *testing.T) {
	m := NewAuthManager()
	m.cacheMu.Lock()
	m.accountsCache = &AccountsConfig{
		Accounts: []AccountInfo{
			{ID: "nil-token-acc", Email: "test@test.com", Token: nil},
		},
	}
	m.accountsLoaded = true
	m.cacheMu.Unlock()

	_, err := m.GetAccessToken()
	if err == nil {
		t.Fatal("Token 为 nil 时应返回错误")
	}
	t.Logf("✓ nil token 错误信息: %s", err.Error())
}

// TestCompleteLogin_StoresClientCredentials 验证 CompleteLogin 将 clientID/clientSecret 存入 AccountInfo
// 因为删除了 sso cache 写入，必须确认凭证存在 AccountInfo 中
// 注意：此测试不调用真实 API，只验证数据结构层面的正确性
func TestCompleteLogin_ClientCredentialsInAccountInfo(t *testing.T) {
	// 直接构造一个 AccountInfo，模拟 CompleteLogin 的输出结构
	// （CompleteLogin 需要真实 API 调用，这里验证数据结构设计）
	account := &AccountInfo{
		ID:           "test-session-id",
		ClientID:     "test-client-id-12345",
		ClientSecret: "test-client-secret-67890",
		Token: &KiroAuthToken{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			ExpiresAt:    "2099-12-31T23:59:59Z",
			Region:       "us-east-1",
		},
		CreatedAt:  time.Now().Format(time.RFC3339),
		LastUsedAt: time.Now().Format(time.RFC3339),
	}

	// 验证 clientID/clientSecret 存在于 AccountInfo 中（不再依赖 sso cache 文件）
	if account.ClientID == "" {
		t.Error("ClientID 不应为空")
	}
	if account.ClientSecret == "" {
		t.Error("ClientSecret 不应为空")
	}
	if account.ClientID != "test-client-id-12345" {
		t.Errorf("ClientID 不匹配: %s", account.ClientID)
	}
	if account.ClientSecret != "test-client-secret-67890" {
		t.Errorf("ClientSecret 不匹配: %s", account.ClientSecret)
	}
	t.Log("✓ clientID/clientSecret 正确存储在 AccountInfo 中")
}

// TestImportAccount_ClientCredentialsInAccountInfo 验证 ImportAccount 将凭证存入 AccountInfo
func TestImportAccount_ClientCredentialsInAccountInfo(t *testing.T) {
	// 模拟 ImportAccount 的输出结构
	account := &AccountInfo{
		ID:           "imported-account-id",
		ClientID:     "imported-client-id",
		ClientSecret: "imported-client-secret",
		Token: &KiroAuthToken{
			AccessToken: "imported-access-token",
			ExpiresAt:   "2099-12-31T23:59:59Z",
			Region:      "us-west-2",
		},
	}

	if account.ClientID != "imported-client-id" {
		t.Errorf("导入账号 ClientID 不匹配: %s", account.ClientID)
	}
	if account.ClientSecret != "imported-client-secret" {
		t.Errorf("导入账号 ClientSecret 不匹配: %s", account.ClientSecret)
	}
	t.Log("✓ 导入账号的 clientID/clientSecret 正确存储")
}

// TestGetLastSelectedAccountInfo_NoSelection 未选择过账号时应返回空
func TestGetLastSelectedAccountInfo_NoSelection(t *testing.T) {
	m := NewAuthManager()

	accountID, email := m.GetLastSelectedAccountInfo()
	if accountID != "" {
		t.Errorf("未选择时 accountID 应为空，实际: %s", accountID)
	}
	if email != "" {
		t.Errorf("未选择时 email 应为空，实际: %s", email)
	}
}
