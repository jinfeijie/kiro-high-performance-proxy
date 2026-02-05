package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestLogLevel_String 测试日志级别字符串转换
func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{DEBUG, "DEBUG"},
		{INFO, "INFO"},
		{WARN, "WARN"},
		{ERROR, "ERROR"},
		{LogLevel(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.expected {
			t.Errorf("LogLevel(%d).String() = %s, want %s", tt.level, got, tt.expected)
		}
	}
}

// TestParseLogLevel 测试从字符串解析日志级别
func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected LogLevel
	}{
		{"DEBUG", DEBUG},
		{"debug", DEBUG},
		{"Debug", DEBUG},
		{"INFO", INFO},
		{"info", INFO},
		{"WARN", WARN},
		{"warn", WARN},
		{"WARNING", WARN},
		{"warning", WARN},
		{"ERROR", ERROR},
		{"error", ERROR},
		{"", INFO},
		{"invalid", INFO},
		{"  INFO  ", INFO},
	}

	for _, tt := range tests {
		if got := ParseLogLevel(tt.input); got != tt.expected {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// TestStructuredLogger_GetLevel 测试获取日志级别
func TestStructuredLogger_GetLevel(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	// 默认级别应该是 INFO
	if got := logger.GetLevel(); got != INFO {
		t.Errorf("GetLevel() = %v, want %v", got, INFO)
	}

	// 设置为 ERROR
	logger.SetLevel(ERROR)
	if got := logger.GetLevel(); got != ERROR {
		t.Errorf("GetLevel() after SetLevel(ERROR) = %v, want %v", got, ERROR)
	}

	// 设置为 DEBUG
	logger.SetLevel(DEBUG)
	if got := logger.GetLevel(); got != DEBUG {
		t.Errorf("GetLevel() after SetLevel(DEBUG) = %v, want %v", got, DEBUG)
	}
}

// TestNewStructuredLogger 测试创建日志记录器
func TestNewStructuredLogger(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	if logger == nil {
		t.Error("logger 不应为 nil")
	}
}

// TestStructuredLogger_Log 测试日志记录（输出到 stdout）
func TestStructuredLogger_Log(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	// 设置为 DEBUG 以确保所有日志都输出
	logger.SetLevel(DEBUG)

	// 记录日志（输出到 stdout，不会报错即可）
	msgID := "msg_test_123"
	message := "测试日志消息"
	data := map[string]any{"key": "value", "count": 42}

	logger.Debug(msgID, message, data)
	logger.Info(msgID, message, data)
	logger.Warn(msgID, message, data)
	logger.Error(msgID, message, data)
}

// TestStructuredLogger_LevelFilter 测试日志级别过滤
func TestStructuredLogger_LevelFilter(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	// 设置最低级别为 ERROR
	logger.SetLevel(ERROR)

	// DEBUG/INFO/WARN 应该被过滤（不输出）
	// ERROR 应该输出
	logger.Debug("msg_1", "debug message", nil)
	logger.Info("msg_2", "info message", nil)
	logger.Warn("msg_3", "warn message", nil)
	logger.Error("msg_4", "error message", nil)
}

// TestStructuredLogger_QueryByMsgID 测试按 msgId 查询（stdout 模式返回空）
func TestStructuredLogger_QueryByMsgID(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	// stdout 模式不支持查询，应返回空数组
	entries, err := logger.QueryByMsgID("msg_aaa")
	if err != nil {
		t.Fatalf("QueryByMsgID() error = %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("查询结果数量 = %d, want 0", len(entries))
	}
}

// TestStructuredLogger_QueryByMsgIDPrefix 测试前缀查询（stdout 模式返回空）
func TestStructuredLogger_QueryByMsgIDPrefix(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	// stdout 模式不支持查询，应返回空数组
	entries, err := logger.QueryByMsgIDPrefix("msg_")
	if err != nil {
		t.Fatalf("QueryByMsgIDPrefix() error = %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("查询结果数量 = %d, want 0", len(entries))
	}
}

// TestLogEntry_JSONRoundTrip 测试 LogEntry JSON 序列化/反序列化
func TestLogEntry_JSONRoundTrip(t *testing.T) {
	original := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Level:     "INFO",
		MsgID:     "msg_test_roundtrip",
		Message:   "测试消息",
		Data: map[string]any{
			"string": "value",
			"number": float64(42),
			"bool":   true,
		},
	}

	// 序列化
	jsonBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("JSON序列化失败: %v", err)
	}

	// 反序列化
	var parsed LogEntry
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		t.Fatalf("JSON反序列化失败: %v", err)
	}

	// 验证字段
	if parsed.Timestamp != original.Timestamp {
		t.Errorf("Timestamp = %s, want %s", parsed.Timestamp, original.Timestamp)
	}
	if parsed.Level != original.Level {
		t.Errorf("Level = %s, want %s", parsed.Level, original.Level)
	}
	if parsed.MsgID != original.MsgID {
		t.Errorf("MsgID = %s, want %s", parsed.MsgID, original.MsgID)
	}
	if parsed.Message != original.Message {
		t.Errorf("Message = %s, want %s", parsed.Message, original.Message)
	}
}
