package main

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// ========== 常量定义 ==========

// MaxBodySize 请求体最大记录大小（10KB）
// 超过此大小的请求体将被截断并标记
const MaxBodySize = 10 * 1024

// SensitiveHeaders 需要脱敏处理的敏感 header 列表
// 这些 header 的值将被替换为 "[REDACTED]"
var SensitiveHeaders = []string{
	"authorization",
	"x-api-key",
	"x-auth-token",
	"cookie",
	"set-cookie",
	"x-csrf-token",
	"x-access-token",
	"x-refresh-token",
}

// ========== ErrorContext 错误上下文结构体 ==========

// ErrorContext 500 错误的完整上下文信息
// 用于记录错误发生时的请求详情，便于问题排查
type ErrorContext struct {
	MsgID      string            `json:"msgId"`               // 请求唯一标识
	Method     string            `json:"method"`              // HTTP 方法
	Path       string            `json:"path"`                // 请求路径
	ClientIP   string            `json:"clientIP"`            // 客户端 IP
	Headers    map[string]string `json:"headers"`             // 请求头（已脱敏）
	Body       string            `json:"body"`                // 请求体
	BodyTrunc  bool              `json:"bodyTruncated"`       // 请求体是否被截断
	AccountID  string            `json:"accountId,omitempty"` // 账户 ID（可选）
	Error      string            `json:"error"`               // 错误信息
	StatusCode int               `json:"statusCode"`          // HTTP 状态码
}

// ========== RecordError 错误记录函数 ==========

// RecordError 记录 500 错误的完整上下文
// 功能：
// 1. 从 Gin context 获取 msgId 和请求体
// 2. 截断超过 10KB 的请求体并标记
// 3. 脱敏处理敏感 header
// 4. 记录完整错误上下文到日志
//
// 参数：
// - c: Gin context
// - logger: 结构化日志记录器
// - err: 错误对象
// - accountID: 账户 ID（可选，传空字符串表示无）
func RecordError(c *gin.Context, logger *StructuredLogger, err error, accountID string) {
	// 1. 获取 msgId
	msgID := GetMsgID(c)

	// 2. 获取并处理请求体
	body := GetRequestBody(c)
	bodyTruncated := false

	// 截断超过 10KB 的请求体
	if len(body) > MaxBodySize {
		body = body[:MaxBodySize] + "[truncated]"
		bodyTruncated = true
	}

	// 3. 获取并脱敏处理请求头
	headers := sanitizeHeaders(c)

	// 4. 获取错误信息
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	// 5. 构建错误上下文
	errCtx := ErrorContext{
		MsgID:      msgID,
		Method:     c.Request.Method,
		Path:       c.Request.URL.Path,
		ClientIP:   c.ClientIP(),
		Headers:    headers,
		Body:       body,
		BodyTrunc:  bodyTruncated,
		AccountID:  accountID,
		Error:      errMsg,
		StatusCode: 500,
	}

	// 6. 记录到日志
	logger.Error(msgID, "500错误详情", map[string]any{
		"errorContext": errCtx,
	})
}

// ========== 辅助函数 ==========

// sanitizeHeaders 获取并脱敏处理请求头
// 敏感 header 的值将被替换为 "[REDACTED]"
func sanitizeHeaders(c *gin.Context) map[string]string {
	headers := make(map[string]string)

	for key, values := range c.Request.Header {
		// 合并多值 header
		value := strings.Join(values, ", ")

		// 检查是否为敏感 header（不区分大小写）
		if isSensitiveHeader(key) {
			value = "[REDACTED]"
		}

		headers[key] = value
	}

	return headers
}

// isSensitiveHeader 检查是否为敏感 header
// 不区分大小写进行匹配
func isSensitiveHeader(key string) bool {
	lowerKey := strings.ToLower(key)
	for _, sensitive := range SensitiveHeaders {
		if lowerKey == sensitive {
			return true
		}
	}
	return false
}

// TruncateBody 截断请求体（导出函数，供测试使用）
// 如果请求体超过 maxSize，截断并添加 "[truncated]" 标记
// 返回：截断后的请求体和是否被截断的标志
func TruncateBody(body string, maxSize int) (string, bool) {
	if len(body) <= maxSize {
		return body, false
	}
	return body[:maxSize] + "[truncated]", true
}
