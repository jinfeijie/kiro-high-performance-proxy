package main

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestTruncateBody_NoTruncation 测试不需要截断的情况
func TestTruncateBody_NoTruncation(t *testing.T) {
	body := "short body"
	result, truncated := TruncateBody(body, MaxBodySize)
	if truncated {
		t.Error("短内容不应该被截断")
	}
	if result != body {
		t.Errorf("结果 = %q, want %q", result, body)
	}
}

// TestTruncateBody_ExactSize 测试刚好等于最大长度的情况
func TestTruncateBody_ExactSize(t *testing.T) {
	body := make([]byte, MaxBodySize)
	for i := range body {
		body[i] = 'x'
	}
	result, truncated := TruncateBody(string(body), MaxBodySize)
	if truncated {
		t.Error("刚好等于最大长度不应该被截断")
	}
	if len(result) != MaxBodySize {
		t.Errorf("结果长度 = %d, want %d", len(result), MaxBodySize)
	}
}

// TestTruncateBody_Truncation 测试需要截断的情况
func TestTruncateBody_Truncation(t *testing.T) {
	body := make([]byte, MaxBodySize+100)
	for i := range body {
		body[i] = 'x'
	}
	result, truncated := TruncateBody(string(body), MaxBodySize)
	if !truncated {
		t.Error("超过最大长度应该被截断")
	}
	if len(result) != MaxBodySize+len("[truncated]") {
		t.Errorf("结果长度 = %d, want %d", len(result), MaxBodySize+len("[truncated]"))
	}
}

// TestIsSensitiveHeader 测试敏感头部检测
func TestIsSensitiveHeader(t *testing.T) {
	tests := []struct {
		header   string
		expected bool
	}{
		{"Authorization", true},
		{"authorization", true},
		{"AUTHORIZATION", true},
		{"X-API-Key", true},
		{"x-api-key", true},
		{"Cookie", true},
		{"Content-Type", false},
		{"Accept", false},
		{"User-Agent", false},
		{"X-Request-ID", false},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			if got := isSensitiveHeader(tt.header); got != tt.expected {
				t.Errorf("isSensitiveHeader(%q) = %v, want %v", tt.header, got, tt.expected)
			}
		})
	}
}

// TestSanitizeHeaders 测试头部脱敏
func TestSanitizeHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer token123")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "secret-key")
	req.Header.Set("Accept", "*/*")

	result := sanitizeHeaders(req.Header)

	if result["Authorization"] != "[REDACTED]" {
		t.Errorf("Authorization 应该被脱敏")
	}
	if result["X-Api-Key"] != "[REDACTED]" {
		t.Errorf("X-Api-Key 应该被脱敏")
	}
	if result["Content-Type"] != "application/json" {
		t.Errorf("Content-Type 不应该被脱敏")
	}
	if result["Accept"] != "*/*" {
		t.Errorf("Accept 不应该被脱敏")
	}
}

// TestRecordError_Basic 测试基本错误记录（stdout 模式）
func TestRecordError_Basic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	body := `{"test": "data"}`
	req := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer token")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set(MsgIDKey, "test-msg-123")
	c.Set(RequestBodyKey, body)

	// 记录错误（输出到 stdout，不报错即可）
	testErr := errors.New("test error message")
	RecordErrorFromGin(c, logger, testErr, "account-456")
}

// TestRecordError_LargeBody 测试大请求体截断
func TestRecordError_LargeBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	// 创建超大请求体
	largeBody := make([]byte, MaxBodySize+1000)
	for i := range largeBody {
		largeBody[i] = 'x'
	}

	req := httptest.NewRequest("POST", "/api/test", bytes.NewBuffer(largeBody))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set(MsgIDKey, "test-large-body")
	c.Set(RequestBodyKey, string(largeBody))

	// 记录错误（输出到 stdout，不报错即可）
	testErr := errors.New("large body error")
	RecordErrorFromGin(c, logger, testErr, "")
}

// TestRecordError_NilError 测试 nil 错误
func TestRecordError_NilError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set(MsgIDKey, "test-nil-error")

	// nil 错误不应该 panic
	RecordErrorFromGin(c, logger, nil, "")
}

// TestRecordError_NoMsgID 测试没有 msgId 的情况
func TestRecordError_NoMsgID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	// 不设置 MsgIDKey

	// 没有 msgId 不应该 panic
	testErr := errors.New("no msgid error")
	RecordErrorFromGin(c, logger, testErr, "")
}

// TestMaxBodySize 测试最大请求体大小常量
func TestMaxBodySize(t *testing.T) {
	if MaxBodySize != 10*1024 {
		t.Errorf("MaxBodySize = %d, want %d", MaxBodySize, 10*1024)
	}
}

// TestSensitiveHeaders 测试敏感头部列表
func TestSensitiveHeaders(t *testing.T) {
	// 验证 SensitiveHeaders 包含必要的敏感 header
	expected := []string{
		"authorization",
		"x-api-key",
		"x-auth-token",
		"cookie",
		"set-cookie",
		"x-csrf-token",
		"x-access-token",
		"x-refresh-token",
	}
	if len(SensitiveHeaders) != len(expected) {
		t.Errorf("SensitiveHeaders 长度 = %d, want %d", len(SensitiveHeaders), len(expected))
	}
}

// TestRecordError_Integration 集成测试
func TestRecordError_Integration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建日志记录器失败: %v", err)
	}
	defer logger.Close()

	// 创建完整的测试场景
	body := `{"action": "test", "data": "integration"}`
	req := httptest.NewRequest("POST", "/api/error", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Request-Id", "integration-test-123")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set(MsgIDKey, "integration-test-123")
	c.Set(RequestBodyKey, body)

	// 模拟请求开始日志
	logger.Info("integration-test-123", "请求开始", map[string]any{
		"method":   "POST",
		"path":     "/api/error",
		"clientIP": c.ClientIP(),
		"query":    c.Request.URL.RawQuery,
	})

	// 记录错误
	testErr := errors.New("database connection failed")
	RecordErrorFromGin(c, logger, testErr, "acc-integration-test")

	// 模拟请求结束日志
	logger.Error("integration-test-123", "请求结束", map[string]any{
		"method":     "POST",
		"path":       "/api/error",
		"statusCode": 500,
		"duration":   "100ms",
		"durationMs": 100,
	})
}
