package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// setupTestLogger 创建测试用的 logger（stdout 模式）
func setupTestLogger(t *testing.T) *StructuredLogger {
	t.Helper()
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("创建测试 logger 失败: %v", err)
	}
	return logger
}

// TestTraceMiddleware_GeneratesMsgID 测试中间件生成 msgId
func TestTraceMiddleware_GeneratesMsgID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	var capturedMsgID string
	router.GET("/test", func(c *gin.Context) {
		capturedMsgID = GetMsgID(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证 msgId 已生成
	if capturedMsgID == "" || capturedMsgID == "unknown" {
		t.Errorf("期望生成有效的 msgId，实际得到: %s", capturedMsgID)
	}

	// 验证 msgId 格式（msg_时间戳_随机数）
	if !strings.HasPrefix(capturedMsgID, "msg_") {
		t.Errorf("msgId 格式错误，期望以 'msg_' 开头，实际: %s", capturedMsgID)
	}

	// 验证响应 header 包含 X-Msg-ID
	responseMsgID := w.Header().Get(HeaderXMsgID)
	if responseMsgID != capturedMsgID {
		t.Errorf("响应 header X-Msg-ID 不匹配，期望: %s，实际: %s", capturedMsgID, responseMsgID)
	}
}

// TestTraceMiddleware_UsesXRequestID 测试中间件使用 X-Request-ID
func TestTraceMiddleware_UsesXRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	customRequestID := "custom-request-id-12345"
	var capturedMsgID string
	router.GET("/test", func(c *gin.Context) {
		capturedMsgID = GetMsgID(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set(HeaderXRequestID, customRequestID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证使用了客户端传入的 X-Request-ID
	if capturedMsgID != customRequestID {
		t.Errorf("期望使用 X-Request-ID: %s，实际: %s", customRequestID, capturedMsgID)
	}

	// 验证响应 header 包含相同的 X-Msg-ID
	responseMsgID := w.Header().Get(HeaderXMsgID)
	if responseMsgID != customRequestID {
		t.Errorf("响应 header X-Msg-ID 不匹配，期望: %s，实际: %s", customRequestID, responseMsgID)
	}
}

// TestTraceMiddleware_SavesRequestBody 测试中间件保存请求体
func TestTraceMiddleware_SavesRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	requestBody := `{"name":"test","value":123}`
	var capturedBody string
	router.POST("/test", func(c *gin.Context) {
		capturedBody = GetRequestBody(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("POST", "/test", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证请求体已保存到 context
	if capturedBody != requestBody {
		t.Errorf("请求体不匹配，期望: %s，实际: %s", requestBody, capturedBody)
	}
}

// TestTraceMiddleware_LogsRequestLifecycle 测试中间件记录请求生命周期
// 注意：stdout 模式下无法验证日志内容，仅验证中间件正常执行
func TestTraceMiddleware_LogsRequestLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	var capturedMsgID string
	router.GET("/test/path", func(c *gin.Context) {
		capturedMsgID = GetMsgID(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test/path?foo=bar", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证请求正常完成
	if w.Code != 200 {
		t.Errorf("期望状态码 200，实际: %d", w.Code)
	}

	// 验证 msgId 已生成
	if capturedMsgID == "" || capturedMsgID == "unknown" {
		t.Errorf("期望生成有效的 msgId，实际: %s", capturedMsgID)
	}

	// 验证响应 header
	if w.Header().Get(HeaderXMsgID) == "" {
		t.Error("响应 header 中缺少 X-Msg-ID")
	}
}

// TestTraceMiddleware_ResponseHeader 测试响应 header 包含 X-Msg-ID
func TestTraceMiddleware_ResponseHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	router.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证响应 header 包含 X-Msg-ID
	responseMsgID := w.Header().Get(HeaderXMsgID)
	if responseMsgID == "" {
		t.Error("响应 header 中缺少 X-Msg-ID")
	}

	// 验证 msgId 格式
	if !strings.HasPrefix(responseMsgID, "msg_") {
		t.Errorf("X-Msg-ID 格式错误，期望以 'msg_' 开头，实际: %s", responseMsgID)
	}
}

// TestGetMsgID_ReturnsUnknownWhenNotSet 测试 GetMsgID 在未设置时返回 unknown
func TestGetMsgID_ReturnsUnknownWhenNotSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	var capturedMsgID string
	router.GET("/test", func(c *gin.Context) {
		capturedMsgID = GetMsgID(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证返回 unknown
	if capturedMsgID != "unknown" {
		t.Errorf("期望返回 'unknown'，实际: %s", capturedMsgID)
	}
}

// TestGetRequestBody_ReturnsEmptyWhenNotSet 测试 GetRequestBody 在未设置时返回空字符串
func TestGetRequestBody_ReturnsEmptyWhenNotSet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	var capturedBody string
	router.GET("/test", func(c *gin.Context) {
		capturedBody = GetRequestBody(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证返回空字符串
	if capturedBody != "" {
		t.Errorf("期望返回空字符串，实际: %s", capturedBody)
	}
}

// TestTraceMiddleware_StatusCodeLogging 测试不同状态码的处理
// 注意：stdout 模式下无法验证日志级别，仅验证中间件正常处理各种状态码
func TestTraceMiddleware_StatusCodeLogging(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{"200 OK", http.StatusOK},
		{"201 Created", http.StatusCreated},
		{"400 Bad Request", http.StatusBadRequest},
		{"404 Not Found", http.StatusNotFound},
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"502 Bad Gateway", http.StatusBadGateway},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			logger := setupTestLogger(t)

			router := gin.New()
			router.Use(TraceMiddleware(logger))

			router.GET("/test", func(c *gin.Context) {
				c.JSON(tc.statusCode, gin.H{"status": "test"})
			})

			req := httptest.NewRequest("GET", "/test", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// 验证状态码正确返回
			if w.Code != tc.statusCode {
				t.Errorf("期望状态码 %d，实际: %d", tc.statusCode, w.Code)
			}

			// 验证响应 header 包含 X-Msg-ID
			if w.Header().Get(HeaderXMsgID) == "" {
				t.Error("响应 header 中缺少 X-Msg-ID")
			}
		})
	}
}

// TestTraceMiddleware_MsgIDUniqueness 测试 msgId 唯一性
func TestTraceMiddleware_MsgIDUniqueness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := setupTestLogger(t)

	router := gin.New()
	router.Use(TraceMiddleware(logger))

	msgIDs := make(map[string]bool)
	mu := make(chan string, 100)

	router.GET("/test", func(c *gin.Context) {
		mu <- GetMsgID(c)
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 发送 100 个请求
	for i := range 100 {
		_ = i
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}

	close(mu)

	// 收集所有 msgId
	for msgID := range mu {
		if msgIDs[msgID] {
			t.Errorf("发现重复的 msgId: %s", msgID)
		}
		msgIDs[msgID] = true
	}

	// 验证生成了 100 个唯一的 msgId
	if len(msgIDs) != 100 {
		t.Errorf("期望 100 个唯一 msgId，实际: %d", len(msgIDs))
	}
}
