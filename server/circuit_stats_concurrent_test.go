package main

import (
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// ========== 并发安全测试 ==========

// TestConcurrentRecord 测试并发 Record 调用的数据一致性
// **Validates: Requirements 1.5 (并发安全)**
//
// 启动多个 goroutine 并发调用 Record()，验证最终统计数据的正确性：
// - 总请求数 = 所有 goroutine 记录的请求数之和
// - 成功数 + 失败数 = 总请求数
// - 错误率 = 失败数 / 总请求数
func TestConcurrentRecord(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "concurrent-record-test"

	// 并发参数
	numGoroutines := 10
	recordsPerGoroutine := 100
	expectedTotal := numGoroutines * recordsPerGoroutine

	// 使用 WaitGroup 等待所有 goroutine 完成
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// 启动多个 goroutine 并发记录
	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer wg.Done()
			// 每个 goroutine 记录一半成功、一半失败
			for j := 0; j < recordsPerGoroutine; j++ {
				success := j%2 == 0
				cs.Record(accountID, success)
			}
		}(i)
	}

	// 等待所有 goroutine 完成
	wg.Wait()

	// 验证统计数据
	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

	if totalReqs != int64(expectedTotal) {
		t.Errorf("并发记录后总请求数 = %d, want %d", totalReqs, expectedTotal)
	}

	// 每个 goroutine 记录 50 成功 + 50 失败
	// 总计：500 成功 + 500 失败 = 1000 请求，错误率 = 0.5
	expectedRate := 0.5
	if math.Abs(errorRate-expectedRate) > 1e-9 {
		t.Errorf("并发记录后错误率 = %f, want %f", errorRate, expectedRate)
	}
}

// TestConcurrentGetErrorRate 测试并发 GetErrorRate 调用的稳定性
// **Validates: Requirements 1.5 (并发安全)**
//
// 在一个 goroutine 持续写入数据的同时，多个 goroutine 并发读取错误率，
// 验证读取操作不会 panic 或返回异常数据（如负数、NaN）。
func TestConcurrentGetErrorRate(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "concurrent-read-test"

	// 预先写入一些数据
	for i := 0; i < 100; i++ {
		cs.Record(accountID, i%3 != 0) // 2/3 成功，1/3 失败
	}

	// 并发参数
	numReaders := 20
	readsPerReader := 50

	var readerWg sync.WaitGroup
	var writerWg sync.WaitGroup
	stopWriter := make(chan struct{})

	// 启动一个写入 goroutine（持续写入）
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for {
			select {
			case <-stopWriter:
				return
			default:
				cs.Record(accountID, rand.Intn(2) == 0)
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// 启动多个读取 goroutine
	errorChan := make(chan error, numReaders*readsPerReader)
	readerWg.Add(numReaders)
	for i := 0; i < numReaders; i++ {
		go func() {
			defer readerWg.Done()
			for j := 0; j < readsPerReader; j++ {
				errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

				// 验证返回值的合法性
				if errorRate < 0 || errorRate > 1 {
					errorChan <- fmt.Errorf("错误率超出范围: %f", errorRate)
					return
				}
				if math.IsNaN(errorRate) {
					errorChan <- fmt.Errorf("错误率为 NaN")
					return
				}
				if totalReqs < 0 {
					errorChan <- fmt.Errorf("总请求数为负数: %d", totalReqs)
					return
				}

				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	// 等待所有读取完成
	readerWg.Wait()

	// 停止写入并等待 writer 完成
	close(stopWriter)
	writerWg.Wait()
	close(errorChan)

	// 检查是否有错误
	for err := range errorChan {
		t.Error(err)
	}
}

// TestConcurrentMultiAccount 测试多账号并发访问的隔离性
// **Validates: Requirements 1.5 (并发安全)**
//
// 多个 goroutine 并发操作不同账号，验证账号之间的数据隔离：
// - 每个账号的统计数据独立
// - 不会出现数据串扰
func TestConcurrentMultiAccount(t *testing.T) {
	cs := NewCircuitStats()

	numAccounts := 5
	recordsPerAccount := 200

	var wg sync.WaitGroup
	wg.Add(numAccounts)

	// 每个账号使用不同的成功率
	successRates := []float64{0.0, 0.25, 0.5, 0.75, 1.0}

	for i := 0; i < numAccounts; i++ {
		go func(accountIndex int) {
			defer wg.Done()
			accountID := fmt.Sprintf("account-%d", accountIndex)
			successRate := successRates[accountIndex]

			for j := 0; j < recordsPerAccount; j++ {
				// 根据预设的成功率决定是否成功
				success := rand.Float64() < successRate
				cs.Record(accountID, success)
			}
		}(i)
	}

	wg.Wait()

	// 验证每个账号的统计数据
	for i := 0; i < numAccounts; i++ {
		accountID := fmt.Sprintf("account-%d", i)
		errorRate, totalReqs := cs.GetErrorRate(accountID, 5)

		if totalReqs != int64(recordsPerAccount) {
			t.Errorf("账号 %s 总请求数 = %d, want %d", accountID, totalReqs, recordsPerAccount)
		}

		// 验证错误率在合理范围内（允许随机误差 ±15%）
		expectedErrorRate := 1.0 - successRates[i]
		tolerance := 0.15
		if math.Abs(errorRate-expectedErrorRate) > tolerance {
			t.Errorf("账号 %s 错误率 = %f, 期望约 %f (±%f)", accountID, errorRate, expectedErrorRate, tolerance)
		}
	}
}

// TestConcurrentCleanup 测试并发清理的安全性
// **Validates: Requirements 1.5 (并发安全)**
//
// 在持续写入和读取的同时，触发清理操作，验证不会出现 panic 或数据损坏。
func TestConcurrentCleanup(t *testing.T) {
	cs := NewCircuitStats()
	accountID := "cleanup-test"

	// 预先写入一些数据
	for i := 0; i < 50; i++ {
		cs.Record(accountID, true)
	}

	var wg sync.WaitGroup
	stopChan := make(chan struct{})

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopChan:
				return
			default:
				cs.Record(accountID, rand.Intn(2) == 0)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopChan:
				return
			default:
				cs.GetErrorRate(accountID, 5)
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	// Cleanup goroutine（手动触发清理）
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopChan:
				return
			default:
				cs.cleanup()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// 运行 200ms
	time.Sleep(200 * time.Millisecond)
	close(stopChan)
	wg.Wait()

	// 验证最终数据仍然有效
	errorRate, totalReqs := cs.GetErrorRate(accountID, 5)
	if errorRate < 0 || errorRate > 1 {
		t.Errorf("清理后错误率异常: %f", errorRate)
	}
	if totalReqs < 0 {
		t.Errorf("清理后总请求数异常: %d", totalReqs)
	}
}
