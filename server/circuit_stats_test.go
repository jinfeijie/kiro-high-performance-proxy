package main

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
	"time"
)

// ========== 属性测试 ==========

// Feature: circuit-breaker-dashboard, Property 1: Record 与 GetErrorRate round-trip 一致性
// **Validates: Requirements 1.1, 1.3**
//
// 对于任意账号ID、任意数量的成功请求 S 和失败请求 F（S+F > 0），
// 在同一时间窗口内调用 Record() 记录后，GetErrorRate() 返回的错误率
// 应等于 F/(S+F)，总请求数应等于 S+F。
func TestProperty1_RecordGetErrorRate_RoundTrip(t *testing.T) {
	// 使用 testing/quick 生成随机输入
	f := func(successRaw, failureRaw uint16) bool {
		// 将 uint16 限制到合理范围（1~500），确保 S+F > 0
		s := int(successRaw%500) + 1
		fCount := int(failureRaw % 500)

		total := s + fCount
		if total == 0 {
			return true // 跳过无效输入
		}

		cs := NewCircuitStats()
		accountID := "prop1-test-account"

		// 记录 s 次成功
		for i := 0; i < s; i++ {
			cs.Record(accountID, true)
		}
		// 记录 fCount 次失败
		for i := 0; i < fCount; i++ {
			cs.Record(accountID, false)
		}

		// 查询最近5分钟的错误率（所有记录都在当前时间桶内）
		errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

		// 验证总请求数
		if totalReqs != int64(total) {
			t.Logf("总请求数不匹配: got %d, want %d (s=%d, f=%d)", totalReqs, total, s, fCount)
			return false
		}

		// 验证错误率（浮点数比较，允许极小误差）
		expectedRate := float64(fCount) / float64(total)
		if math.Abs(errorRate-expectedRate) > 1e-9 {
			t.Logf("错误率不匹配: got %f, want %f (s=%d, f=%d)", errorRate, expectedRate, s, fCount)
			return false
		}

		return true
	}

	cfg := &quick.Config{MaxCount: 100}
	if err := quick.Check(f, cfg); err != nil {
		t.Errorf("Property 1 失败: %v", err)
	}
}

// Feature: circuit-breaker-dashboard, Property 2: 时间桶数量不变量
// **Validates: Requirements 1.2**
//
// 对于任意账号，无论记录了多少次请求，AccountCircuitStats 中的有效桶数量
// 不超过 30 个（5分钟/10秒），且所有有效桶的时间戳都在当前时间往前5分钟范围内。
func TestProperty2_BucketCountInvariant(t *testing.T) {
	f := func(recordCountRaw uint16) bool {
		// 记录次数限制在 1~1000
		recordCount := int(recordCountRaw%1000) + 1

		cs := NewCircuitStats()
		accountID := "prop2-test-account"

		// 随机记录成功或失败
		rng := rand.New(rand.NewSource(int64(recordCount)))
		for i := 0; i < recordCount; i++ {
			cs.Record(accountID, rng.Intn(2) == 0)
		}

		// 获取账号统计数据，检查桶数量
		cs.mu.RLock()
		acct, exists := cs.accounts[accountID]
		cs.mu.RUnlock()

		if !exists {
			t.Log("账号不存在，但已记录过请求")
			return false
		}

		acct.mu.Lock()
		bucketCount := len(acct.Buckets)
		now := time.Now().Unix()
		cutoff := now - maxWindowSeconds

		// 检查所有桶的时间戳都在有效范围内
		for _, b := range acct.Buckets {
			// 桶的结束时间必须在 cutoff 之后（即桶未过期）
			if b.Timestamp+bucketSeconds <= cutoff {
				t.Logf("发现过期桶: ts=%d, cutoff=%d", b.Timestamp, cutoff)
				acct.mu.Unlock()
				return false
			}
			// 桶的起始时间不能超过当前时间
			if b.Timestamp > now {
				t.Logf("发现未来桶: ts=%d, now=%d", b.Timestamp, now)
				acct.mu.Unlock()
				return false
			}
		}
		acct.mu.Unlock()

		// 桶数量不超过 maxBuckets（30个）
		if bucketCount > maxBuckets {
			t.Logf("桶数量超限: got %d, max %d", bucketCount, maxBuckets)
			return false
		}

		return true
	}

	cfg := &quick.Config{MaxCount: 100}
	if err := quick.Check(f, cfg); err != nil {
		t.Errorf("Property 2 失败: %v", err)
	}
}

// ========== 单元测试 ==========

// TestGetErrorRate_EmptyAccount 空账号返回 (0, 0)
// 满足需求 1.4：没有任何请求记录时返回错误率为0且总请求数为0
func TestGetErrorRate_EmptyAccount(t *testing.T) {
	cs := NewCircuitStats()

	errorRate, totalReqs := cs.GetErrorRate("nonexistent-account", 5)

	if errorRate != 0 {
		t.Errorf("空账号错误率 = %f, want 0", errorRate)
	}
	if totalReqs != 0 {
		t.Errorf("空账号总请求数 = %d, want 0", totalReqs)
	}
}

// TestGetErrorRate_SingleBucket 单桶精确计算
// 验证在单个时间桶内的精确错误率计算
func TestGetErrorRate_SingleBucket(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "single-bucket-test"

	// 记录 7 次成功、3 次失败 -> 错误率 = 3/10 = 0.3
	for i := 0; i < 7; i++ {
		cs.Record(accountID, true)
	}
	for i := 0; i < 3; i++ {
		cs.Record(accountID, false)
	}

	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

	if totalReqs != 10 {
		t.Errorf("总请求数 = %d, want 10", totalReqs)
	}

	expectedRate := 0.3
	if math.Abs(errorRate-expectedRate) > 1e-9 {
		t.Errorf("错误率 = %f, want %f", errorRate, expectedRate)
	}
}

// TestGetErrorRate_MultiBucket_WindowFilter 多桶跨窗口过滤
// 验证只有在指定时间窗口内的桶才被计入统计
func TestGetErrorRate_MultiBucket_WindowFilter(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "multi-bucket-test"

	// 手动构造桶数据，模拟不同时间段的请求
	cs.mu.Lock()
	acct := &AccountCircuitStats{}
	cs.accounts[accountID] = acct
	cs.mu.Unlock()

	now := time.Now().Unix()
	nowBucket := alignToBucket(now)

	acct.mu.Lock()
	// 桶1：6分钟前（超出5分钟窗口，应被过滤）
	acct.Buckets = append(acct.Buckets, TimeBucket{
		Timestamp: nowBucket - 360,
		Success:   100,
		Failure:   100,
	})
	// 桶2：2分钟前（在5分钟窗口内）
	acct.Buckets = append(acct.Buckets, TimeBucket{
		Timestamp: nowBucket - 120,
		Success:   8,
		Failure:   2,
	})
	// 桶3：当前桶（在5分钟窗口内）
	acct.Buckets = append(acct.Buckets, TimeBucket{
		Timestamp: nowBucket,
		Success:   5,
		Failure:   5,
	})
	acct.mu.Unlock()

	// 查询最近5分钟：只应包含桶2和桶3
	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

	// 桶2: 8+2=10, 桶3: 5+5=10 -> 总计20请求，7次失败
	expectedTotal := int64(20)
	if totalReqs != expectedTotal {
		t.Errorf("总请求数 = %d, want %d", totalReqs, expectedTotal)
	}

	// 失败数 = 2+5 = 7, 错误率 = 7/20 = 0.35
	expectedRate := 7.0 / 20.0
	if math.Abs(errorRate-expectedRate) > 1e-9 {
		t.Errorf("错误率 = %f, want %f", errorRate, expectedRate)
	}

	// 查询最近1分钟：只应包含桶3（当前桶）
	errorRate1m, totalReqs1m := cs.GetErrorRate(accountID, 1)

	if totalReqs1m != 10 {
		t.Errorf("1分钟总请求数 = %d, want 10", totalReqs1m)
	}

	expectedRate1m := 5.0 / 10.0
	if math.Abs(errorRate1m-expectedRate1m) > 1e-9 {
		t.Errorf("1分钟错误率 = %f, want %f", errorRate1m, expectedRate1m)
	}
}

// TestRecord_CreatesBucket 验证 Record 能正确创建新桶
func TestRecord_CreatesBucket(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "create-bucket-test"

	cs.Record(accountID, true)

	cs.mu.RLock()
	acct, exists := cs.accounts[accountID]
	cs.mu.RUnlock()

	if !exists {
		t.Fatal("Record 后账号应该存在")
	}

	acct.mu.Lock()
	defer acct.mu.Unlock()

	if len(acct.Buckets) != 1 {
		t.Fatalf("桶数量 = %d, want 1", len(acct.Buckets))
	}

	b := acct.Buckets[0]
	if b.Success != 1 {
		t.Errorf("成功次数 = %d, want 1", b.Success)
	}
	if b.Failure != 0 {
		t.Errorf("失败次数 = %d, want 0", b.Failure)
	}

	// 验证时间戳对齐到10秒边界
	if b.Timestamp%bucketSeconds != 0 {
		t.Errorf("时间戳未对齐: %d %% %d = %d", b.Timestamp, bucketSeconds, b.Timestamp%bucketSeconds)
	}
}

// TestAlignToBucket 验证时间戳对齐逻辑
func TestAlignToBucket(t *testing.T) {
	tests := []struct {
		input    int64
		expected int64
	}{
		{1000, 1000}, // 已对齐
		{1001, 1000}, // 向下对齐
		{1009, 1000}, // 向下对齐
		{1010, 1010}, // 已对齐
		{1015, 1010}, // 向下对齐
		{0, 0},       // 零值
	}

	for _, tt := range tests {
		got := alignToBucket(tt.input)
		if got != tt.expected {
			t.Errorf("alignToBucket(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// TestCleanupAccount_RemovesExpiredBuckets 验证过期桶清理
func TestCleanupAccount_RemovesExpiredBuckets(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "cleanup-test"

	cs.mu.Lock()
	acct := &AccountCircuitStats{}
	cs.accounts[accountID] = acct
	cs.mu.Unlock()

	now := time.Now().Unix()
	nowBucket := alignToBucket(now)

	acct.mu.Lock()
	// 添加一个过期桶（10分钟前）和一个有效桶（当前）
	acct.Buckets = append(acct.Buckets, TimeBucket{
		Timestamp: nowBucket - 600,
		Success:   50,
		Failure:   50,
	})
	acct.Buckets = append(acct.Buckets, TimeBucket{
		Timestamp: nowBucket,
		Success:   5,
		Failure:   3,
	})

	// 执行清理
	cs.cleanupAccount(acct, now)

	// 过期桶应被移除，只剩当前桶
	if len(acct.Buckets) != 1 {
		t.Errorf("清理后桶数量 = %d, want 1", len(acct.Buckets))
	}
	if acct.Buckets[0].Timestamp != nowBucket {
		t.Errorf("剩余桶时间戳 = %d, want %d", acct.Buckets[0].Timestamp, nowBucket)
	}
	acct.mu.Unlock()
}

// TestGetErrorRate_AllFailures 全部失败时错误率为1.0
func TestGetErrorRate_AllFailures(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "all-failures"

	for i := 0; i < 10; i++ {
		cs.Record(accountID, false)
	}

	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

	if totalReqs != 10 {
		t.Errorf("总请求数 = %d, want 10", totalReqs)
	}
	if errorRate != 1.0 {
		t.Errorf("错误率 = %f, want 1.0", errorRate)
	}
}

// TestGetErrorRate_AllSuccess 全部成功时错误率为0
func TestGetErrorRate_AllSuccess(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "all-success"

	for i := 0; i < 10; i++ {
		cs.Record(accountID, true)
	}

	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

	if totalReqs != 10 {
		t.Errorf("总请求数 = %d, want 10", totalReqs)
	}
	if errorRate != 0 {
		t.Errorf("错误率 = %f, want 0", errorRate)
	}
}
