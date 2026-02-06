package main

import (
	"sync"
	"time"
)

// ========== 常量定义 ==========

const (
	// bucketSeconds 每个时间桶的跨度（10秒）
	bucketSeconds = 10
	// maxBuckets 最多保留的桶数量（5分钟 / 10秒 = 30个桶）
	maxBuckets = 30
	// maxWindowSeconds 最大时间窗口（5分钟 = 300秒）
	maxWindowSeconds = 300
)

// ========== 数据结构 ==========

// TimeBucket 单个时间桶（10秒一个桶）
// 记录该时间段内的成功和失败请求次数
type TimeBucket struct {
	Timestamp int64 // 桶的起始时间戳（秒，对齐到10秒边界）
	Success   int64 // 成功次数
	Failure   int64 // 失败次数
}

// AccountCircuitStats 单个账号的统计数据
// 每个账号独立维护自己的时间桶列表和锁
type AccountCircuitStats struct {
	Buckets []TimeBucket
	mu      sync.Mutex
}

// CircuitStats 全局错误率统计器
// 管理所有账号的统计数据，使用读写锁保护 accounts map
type CircuitStats struct {
	accounts map[string]*AccountCircuitStats
	mu       sync.RWMutex
}

// ========== 构造函数 ==========

// NewCircuitStats 创建全局错误率统计器
// 启动后台清理协程,定期清理过期时间桶,防止内存泄漏
func NewCircuitStats() *CircuitStats {
	cs := &CircuitStats{
		accounts: make(map[string]*AccountCircuitStats),
	}

	// 启动后台清理协程(每1分钟清理一次过期桶)
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cs.cleanup()
		}
	}()

	return cs
}

// ClearAccount 清除指定账号的所有统计数据
// 用于手动解除熔断时，避免残留的高错误率数据导致秒回熔断
func (cs *CircuitStats) ClearAccount(accountID string) {
	cs.mu.Lock()
	delete(cs.accounts, accountID)
	cs.mu.Unlock()
}

// ========== 核心方法 ==========

// alignToB bucket 将时间戳对齐到10秒边界
// 例如：1234567 -> 1234560
func alignToBucket(ts int64) int64 {
	return ts - ts%bucketSeconds
}

// Record 记录一次请求结果
// 找到或创建当前时间桶，累加成功/失败计数，然后清理过期桶
func (cs *CircuitStats) Record(accountID string, success bool) {
	// 先用读锁查找账号，大多数情况下账号已存在
	cs.mu.RLock()
	acct, exists := cs.accounts[accountID]
	cs.mu.RUnlock()

	// 账号不存在时，用写锁创建（双重检查避免竞态）
	if !exists {
		cs.mu.Lock()
		acct, exists = cs.accounts[accountID]
		if !exists {
			acct = &AccountCircuitStats{}
			cs.accounts[accountID] = acct
		}
		cs.mu.Unlock()
	}

	now := time.Now().Unix()
	bucketTS := alignToBucket(now)

	acct.mu.Lock()
	defer acct.mu.Unlock()

	// 查找当前时间桶（从尾部开始找，因为最新的桶在末尾）
	found := false
	for i := len(acct.Buckets) - 1; i >= 0; i-- {
		if acct.Buckets[i].Timestamp == bucketTS {
			if success {
				acct.Buckets[i].Success++
			} else {
				acct.Buckets[i].Failure++
			}
			found = true
			break
		}
	}

	// 桶不存在则创建新桶
	if !found {
		b := TimeBucket{Timestamp: bucketTS}
		if success {
			b.Success = 1
		} else {
			b.Failure = 1
		}
		acct.Buckets = append(acct.Buckets, b)
	}

	// 清理过期桶
	cs.cleanupAccount(acct, now)
}

// GetErrorRate 查询指定账号在最近 windowMinutes 分钟内的错误率
// 返回 (errorRate, totalRequests)
// 如果没有请求记录，返回 (0, 0)
func (cs *CircuitStats) GetErrorRate(accountID string, windowMinutes int) (float64, int64) {
	cs.mu.RLock()
	acct, exists := cs.accounts[accountID]
	cs.mu.RUnlock()

	// 账号不存在，返回零值（满足需求 1.4）
	if !exists {
		return 0, 0
	}

	now := time.Now().Unix()
	windowStart := now - int64(windowMinutes*60)

	acct.mu.Lock()
	defer acct.mu.Unlock()

	var totalSuccess, totalFailure int64
	for _, b := range acct.Buckets {
		// 只汇总时间窗口内的桶
		// 桶的结束时间 = Timestamp + bucketSeconds
		// 桶至少部分落在窗口内才计入(修复边界条件:使用>=而不是>)
		if b.Timestamp+bucketSeconds >= windowStart && b.Timestamp <= now {
			totalSuccess += b.Success
			totalFailure += b.Failure
		}
	}

	total := totalSuccess + totalFailure
	if total == 0 {
		return 0, 0
	}

	errorRate := float64(totalFailure) / float64(total)
	return errorRate, total
}

// cleanup 清理所有账号的过期桶
func (cs *CircuitStats) cleanup() {
	now := time.Now().Unix()

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, acct := range cs.accounts {
		acct.mu.Lock()
		cs.cleanupAccount(acct, now)
		acct.mu.Unlock()
	}
}

// cleanupAccount 清理单个账号的过期桶
// 移除超过 5 分钟的桶，调用方必须持有 acct.mu 锁
func (cs *CircuitStats) cleanupAccount(acct *AccountCircuitStats, now int64) {
	cutoff := now - maxWindowSeconds

	// 从头部开始找到第一个未过期的桶
	// 桶按时间顺序排列，过期的都在前面
	keepFrom := 0
	for keepFrom < len(acct.Buckets) {
		if acct.Buckets[keepFrom].Timestamp+bucketSeconds > cutoff {
			break
		}
		keepFrom++
	}

	// 有过期桶需要清理
	if keepFrom > 0 {
		remaining := len(acct.Buckets) - keepFrom
		copy(acct.Buckets, acct.Buckets[keepFrom:])
		acct.Buckets = acct.Buckets[:remaining]
	}
}
