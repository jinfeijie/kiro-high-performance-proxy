package main

import (
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ========== 日志级别定义 ==========

// LogLevel 日志级别（保持对外接口兼容）
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	NONE // 关闭所有日志
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
	case NONE:
		return "NONE"
	default:
		return "UNKNOWN"
	}
}

// ========== 日志条目定义（保持兼容，用于查询接口） ==========

// LogEntry 日志条目（保持兼容性，QueryByMsgID 等接口使用）
type LogEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	MsgID     string         `json:"msgId"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
}

// ========== 结构化日志记录器（基于 zap） ==========

// StructuredLogger 结构化日志记录器
// 内部使用 uber-go/zap，对外保持原有接口不变
// zap.AddCaller() 自动记录调用者文件名和行号
type StructuredLogger struct {
	zap         *zap.Logger
	level       zap.AtomicLevel // 动态日志级别（控制正常日志）
	forceLogger *zap.Logger     // 独立 logger，固定 Debug 级别，ForceDebug 专用，不影响全局
}

// 默认配置常量
const (
	DefaultLogLevel = NONE // 默认关闭所有日志
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
	case "NONE", "OFF":
		return NONE
	default:
		return INFO
	}
}

// toZapLevel 将自定义 LogLevel 转为 zapcore.Level
func toZapLevel(l LogLevel) zapcore.Level {
	switch l {
	case DEBUG:
		return zapcore.DebugLevel
	case INFO:
		return zapcore.InfoLevel
	case WARN:
		return zapcore.WarnLevel
	case ERROR:
		return zapcore.ErrorLevel
	case NONE:
		// zap 没有 NONE，用 FatalLevel+1 使所有日志都被过滤
		return zapcore.FatalLevel + 1
	default:
		return zapcore.InfoLevel
	}
}

// NewStructuredLogger 创建日志记录器
// 参数已废弃，保留签名兼容性
func NewStructuredLogger(filePath string, maxSize int64) (*StructuredLogger, error) {
	// 确定初始日志级别
	initLevel := DefaultLogLevel
	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" {
		initLevel = ParseLogLevel(envLevel)
	}

	// 创建动态级别控制器
	atomicLevel := zap.NewAtomicLevelAt(toZapLevel(initLevel))

	// 自定义 encoder 配置：JSON 格式，带 caller 信息
	encoderCfg := zapcore.EncoderConfig{
		TimeKey:      "timestamp",
		LevelKey:     "level",
		CallerKey:    "caller",
		MessageKey:   "message",
		EncodeTime:   zapcore.ISO8601TimeEncoder,
		EncodeLevel:  zapcore.CapitalLevelEncoder,
		EncodeCaller: zapcore.ShortCallerEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		atomicLevel,
	)

	// AddCaller: 自动记录调用者文件名和行号
	// AddCallerSkip(1): 跳过 StructuredLogger 自身的包装层
	zapLogger := zap.New(core,
		zap.AddCaller(),
		zap.AddCallerSkip(1),
	)

	// 创建独立的 forceLogger，固定 Debug 级别
	// ForceDebug 专用，避免修改全局 level 导致并发竞态
	forceCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		zapcore.DebugLevel,
	)
	forceLogger := zap.New(forceCore,
		zap.AddCaller(),
		zap.AddCallerSkip(1),
	)

	return &StructuredLogger{
		zap:         zapLogger,
		level:       atomicLevel,
		forceLogger: forceLogger,
	}, nil
}

// GetLevel 获取当前日志级别
func (l *StructuredLogger) GetLevel() LogLevel {
	zapLvl := l.level.Level()
	switch {
	case zapLvl <= zapcore.DebugLevel:
		return DEBUG
	case zapLvl == zapcore.InfoLevel:
		return INFO
	case zapLvl == zapcore.WarnLevel:
		return WARN
	case zapLvl == zapcore.ErrorLevel:
		return ERROR
	default:
		return NONE
	}
}

// SetLevel 动态设置日志级别
func (l *StructuredLogger) SetLevel(level LogLevel) {
	l.level.SetLevel(toZapLevel(level))
}

// buildFields 将 msgID + data map 转为 zap.Field 切片
// 保持扁平化：msgId 作为顶层字段，data 中每个 key 也作为顶层字段
func buildFields(msgID string, data map[string]any) []zap.Field {
	fields := make([]zap.Field, 0, len(data)+1)
	fields = append(fields, zap.String("msgId", msgID))
	for k, v := range data {
		fields = append(fields, zap.Any(k, v))
	}
	return fields
}

// Debug 记录 DEBUG 级别日志
func (l *StructuredLogger) Debug(msgID, message string, data map[string]any) {
	l.zap.Debug(message, buildFields(msgID, data)...)
}

// Info 记录 INFO 级别日志
func (l *StructuredLogger) Info(msgID, message string, data map[string]any) {
	l.zap.Info(message, buildFields(msgID, data)...)
}

// Warn 记录 WARN 级别日志
func (l *StructuredLogger) Warn(msgID, message string, data map[string]any) {
	l.zap.Warn(message, buildFields(msgID, data)...)
}

// Error 记录 ERROR 级别日志
func (l *StructuredLogger) Error(msgID, message string, data map[string]any) {
	l.zap.Error(message, buildFields(msgID, data)...)
}

// ForceDebug 强制输出 DEBUG 日志，无视当前日志级别
// 用于 OneDayAI_Start_Debug 模式，即使 level=NONE 也能输出
// 使用独立的 forceLogger，不修改全局 level，避免并发竞态
func (l *StructuredLogger) ForceDebug(msgID, message string, data map[string]any) {
	l.forceLogger.Debug(message, buildFields(msgID, data)...)
}

// Close 关闭日志（刷新 zap 缓冲区）
func (l *StructuredLogger) Close() error {
	// Sync 刷新所有缓冲的日志条目
	// stdout 的 Sync 在某些系统上会返回 error，忽略即可
	_ = l.zap.Sync()
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
