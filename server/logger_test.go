package main

import (
	"os"
	"testing"
)

// ========== NewStructuredLogger 测试 ==========

func TestNewStructuredLogger(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("NewStructuredLogger() error = %v", err)
	}
	defer logger.Close()

	if logger.zap == nil {
		t.Fatal("zap logger 不应为 nil")
	}
}

func TestNewStructuredLogger_DefaultLevel(t *testing.T) {
	// 默认 NONE
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	if logger.GetLevel() != NONE {
		t.Errorf("默认级别应为 NONE, got %v", logger.GetLevel())
	}
}

func TestNewStructuredLogger_EnvLevel(t *testing.T) {
	os.Setenv("LOG_LEVEL", "DEBUG")
	defer os.Unsetenv("LOG_LEVEL")

	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	if logger.GetLevel() != DEBUG {
		t.Errorf("环境变量 DEBUG, got %v", logger.GetLevel())
	}
}

// ========== GetLevel / SetLevel 测试 ==========

func TestStructuredLogger_GetLevel(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	// 默认 NONE
	if logger.GetLevel() != NONE {
		t.Errorf("expected NONE, got %v", logger.GetLevel())
	}

	// 设置为 DEBUG
	logger.SetLevel(DEBUG)
	if logger.GetLevel() != DEBUG {
		t.Errorf("expected DEBUG, got %v", logger.GetLevel())
	}
}

func TestStructuredLogger_SetLevel_AllLevels(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	tests := []LogLevel{DEBUG, INFO, WARN, ERROR, NONE}
	for _, lvl := range tests {
		logger.SetLevel(lvl)
		got := logger.GetLevel()
		if got != lvl {
			t.Errorf("SetLevel(%v): got %v", lvl, got)
		}
	}
}

// ========== Log 方法测试（不 panic） ==========

func TestStructuredLogger_Log(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()
	logger.SetLevel(DEBUG)

	msgID := "test-msg-001"
	message := "测试日志"
	data := map[string]any{"key": "value", "count": 42}

	// 所有级别都不应 panic
	logger.Debug(msgID, message, data)
	logger.Info(msgID, message, data)
	logger.Warn(msgID, message, data)
	logger.Error(msgID, message, data)
}

func TestStructuredLogger_LevelFilter(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	// 设置为 ERROR，DEBUG/INFO/WARN 应被过滤（不 panic 即可）
	logger.SetLevel(ERROR)
	logger.Debug("msg_1", "debug message", nil)
	logger.Info("msg_2", "info message", nil)
	logger.Warn("msg_3", "warn message", nil)
	logger.Error("msg_4", "error message", nil)
}

func TestStructuredLogger_NilData(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()
	logger.SetLevel(DEBUG)

	// nil data 不应 panic
	logger.Debug("msg_test", "nil data 测试", nil)
}

func TestStructuredLogger_EmptyData(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()
	logger.SetLevel(DEBUG)

	// 空 map 不应 panic
	logger.Debug("msg_test", "empty data", map[string]any{})
}

// ========== Query 方法测试（stdout 模式返回空） ==========

func TestStructuredLogger_QueryByMsgID(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	entries, err := logger.QueryByMsgID("test-id")
	if err != nil {
		t.Fatalf("QueryByMsgID error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestStructuredLogger_QueryByMsgIDPrefix(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	entries, err := logger.QueryByMsgIDPrefix("test")
	if err != nil {
		t.Fatalf("QueryByMsgIDPrefix error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// ========== ParseLogLevel 测试 ==========

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected LogLevel
	}{
		{"DEBUG", DEBUG},
		{"debug", DEBUG},
		{"INFO", INFO},
		{"WARN", WARN},
		{"WARNING", WARN},
		{"ERROR", ERROR},
		{"NONE", NONE},
		{"OFF", NONE},
		{"unknown", INFO},
		{"", INFO},
		{"  DEBUG  ", DEBUG},
	}
	for _, tt := range tests {
		got := ParseLogLevel(tt.input)
		if got != tt.expected {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}

// ========== LogLevel.String 测试 ==========

func TestLogLevel_String(t *testing.T) {
	tests := []struct {
		level    LogLevel
		expected string
	}{
		{DEBUG, "DEBUG"},
		{INFO, "INFO"},
		{WARN, "WARN"},
		{ERROR, "ERROR"},
		{NONE, "NONE"},
		{LogLevel(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.expected {
			t.Errorf("LogLevel(%d).String() = %q, want %q", tt.level, got, tt.expected)
		}
	}
}

// ========== Close 测试 ==========

func TestStructuredLogger_Close(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	// Close 不应 panic 或返回 error
	if err := logger.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// ========== 复杂数据类型测试 ==========

func TestStructuredLogger_ComplexData(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()
	logger.SetLevel(DEBUG)

	// 嵌套 map、slice、各种类型都不应 panic
	data := map[string]any{
		"string": "hello",
		"int":    42,
		"float":  3.14,
		"bool":   true,
		"nil":    nil,
		"slice":  []string{"a", "b"},
		"nested": map[string]any{"inner": "value"},
		"bytes":  []byte("hello bytes"),
	}
	logger.Debug("msg_complex", "复杂数据测试", data)
}

// ========== buildFields 测试 ==========

func TestBuildFields(t *testing.T) {
	fields := buildFields("msg-123", map[string]any{
		"key1": "val1",
		"key2": 42,
	})
	// msgId + 2 data fields = 3
	if len(fields) != 3 {
		t.Errorf("expected 3 fields, got %d", len(fields))
	}
}

func TestBuildFields_NilData(t *testing.T) {
	fields := buildFields("msg-123", nil)
	// 只有 msgId
	if len(fields) != 1 {
		t.Errorf("expected 1 field, got %d", len(fields))
	}
}

// ========== toZapLevel 测试 ==========

func TestToZapLevel(t *testing.T) {
	// 确保所有级别都有映射，不 panic
	levels := []LogLevel{DEBUG, INFO, WARN, ERROR, NONE, LogLevel(99)}
	for _, l := range levels {
		_ = toZapLevel(l)
	}
}

// ========== ForceDebug 测试 ==========

func TestStructuredLogger_ForceDebug_NotPanic(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	// 即使级别是 NONE，ForceDebug 也不应 panic
	logger.SetLevel(NONE)
	logger.ForceDebug("msg-fd-1", "强制debug测试", map[string]any{"key": "val"})
}

func TestStructuredLogger_ForceDebug_RestoresLevel(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	// 设置为 ERROR，调用 ForceDebug 后级别应恢复为 ERROR
	logger.SetLevel(ERROR)
	logger.ForceDebug("msg-fd-2", "测试级别恢复", nil)

	if logger.GetLevel() != ERROR {
		t.Errorf("ForceDebug 后级别应恢复为 ERROR, got %v", logger.GetLevel())
	}
}

func TestStructuredLogger_ForceDebug_AllLevels(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()

	// 每个级别都测一遍，确保 ForceDebug 后级别恢复
	levels := []LogLevel{DEBUG, INFO, WARN, ERROR, NONE}
	for _, lvl := range levels {
		logger.SetLevel(lvl)
		logger.ForceDebug("msg-fd", "test", map[string]any{"level": lvl.String()})
		if logger.GetLevel() != lvl {
			t.Errorf("ForceDebug 后级别应恢复为 %v, got %v", lvl, logger.GetLevel())
		}
	}
}

func TestStructuredLogger_ForceDebug_NilData(t *testing.T) {
	logger, err := NewStructuredLogger("", 0)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	defer logger.Close()
	logger.SetLevel(NONE)

	// nil data 不应 panic
	logger.ForceDebug("msg-fd-nil", "nil data", nil)
}
