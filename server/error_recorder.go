package main

import (
	"net/http"
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

// ========== ErrorRecordContext 错误记录上下文 ==========

// ErrorRecordContext 错误记录所需的请求上下文（框架无关）
// 从 HTTP 层提取的纯数据结构，不依赖任何 Web 框架
type ErrorRecordContext struct {
	MsgID     string      // 请求唯一标识
	Method    string      // HTTP 方法
	Path      string      // 请求路径
	ClientIP  string      // 客户端 IP
	Headers   http.Header // 原始请求头
	Body      string      // 请求体
	AccountID string      // 账户 ID（可选）
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

// RecordError 记录 500 错误的完整上下文（框架无关版本）
// 功能：
// 1. 接收纯数据结构而不是 gin.Context
// 2. 截断超过 10KB 的请求体并标记
// 3. 脱敏处理敏感 header
// 4. 记录完整错误上下文到日志
//
// 参数：
// - ctx: 错误记录上下文（纯数据结构）
// - logger: 结构化日志记录器
// - err: 错误对象
//
// 设计原则：
// - 职责分离：不依赖 Web 框架，可在任何场景使用
// - 数据驱动：接收纯数据结构，便于测试和复用
func RecordError(ctx ErrorRecordContext, logger *StructuredLogger, err error) {
	// 1. 处理请求体
	body := ctx.Body
	bodyTruncated := false

	// 截断超过 10KB 的请求体
	if len(body) > MaxBodySize {
		body = body[:MaxBodySize] + "[truncated]"
		bodyTruncated = true
	}

	// 2. 脱敏处理请求头
	headers := sanitizeHeaders(ctx.Headers)

	// 3. 获取错误信息
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	// 4. 构建错误上下文
	errCtx := ErrorContext{
		MsgID:      ctx.MsgID,
		Method:     ctx.Method,
		Path:       ctx.Path,
		ClientIP:   ctx.ClientIP,
		Headers:    headers,
		Body:       body,
		BodyTrunc:  bodyTruncated,
		AccountID:  ctx.AccountID,
		Error:      errMsg,
		StatusCode: 500,
	}

	// 5. 记录到日志
	logger.Error(ctx.MsgID, "500错误详情", map[string]any{
		"errorContext": errCtx,
	})
}

// RecordErrorFromGin 从 Gin context 提取数据并记录错误（适配器函数）
// 这是 Controller 层使用的便捷函数，负责从 gin.Context 提取数据
//
// 参数：
// - c: Gin context
// - logger: 结构化日志记录器
// - err: 错误对象
// - accountID: 账户 ID（可选，传空字符串表示无）
func RecordErrorFromGin(c *gin.Context, logger *StructuredLogger, err error, accountID string) {
	// 从 gin.Context 提取纯数据
	ctx := ErrorRecordContext{
		MsgID:     GetMsgID(c),
		Method:    c.Request.Method,
		Path:      c.Request.URL.Path,
		ClientIP:  c.ClientIP(),
		Headers:   c.Request.Header,
		Body:      GetRequestBody(c),
		AccountID: accountID,
	}

	// 调用框架无关的错误记录函数
	RecordError(ctx, logger, err)
}

// ========== 辅助函数 ==========

// sanitizeHeaders 脱敏处理请求头
// 敏感 header 的值将被替换为 "[REDACTED]"
func sanitizeHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)

	for key, values := range headers {
		// 合并多值 header
		value := strings.Join(values, ", ")

		// 检查是否为敏感 header（不区分大小写）
		if isSensitiveHeader(key) {
			value = "[REDACTED]"
		}

		result[key] = value
	}

	return result
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
