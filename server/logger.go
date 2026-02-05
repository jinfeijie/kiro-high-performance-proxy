package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ========== 日志级别定义 ==========

// LogLevel 日志级别
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

// String 返回日志级别的字符串表示
func (l LogLevel) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ========== 日志条目定义 ==========

// LogEntry 日志条目
type LogEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	MsgID     string         `json:"msgId"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
}

// ========== 结构化日志记录器 ==========

// StructuredLogger 结构化日志记录器
// 遵循 C 语言哲学：简单直接、数据结构透明、最小依赖
// 直接输出到 stdout，不写文件
type StructuredLogger struct {
	mutex sync.Mutex
	level LogLevel // 最低日志级别
}

// 默认配置常量
const (
	DefaultLogLevel = INFO // 默认 INFO 级别（线上推荐）
)

// ParseLogLevel 从字符串解析日志级别
func ParseLogLevel(s string) LogLevel {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return DEBUG
	case "INFO":
		return INFO
	case "WARN", "WARNING":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return INFO // 默认 INFO
	}
}

// GetLevel 获取当前日志级别
func (l *StructuredLogger) GetLevel() LogLevel {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return l.level
}

// NewStructuredLogger 创建日志记录器
// 参数已废弃，保留签名兼容性
func NewStructuredLogger(filePath string, maxSize int64) (*StructuredLogger, error) {
	logger := &StructuredLogger{
		level: DefaultLogLevel,
	}

	// 从环境变量读取日志级别（优先级最高）
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		logger.level = ParseLogLevel(envLevel)
	}

	return logger, nil
}

// SetLevel 设置最低日志级别
func (l *StructuredLogger) SetLevel(level LogLevel) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.level = level
}

// Log 记录日志（输出到 stdout）
// level: 日志级别
// msgID: 请求唯一标识（用于串联追踪）
// message: 日志消息
// data: 附加数据（可选）
func (l *StructuredLogger) Log(level LogLevel, msgID, message string, data map[string]any) {
	// 级别过滤
	if level < l.level {
		return
	}

	// 构建日志条目
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     level.String(),
		MsgID:     msgID,
		Message:   message,
		Data:      data,
	}

	// 序列化为 JSON
	jsonBytes, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[LOG ERROR] JSON序列化失败: %v\n", err)
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	// 输出到 stdout
	fmt.Println(string(jsonBytes))
}

// Debug 记录 DEBUG 级别日志
func (l *StructuredLogger) Debug(msgID, message string, data map[string]any) {
	l.Log(DEBUG, msgID, message, data)
}

// Info 记录 INFO 级别日志
func (l *StructuredLogger) Info(msgID, message string, data map[string]any) {
	l.Log(INFO, msgID, message, data)
}

// Warn 记录 WARN 级别日志
func (l *StructuredLogger) Warn(msgID, message string, data map[string]any) {
	l.Log(WARN, msgID, message, data)
}

// Error 记录 ERROR 级别日志
func (l *StructuredLogger) Error(msgID, message string, data map[string]any) {
	l.Log(ERROR, msgID, message, data)
}

// Close 关闭日志（无操作，保留接口兼容性）
func (l *StructuredLogger) Close() error {
	return nil
}

// QueryByMsgID 按 msgId 查询日志（stdout 模式不支持查询，返回空）
func (l *StructuredLogger) QueryByMsgID(msgID string) ([]LogEntry, error) {
	return []LogEntry{}, nil
}

// QueryByMsgIDPrefix 按 msgId 前缀查询日志（stdout 模式不支持查询，返回空）
func (l *StructuredLogger) QueryByMsgIDPrefix(prefix string) ([]LogEntry, error) {
	return []LogEntry{}, nil
}
