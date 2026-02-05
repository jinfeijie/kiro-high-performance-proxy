package main

import (
	"bytes"
	"io"
	"time"

	"github.com/gin-gonic/gin"
)

// ========== Context Key 常量 ==========

const (
	// MsgIDKey 请求唯一标识的 context key
	MsgIDKey = "msgId"
	// RequestBodyKey 请求体的 context key（用于错误记录）
	RequestBodyKey = "requestBody"
)

// ========== HTTP Header 常量 ==========

const (
	// HeaderXRequestID 客户端传入的请求ID header
	HeaderXRequestID = "X-Request-ID"
	// HeaderXMsgID 响应中返回的 msgId header
	HeaderXMsgID = "X-Msg-ID"
)

// ========== TraceMiddleware 请求追踪中间件 ==========

// TraceMiddleware 请求追踪中间件
// 功能：
// 1. 生成或获取 msgId（优先使用 X-Request-ID header）
// 2. 将 msgId 存入 Gin context
// 3. 保存请求体到 context（用于错误记录）
// 4. 记录请求开始日志
// 5. 在响应 header 中添加 X-Msg-ID
// 6. 记录请求结束日志（包含状态码和耗时）
func TraceMiddleware(logger *StructuredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

		// 1. 获取或生成 msgId
		// 优先使用客户端传入的 X-Request-ID，否则生成新的
		msgID := c.GetHeader(HeaderXRequestID)
		if msgID == "" {
			msgID = generateID("msg")
		}

		// 2. 将 msgId 存入 Gin context
		c.Set(MsgIDKey, msgID)

		// 3. 保存请求体到 context（用于错误记录）
		// 需要读取后重新设置，因为 Body 只能读取一次
		if c.Request.Body != nil {
			bodyBytes, err := io.ReadAll(c.Request.Body)
			if err == nil {
				// 重新设置 Body，供后续 handler 使用
				c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
				// 保存到 context
				c.Set(RequestBodyKey, string(bodyBytes))
			}
		}

		// 4. 请求开始日志已禁用（减少日志噪音）

		// 5. 在响应 header 中添加 X-Msg-ID
		// 在 c.Next() 之前设置，确保响应中包含
		c.Header(HeaderXMsgID, msgID)

		// 执行后续 handler
		c.Next()

		// 6. 请求结束日志已禁用（减少日志噪音）
		// 只在错误时记录
		statusCode := c.Writer.Status()
		if statusCode >= 400 {
			duration := time.Since(startTime)
			logData := map[string]any{
				"method":     c.Request.Method,
				"path":       c.Request.URL.Path,
				"statusCode": statusCode,
				"duration":   duration.String(),
				"durationMs": duration.Milliseconds(),
			}
			if statusCode >= 500 {
				logger.Error(msgID, "请求失败", logData)
			} else {
				logger.Warn(msgID, "请求失败", logData)
			}
		}
	}
}

// ========== 辅助函数 ==========

// GetMsgID 从 Gin context 获取 msgId
// 如果 context 中没有 msgId，返回 "unknown"
func GetMsgID(c *gin.Context) string {
	if msgID, exists := c.Get(MsgIDKey); exists {
		if id, ok := msgID.(string); ok {
			return id
		}
	}
	return "unknown"
}

// GetRequestBody 从 Gin context 获取请求体
// 如果 context 中没有请求体，返回空字符串
func GetRequestBody(c *gin.Context) string {
	if body, exists := c.Get(RequestBodyKey); exists {
		if b, ok := body.(string); ok {
			return b
		}
	}
	return ""
}
