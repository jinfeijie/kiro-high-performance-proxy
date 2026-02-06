package kiroclient

// SetAccountsCacheForTest 仅供外部包测试使用
// 为什么需要：server 包的测试需要注入测试账号到 AuthManager，
// 但 accountsCache 是未导出字段，无法从外部包直接访问
func (m *AuthManager) SetAccountsCacheForTest(config *AccountsConfig) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	m.accountsCache = config
	m.accountsLoaded = true
}
