package kiroclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const RequestBodyKey = "requestBody"

// TraceLogger 日志接口（避免 server 包和 kiroclient 包循环依赖）
type TraceLogger interface {
	Debug(msgId, message string, data map[string]any)
	Info(msgId, message string, data map[string]any)
	Warn(msgId, message string, data map[string]any)
	Error(msgId, message string, data map[string]any)
	ForceDebug(msgId, message string, data map[string]any)
}

// DebugModeKey context key，用于标记当前请求是否开启了 debug 模式
// 当消息中包含 OneDayAI_Start_Debug 关键字时，设置为 true
const DebugModeKey = "debugMode"

// IsDebugMode 从 context 中判断是否开启了 debug 模式
// 导出给 server 包使用
func IsDebugMode(ctx context.Context) bool {
	if v := ctx.Value(DebugModeKey); v != nil {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// GetMsgIdFromCtx 从 context 中获取 msgId（导出版本）
// 导出给 server 包使用，避免重复实现
func GetMsgIdFromCtx(ctx context.Context) string {
	return getMsgIdFromCtx(ctx)
}

// DebugLog 统一的 debug 日志封装
// 如果 debug 模式开启，用 ForceDebug 强制输出；否则走正常 Debug 级别
// 导出给 server 包使用，两个包共用同一套逻辑
func DebugLog(ctx context.Context, logger TraceLogger, msg string, data map[string]any) {
	if logger == nil {
		return
	}
	msgId := getMsgIdFromCtx(ctx)
	if IsDebugMode(ctx) {
		logger.ForceDebug(msgId, msg, data)
		return
	}
	logger.Debug(msgId, msg, data)
}

// TruncationType 截断类型
// 用于标识 JSON 字符串被截断的方式，便于后续修复处理
type TruncationType int

const (
	TruncationNone    TruncationType = iota // 非截断（完整或语法错误）
	TruncationBracket                       // 缺少闭合括号/花括号
	TruncationString                        // 字符串值未闭合
	TruncationNumber                        // 数字值不完整
	TruncationKey                           // 键名不完整
	TruncationColon                         // 冒号后无值
)

// String 返回截断类型的字符串表示，便于调试和日志
func (t TruncationType) String() string {
	switch t {
	case TruncationNone:
		return "none"
	case TruncationBracket:
		return "bracket"
	case TruncationString:
		return "string"
	case TruncationNumber:
		return "number"
	case TruncationKey:
		return "key"
	case TruncationColon:
		return "colon"
	default:
		return "unknown"
	}
}

// detectTruncation 检测 JSON 截断类型
// 返回截断类型和截断位置
// 设计原则：使用栈跟踪括号嵌套，跟踪字符串状态，检测不完整的数字
// 区分截断（可修复）和语法错误（不可修复）
func detectTruncation(s string) (TruncationType, int) {
	if s == "" {
		return TruncationNone, 0
	}

	// 去除首尾空白
	s = strings.TrimSpace(s)
	if s == "" {
		return TruncationNone, 0
	}

	n := len(s)

	// 状态跟踪
	var bracketStack []byte // 括号栈：存储 '{' 或 '['
	inString := false       // 是否在字符串内部
	escaped := false        // 前一个字符是否是转义符 '\'
	lastTokenType := 0      // 上一个 token 类型：0=无, 1=key, 2=colon, 3=value, 4=comma
	valueStart := -1        // 当前值的起始位置

	for i := 0; i < n; i++ {
		c := s[i]

		// 处理转义字符
		if escaped {
			escaped = false
			continue
		}

		// 在字符串内部
		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
				lastTokenType = 3 // value
				valueStart = -1
			}
			continue
		}

		// 不在字符串内部
		switch c {
		case '"':
			inString = true
			if lastTokenType == 2 { // 冒号后面
				valueStart = i
				lastTokenType = 3
			} else if lastTokenType == 0 || lastTokenType == 4 || lastTokenType == 5 { // 开始或逗号后或左括号后
				// 可能是 key
				if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '{' {
					lastTokenType = 1 // key
				} else {
					lastTokenType = 3 // 数组中的字符串值
				}
				valueStart = i
			}

		case ':':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '{' {
				lastTokenType = 2 // colon
			}

		case ',':
			lastTokenType = 4 // comma
			valueStart = -1

		case '{', '[':
			bracketStack = append(bracketStack, c)
			lastTokenType = 5 // 左括号
			valueStart = -1

		case '}', ']':
			if len(bracketStack) == 0 {
				// 多余的闭合括号 - 语法错误
				return TruncationNone, i
			}
			expected := byte('{')
			if c == ']' {
				expected = '['
			}
			if bracketStack[len(bracketStack)-1] != expected {
				// 括号不匹配 - 语法错误
				return TruncationNone, i
			}
			bracketStack = bracketStack[:len(bracketStack)-1]
			lastTokenType = 3 // value
			valueStart = -1

		case ' ', '\t', '\n', '\r':
			// 跳过空白字符
			continue

		default:
			// 数字、布尔值、null
			if lastTokenType == 2 || lastTokenType == 4 || lastTokenType == 5 || lastTokenType == 0 {
				// 冒号后、逗号后、左括号后、开始位置
				if valueStart == -1 {
					valueStart = i
				}
				lastTokenType = 3
			}
		}
	}

	// 分析结束状态，判断截断类型

	// 1. 字符串未闭合
	if inString {
		return TruncationString, valueStart
	}

	// 2. 检查是否有未闭合的括号
	if len(bracketStack) > 0 {
		// 检查最后的 token 状态
		lastNonSpace := findLastNonSpace(s)
		if lastNonSpace >= 0 {
			lastChar := s[lastNonSpace]

			// 冒号后无值
			if lastChar == ':' {
				return TruncationColon, lastNonSpace
			}

			// 逗号后可能是不完整的 key
			if lastChar == ',' {
				return TruncationBracket, n
			}

			// 检查是否是不完整的数字
			if isIncompleteNumber(s, lastNonSpace) {
				return TruncationNumber, findNumberStart(s, lastNonSpace)
			}

			// 检查是否是不完整的 key（在对象中，逗号后的字符串）
			if lastTokenType == 1 && !inString {
				// key 后面没有冒号
				return TruncationKey, valueStart
			}
		}

		return TruncationBracket, n
	}

	// 3. 括号已闭合，检查是否是完整的 JSON
	// 尝试解析，如果成功则是完整的 JSON
	return TruncationNone, 0
}

// findLastNonSpace 找到最后一个非空白字符的位置
func findLastNonSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return i
		}
	}
	return -1
}

// isIncompleteNumber 检查是否是不完整的数字
// 不完整的数字：以 '.', 'e', 'E', '-', '+' 结尾，或者只有负号
func isIncompleteNumber(s string, pos int) bool {
	if pos < 0 || pos >= len(s) {
		return false
	}

	c := s[pos]

	// 以这些字符结尾表示数字不完整
	if c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+' {
		// 确认前面是数字的一部分
		if pos == 0 {
			return c == '-' || c == '+' // 只有符号
		}

		// 向前查找，确认是数字上下文
		for i := pos - 1; i >= 0; i-- {
			pc := s[i]
			if pc >= '0' && pc <= '9' {
				return true
			}
			if pc == '.' || pc == 'e' || pc == 'E' || pc == '-' || pc == '+' {
				continue
			}
			if pc == ' ' || pc == '\t' || pc == '\n' || pc == '\r' {
				continue
			}
			// 遇到其他字符，检查是否是数字开始的上下文
			if pc == ':' || pc == ',' || pc == '[' || pc == '{' {
				return true
			}
			break
		}
	}

	return false
}

// findNumberStart 找到数字的起始位置
func findNumberStart(s string, pos int) int {
	start := pos
	for i := pos; i >= 0; i-- {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+' {
			start = i
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		break
	}
	return start
}

// fixTruncatedJSON 尝试修复截断的 JSON
// 返回修复后的字符串和是否成功
// 设计原则：根据截断类型应用不同的修复策略，修复后验证 JSON 是否有效
func fixTruncatedJSON(s string, truncType TruncationType) (string, bool) {
	if s == "" {
		return "{}", true
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return "{}", true
	}

	var fixed string

	switch truncType {
	case TruncationNone:
		// 非截断情况，直接返回原字符串
		fixed = s

	case TruncationBracket:
		// 补全缺失的闭合符号
		fixed = fixBrackets(s)

	case TruncationString:
		// 闭合字符串并补全括号
		fixed = fixTruncatedString(s)

	case TruncationNumber:
		// 移除不完整的数字部分，然后补全括号
		fixed = fixTruncatedNumber(s)

	case TruncationKey:
		// 移除不完整的键并补全
		fixed = fixTruncatedKey(s)

	case TruncationColon:
		// 移除不完整的键值对并补全
		fixed = fixTruncatedColon(s)

	default:
		return s, false
	}

	// 验证修复后的 JSON 是否有效
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(fixed), &result); err != nil {
		// 修复失败，尝试更激进的修复
		fixed = aggressiveFix(s)
		if err := json.Unmarshal([]byte(fixed), &result); err != nil {
			return s, false
		}
	}

	return fixed, true
}

// fixBrackets 补全缺失的闭合括号
// 分析括号栈，按逆序补全缺失的 } 和 ]
func fixBrackets(s string) string {
	var bracketStack []byte
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			bracketStack = append(bracketStack, '{')
		case '[':
			bracketStack = append(bracketStack, '[')
		case '}':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '{' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		case ']':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '[' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		}
	}

	// 按逆序补全缺失的闭合符号
	result := s
	for i := len(bracketStack) - 1; i >= 0; i-- {
		if bracketStack[i] == '{' {
			result += "}"
		} else {
			result += "]"
		}
	}

	return result
}

// fixTruncatedString 修复截断的字符串
// 闭合字符串并补全括号
func fixTruncatedString(s string) string {
	// 添加闭合引号
	result := s + `"`

	// 然后补全括号
	return fixBrackets(result)
}

// fixTruncatedNumber 修复截断的数字
// 移除不完整的数字部分（如 '.', 'e', 'E', '-', '+'），然后补全括号
func fixTruncatedNumber(s string) string {
	// 找到最后一个非空白字符
	lastPos := findLastNonSpace(s)
	if lastPos < 0 {
		return fixBrackets(s)
	}

	// 检查最后一个字符是否是不完整的数字部分
	lastChar := s[lastPos]
	if lastChar == '.' || lastChar == 'e' || lastChar == 'E' || lastChar == '-' || lastChar == '+' {
		// 向前查找，移除不完整的数字尾部
		result := s[:lastPos]

		// 继续检查是否还有不完整的部分
		for {
			lastPos = findLastNonSpace(result)
			if lastPos < 0 {
				break
			}
			lastChar = result[lastPos]
			if lastChar == '.' || lastChar == 'e' || lastChar == 'E' || lastChar == '-' || lastChar == '+' {
				result = result[:lastPos]
			} else {
				break
			}
		}

		return fixBrackets(result)
	}

	// 如果最后一个字符是数字，直接补全括号
	return fixBrackets(s)
}

// fixTruncatedKey 修复截断的键
// 移除不完整的键并补全
// 例如：{"a":1,"b -> {"a":1}
func fixTruncatedKey(s string) string {
	// 找到最后一个逗号的位置
	lastComma := strings.LastIndex(s, ",")
	if lastComma == -1 {
		// 没有逗号，可能是第一个键被截断
		// 尝试找到 { 后的内容
		firstBrace := strings.Index(s, "{")
		if firstBrace != -1 {
			// 检查 { 后是否有完整的键值对
			afterBrace := strings.TrimSpace(s[firstBrace+1:])
			if afterBrace == "" || afterBrace[0] == '"' {
				// 可能是空对象或第一个键被截断
				return fixBrackets(s[:firstBrace+1])
			}
		}
		return fixBrackets(s)
	}

	// 截断到最后一个逗号之前
	result := strings.TrimSpace(s[:lastComma])

	// 补全括号
	return fixBrackets(result)
}

// fixTruncatedColon 修复冒号后无值的情况
// 移除不完整的键值对并补全
// 例如：{"a":1,"b": -> {"a":1}
func fixTruncatedColon(s string) string {
	// 找到最后一个逗号的位置
	lastComma := strings.LastIndex(s, ",")
	if lastComma == -1 {
		// 没有逗号，可能是第一个键值对被截断
		firstBrace := strings.Index(s, "{")
		if firstBrace != -1 {
			return fixBrackets(s[:firstBrace+1])
		}
		return fixBrackets(s)
	}

	// 截断到最后一个逗号之前
	result := strings.TrimSpace(s[:lastComma])

	// 补全括号
	return fixBrackets(result)
}

// aggressiveFix 更激进的修复策略
// 当常规修复失败时，尝试更激进的方法
func aggressiveFix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// 如果不是以 { 或 [ 开头，无法修复
	if s[0] != '{' && s[0] != '[' {
		return "{}"
	}

	// 尝试找到最后一个完整的键值对
	// 策略：从后向前扫描，找到最后一个有效的 JSON 结构

	// 首先尝试闭合字符串
	inString := false
	escaped := false
	var bracketStack []byte

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			bracketStack = append(bracketStack, '{')
		case '[':
			bracketStack = append(bracketStack, '[')
		case '}':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '{' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		case ']':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '[' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		}
	}

	result := s

	// 如果在字符串内部，闭合字符串
	if inString {
		result += `"`
	}

	// 检查最后一个字符，处理特殊情况
	lastPos := findLastNonSpace(result)
	if lastPos >= 0 {
		lastChar := result[lastPos]
		// 如果以逗号结尾，移除逗号
		if lastChar == ',' {
			result = strings.TrimSpace(result[:lastPos])
		}
		// 如果以冒号结尾，移除整个键值对
		if lastChar == ':' {
			lastComma := strings.LastIndex(result, ",")
			if lastComma != -1 {
				result = strings.TrimSpace(result[:lastComma])
			} else {
				// 没有逗号，找到第一个 {
				firstBrace := strings.Index(result, "{")
				if firstBrace != -1 {
					result = result[:firstBrace+1]
				}
			}
		}
	}

	// 重新计算括号栈
	bracketStack = nil
	inString = false
	escaped = false

	for i := 0; i < len(result); i++ {
		c := result[i]

		if escaped {
			escaped = false
			continue
		}

		if inString {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '{':
			bracketStack = append(bracketStack, '{')
		case '[':
			bracketStack = append(bracketStack, '[')
		case '}':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '{' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		case ']':
			if len(bracketStack) > 0 && bracketStack[len(bracketStack)-1] == '[' {
				bracketStack = bracketStack[:len(bracketStack)-1]
			}
		}
	}

	// 补全括号
	for i := len(bracketStack) - 1; i >= 0; i-- {
		if bracketStack[i] == '{' {
			result += "}"
		} else {
			result += "]"
		}
	}

	return result
}

// ChatMessage 聊天消息（支持多模态和工具调用）
type ChatMessage struct {
	Role        string           `json:"role"`
	Content     string           `json:"content"`
	Images      []ImageBlock     `json:"images,omitempty"`      // 图片列表（可选）
	ToolUses    []KiroToolUse    `json:"toolUses,omitempty"`    // assistant 消息中的工具调用
	ToolResults []KiroToolResult `json:"toolResults,omitempty"` // user 消息中的工具结果
}

// ChatService 聊天服务
type ChatService struct {
	authManager *AuthManager
	httpClient  *http.Client
	machineID   string
	version     string
	logger      TraceLogger // 链路日志（可选，由 server 层注入）
}

// NewChatService 创建聊天服务
// 参数：
// - authManager: 认证管理器
func NewChatService(authManager *AuthManager) *ChatService {
	return &ChatService{
		authManager: authManager,
		httpClient:  &http.Client{Timeout: 120 * time.Second},
		machineID:   generateMachineID(),
		version:     "0.8.140",
	}
}

// SetLogger 注入日志记录器（由 server 层调用）
func (s *ChatService) SetLogger(logger TraceLogger) {
	s.logger = logger
}

// getMsgIdFromCtx 从 context 中获取 msgId
func getMsgIdFromCtx(ctx context.Context) string {
	if v := ctx.Value("msgId"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "unknown"
}

// generateConversationID 生成会话 ID
func generateConversationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// IsNonCircuitBreakingError 判断错误是否不应触发熔断和降级
// 以下错误不应计入熔断器失败计数：
// A. 客户端问题：
//  1. context deadline exceeded - 客户端超时
//  2. context canceled - 客户端取消请求
//  3. Improperly formed request - 请求格式错误
//  4. Input is too long / CONTENT_LENGTH_EXCEEDS_THRESHOLD - 输入过长
//  5. INVALID_MODEL_ID - 模型ID无效
//
// B. 服务端临时故障（非账号问题）：
//  6. MODEL_TEMPORARILY_UNAVAILABLE - Kiro 模型临时不可用
//  7. INSUFFICIENT_MODEL_CAPACITY - 模型容量不足（429）
//  8. service temporarily unavailable - 服务临时不可用（503）
//  9. 502 Bad Gateway - 网关错误
//  10. unexpected error - 服务端未捕获异常
func IsNonCircuitBreakingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()

	// A. 客户端问题
	if strings.Contains(msg, "context deadline exceeded") {
		return true
	}
	if strings.Contains(msg, "context canceled") {
		return true
	}
	if strings.Contains(msg, "Improperly formed request") {
		return true
	}
	if strings.Contains(msg, "CONTENT_LENGTH_EXCEEDS_THRESHOLD") {
		return true
	}
	if strings.Contains(msg, "Input is too long") {
		return true
	}
	if strings.Contains(msg, "INVALID_MODEL_ID") {
		return true
	}

	// B. 服务端临时故障（非账号问题，重试其他账号也可能遇到）
	if strings.Contains(msg, "MODEL_TEMPORARILY_UNAVAILABLE") {
		return true
	}
	if strings.Contains(msg, "INSUFFICIENT_MODEL_CAPACITY") {
		return true
	}
	if strings.Contains(msg, "service temporarily unavailable") {
		return true
	}
	if strings.Contains(msg, "502 Bad Gateway") {
		return true
	}
	if strings.Contains(msg, "unexpected error") {
		return true
	}

	return false
}

// IsErrorLog 观测日志
func IsErrorLog(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	// 模型ID无效属于客户端传参错误，不应触发熔断
	if strings.Contains(msg, "INVALID_MODEL_ID") {
		return false
	}
	return true
}

// toJSONString 将任意对象转换为JSON字符串（用于日志输出）
// 如果转换失败，返回错误信息字符串
func toJSONString(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<JSON序列化失败: %v>", err)
	}
	return string(data)
}

// ChatStreamWithModel 流式聊天（支持指定模型）
// 向后兼容版本，不返回 usage 信息
func (s *ChatService) ChatStreamWithModel(ctx context.Context, messages []ChatMessage, model string, callback func(content string, done bool)) error {
	_, err := s.ChatStreamWithModelAndUsage(ctx, messages, model, callback)
	return err
}

// ChatStreamWithModelAndUsage 流式聊天（支持指定模型，返回精确 usage）
// 返回 KiroUsage 包含从 Kiro API EventStream 解析的精确 token 使用量
func (s *ChatService) ChatStreamWithModelAndUsage(ctx context.Context, messages []ChatMessage, model string, callback func(content string, done bool)) (*KiroUsage, error) {
	// 使用带账号ID的方法，便于熔断器追踪
	token, accountID, err := s.authManager.GetAccessTokenWithAccountID()
	if err != nil {
		// 降级：使用旧方法
		token, err = s.authManager.GetAccessToken()
		if err != nil {
			return nil, err
		}
		accountID = ""
	}

	// 打印使用的账号（用于调试轮询）
	// 线上环境已禁用调试日志

	// 构建会话状态
	conversationID := generateConversationID()
	history := make([]any, 0)

	// 转换历史消息
	for i := 0; i < len(messages)-1; i++ {
		msg := messages[i]
		switch msg.Role {
		case "user":
			userMsg := map[string]any{
				"content": msg.Content,
				"origin":  "AI_EDITOR",
			}
			// 只有 model 非空时才添加 modelId
			if model != "" {
				userMsg["modelId"] = model
			}
			// 如果有图片，添加到消息中
			if len(msg.Images) > 0 {
				images := make([]map[string]any, 0, len(msg.Images))
				for _, img := range msg.Images {
					images = append(images, map[string]any{
						"format": img.Format,
						"source": map[string]any{
							"bytes": img.Source.Bytes,
						},
					})
				}
				userMsg["images"] = images
			}
			history = append(history, map[string]any{
				"userInputMessage": userMsg,
			})
		case "assistant":
			history = append(history, map[string]any{
				"assistantResponseMessage": map[string]any{
					"content": msg.Content,
				},
			})
		}
	}

	// 当前消息
	var currentMessage any
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		userMsg := map[string]any{
			"content": lastMsg.Content,
			"origin":  "AI_EDITOR",
		}
		// 只有 model 非空时才添加 modelId
		if model != "" {
			userMsg["modelId"] = model
		}
		// 如果有图片，添加到消息中
		if len(lastMsg.Images) > 0 {
			images := make([]map[string]any, 0, len(lastMsg.Images))
			for _, img := range lastMsg.Images {
				images = append(images, map[string]any{
					"format": img.Format,
					"source": map[string]any{
						"bytes": img.Source.Bytes,
					},
				})
			}
			userMsg["images"] = images
		}
		currentMessage = map[string]any{
			"userInputMessage": userMsg,
		}
	}

	// 构建请求体
	// 注意：customizationArn 需要 ARN 格式，简单模型 ID 不被接受
	// Kiro API 会根据账号配置自动选择模型，暂不传递 customizationArn
	reqBody := map[string]any{
		"conversationState": map[string]any{
			"conversationId":  conversationID,
			"currentMessage":  currentMessage,
			"history":         history,
			"chatTriggerType": "MANUAL",
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 【包2】记录发给 Kiro API 的请求 body
	DebugLog(ctx, s.logger, "【包2】发给Kiro API", map[string]any{
		"body": string(body),
	})

	// 确定 endpoint
	region := s.authManager.GetRegion()
	var endpoint string
	if region == "eu-central-1" {
		endpoint = "https://q.eu-central-1.amazonaws.com"
	} else {
		endpoint = "https://q.us-east-1.amazonaws.com"
	}

	url := endpoint + "/generateAssistantResponse"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE %s %s", s.version, s.machineID))
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/3.x KiroIDE")
	req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("x-amzn-kiro-agent-mode", "chat")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// 【包3】HTTP 请求失败
		if s.logger != nil {
			s.logger.Error(getMsgIdFromCtx(ctx), "【包3】Kiro API请求失败", map[string]any{
				"error": err.Error(),
			})
		}
		// 客户端超时等非服务端故障不触发熔断
		if !IsNonCircuitBreakingError(err) {
			s.authManager.RecordRequestResult(accountID, false)
		}
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		reqErr := fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))

		// 【包3】Kiro API 返回非200
		if s.logger != nil {
			s.logger.Error(getMsgIdFromCtx(ctx), "【包3】Kiro API返回非200", map[string]any{
				"statusCode": resp.StatusCode,
				"body":       string(bodyBytes),
			})
		}
		// 客户端参数错误（400）不触发熔断
		if !IsNonCircuitBreakingError(reqErr) {
			s.authManager.RecordRequestResult(accountID, false)
		}
		return nil, reqErr
	}

	// 记录请求成功（headers）
	if s.logger != nil {
		headers := make(map[string]string)
		for k, v := range resp.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		DebugLog(ctx, s.logger, "【包3】Kiro API返回成功", map[string]any{
			"statusCode":  resp.StatusCode,
			"contentType": resp.Header.Get("Content-Type"),
			"requestId":   resp.Header.Get("x-amzn-RequestId"),
			"headers":     headers,
		})
	}

	// 记录请求成功
	s.authManager.RecordRequestResult(accountID, true)

	// 解析 EventStream（每个事件的 payload 在 parseEventStream 内逐条记录）
	usage, parseErr := s.parseEventStream(ctx, resp.Body, callback)

	return usage, parseErr
}

// UTF8Buffer 处理跨消息边界的 UTF-8 字符
// 当 UTF-8 多字节字符被拆分到不同的消息中时，需要缓冲不完整的字节
type UTF8Buffer struct {
	pending []byte // 待处理的不完整 UTF-8 字节
}

// ProcessBytes 处理原始字节，返回完整的 UTF-8 字符串
// 如果字节末尾有不完整的 UTF-8 序列，会缓冲起来等待下一次调用
func (b *UTF8Buffer) ProcessBytes(data []byte) string {
	// 将待处理的字节和新数据合并
	combined := append(b.pending, data...)
	b.pending = nil

	if len(combined) == 0 {
		return ""
	}

	// 从末尾检查是否有不完整的 UTF-8 序列
	// UTF-8 编码规则：
	// - 0xxxxxxx: 单字节字符 (ASCII)
	// - 110xxxxx 10xxxxxx: 2字节字符
	// - 1110xxxx 10xxxxxx 10xxxxxx: 3字节字符 (中文常用)
	// - 11110xxx 10xxxxxx 10xxxxxx 10xxxxxx: 4字节字符

	// 找到最后一个完整字符的位置
	validEnd := len(combined)
	for i := len(combined) - 1; i >= 0 && i >= len(combined)-4; i-- {
		c := combined[i]
		if c&0x80 == 0 {
			// ASCII 字符，完整
			break
		}
		if c&0xC0 == 0xC0 {
			// 这是一个多字节序列的起始字节
			// 计算期望的字节数
			var expectedLen int
			if c&0xF8 == 0xF0 {
				expectedLen = 4
			} else if c&0xF0 == 0xE0 {
				expectedLen = 3
			} else if c&0xE0 == 0xC0 {
				expectedLen = 2
			} else {
				// 无效的起始字节
				break
			}

			// 检查是否有足够的字节
			remaining := len(combined) - i
			if remaining < expectedLen {
				// 不完整的序列，需要缓冲
				validEnd = i
				b.pending = make([]byte, len(combined)-i)
				copy(b.pending, combined[i:])
			}
			break
		}
		// 继续检查前一个字节（这是一个续字节 10xxxxxx）
	}

	if validEnd == 0 {
		return ""
	}

	return string(combined[:validEnd])
}

// Process 处理字符串，返回完整的 UTF-8 字符串（向后兼容）
func (b *UTF8Buffer) Process(s string) string {
	return b.ProcessBytes([]byte(s))
}

// Flush 刷新缓冲区，返回所有待处理的字节（可能包含不完整的 UTF-8）
func (b *UTF8Buffer) Flush() string {
	if len(b.pending) == 0 {
		return ""
	}
	result := string(b.pending)
	b.pending = nil
	return result
}

// extractStringFieldFromPayload 从 JSON payload 中提取指定字段的原始字节
// 避免 json.Unmarshal 将不完整的 UTF-8 字节转换为 \ufffd
func extractStringFieldFromPayload(payload []byte, fieldName string) ([]byte, bool) {
	// 查找 "fieldName":" 模式
	fieldKey := []byte(`"` + fieldName + `":"`)
	idx := bytes.Index(payload, fieldKey)
	if idx == -1 {
		return nil, false
	}

	// 跳过 "fieldName":"
	start := idx + len(fieldKey)
	if start >= len(payload) {
		return nil, false
	}

	// 查找字符串结束位置（处理转义字符）
	var result []byte
	escaped := false
	for i := start; i < len(payload); i++ {
		c := payload[i]
		if escaped {
			// 处理转义字符
			switch c {
			case '"':
				result = append(result, '"')
			case '\\':
				result = append(result, '\\')
			case 'n':
				result = append(result, '\n')
			case 'r':
				result = append(result, '\r')
			case 't':
				result = append(result, '\t')
			case 'u':
				// Unicode 转义 \uXXXX
				if i+4 < len(payload) {
					hex := string(payload[i+1 : i+5])
					var r rune
					_, _ = fmt.Sscanf(hex, "%x", &r)
					result = append(result, []byte(string(r))...)
					i += 4
				}
			default:
				result = append(result, c)
			}
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			// 字符串结束
			return result, true
		}
		result = append(result, c)
	}

	// 字符串未闭合，返回已解析的部分
	return result, len(result) > 0
}

// extractContentFromPayload 从 JSON payload 中提取 content 字段的原始字节
// 避免 json.Unmarshal 将不完整的 UTF-8 字节转换为 \ufffd
func extractContentFromPayload(payload []byte) ([]byte, bool) {
	return extractStringFieldFromPayload(payload, "content")
}

// extractTextFromPayload 从 JSON payload 中提取 text 字段的原始字节
func extractTextFromPayload(payload []byte) ([]byte, bool) {
	return extractStringFieldFromPayload(payload, "text")
}

// parseEventStream 解析 EventStream
// 返回 KiroUsage 包含从 API 获取的精确 token 使用量
func (s *ChatService) parseEventStream(ctx context.Context, body io.Reader, callback func(content string, done bool)) (*KiroUsage, error) {
	usage := &KiroUsage{}
	utf8Buffer := &UTF8Buffer{} // UTF-8 缓冲处理器

	for {
		msg, err := s.readEventStreamMessage(body)
		if err != nil {
			if err == io.EOF {
				// 刷新缓冲区中剩余的内容
				if remaining := utf8Buffer.Flush(); remaining != "" {
					callback(remaining, false)
				}
				callback("", true)
				return usage, nil
			}
			return usage, err
		}

		msgType := msg.Headers[":message-type"]
		if msgType == "error" {
			return usage, fmt.Errorf("EventStream 错误: %s", msg.Headers[":error-message"])
		}

		if msgType != "event" {
			continue
		}

		eventType := msg.Headers[":event-type"]

		// 【包3】记录每个 EventStream 事件的原始 payload
		// 先检查 payload 是否为合法 JSON，是则直接嵌入（避免双重转义），否则用 string 降级
		if s.logger != nil {
			var payloadVal any
			if json.Valid(msg.Payload) {
				payloadVal = json.RawMessage(msg.Payload)
			} else {
				payloadVal = string(msg.Payload)
			}
			DebugLog(ctx, s.logger, "【包3】EventStream事件", map[string]any{
				"eventType": eventType,
				"payload":   payloadVal,
			})
		}

		// 解析 assistantResponseEvent（文本内容）
		if eventType == "assistantResponseEvent" {
			// 直接从原始 payload 提取 content 字节，避免 json.Unmarshal 损坏 UTF-8
			if contentBytes, ok := extractContentFromPayload(msg.Payload); ok && len(contentBytes) > 0 {
				// 使用 UTF-8 缓冲处理器处理原始字节
				processed := utf8Buffer.ProcessBytes(contentBytes)
				if processed != "" {
					callback(processed, false)
				}
			}
		}

		// 解析 messageMetadataEvent（token 使用量）
		// 参考 Kiro-account-manager kiroApi.ts 第 680-720 行
		if eventType == "messageMetadataEvent" {
			var event struct {
				TokenUsage *struct {
					UncachedInputTokens   int `json:"uncachedInputTokens"`
					CacheReadInputTokens  int `json:"cacheReadInputTokens"`
					CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
					OutputTokens          int `json:"outputTokens"`
					ReasoningTokens       int `json:"reasoningTokens"`
				} `json:"tokenUsage"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil && event.TokenUsage != nil {
				tu := event.TokenUsage
				// inputTokens = uncached + cacheRead + cacheWrite
				usage.InputTokens = tu.UncachedInputTokens + tu.CacheReadInputTokens + tu.CacheWriteInputTokens
				usage.OutputTokens = tu.OutputTokens
				usage.CacheReadTokens = tu.CacheReadInputTokens
				usage.CacheWriteTokens = tu.CacheWriteInputTokens
				usage.ReasoningTokens = tu.ReasoningTokens
			}
		}

		// 解析 meteringEvent（credits 消耗）
		// 参考 Kiro-account-manager kiroApi.ts 第 730-750 行
		if eventType == "meteringEvent" {
			var event struct {
				Usage float64 `json:"usage"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				usage.Credits += event.Usage
			}
		}
	}
}

// EventStreamMessage EventStream 消息
type EventStreamMessage struct {
	Headers map[string]string
	Payload []byte
}

// readEventStreamMessage 读取 EventStream 消息
func (s *ChatService) readEventStreamMessage(r io.Reader) (*EventStreamMessage, error) {
	// 读取前言
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r, prelude); err != nil {
		return nil, err
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])

	// 验证前言 CRC
	if crc32.ChecksumIEEE(prelude[0:8]) != preludeCRC {
		return nil, fmt.Errorf("前言 CRC 校验失败")
	}

	// 读取 headers
	headersData := make([]byte, headersLen)
	if _, err := io.ReadFull(r, headersData); err != nil {
		return nil, err
	}

	// 读取 payload
	payloadLen := totalLen - 12 - headersLen - 4
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	// 读取消息 CRC
	msgCRCBytes := make([]byte, 4)
	if _, err := io.ReadFull(r, msgCRCBytes); err != nil {
		return nil, err
	}

	// 验证消息 CRC
	msgCRC := binary.BigEndian.Uint32(msgCRCBytes)
	fullMsg := append(append(prelude, headersData...), payload...)
	if crc32.ChecksumIEEE(fullMsg) != msgCRC {
		return nil, fmt.Errorf("消息 CRC 校验失败")
	}

	// 解析 headers
	headers := s.parseHeaders(headersData)

	return &EventStreamMessage{
		Headers: headers,
		Payload: payload,
	}, nil
}

// parseHeaders 解析 headers
func (s *ChatService) parseHeaders(data []byte) map[string]string {
	headers := make(map[string]string)
	offset := 0

	for offset < len(data) {
		if offset >= len(data) {
			break
		}

		// Header name length
		nameLen := int(data[offset])
		offset++

		if offset+nameLen > len(data) {
			break
		}

		// Header name
		name := string(data[offset : offset+nameLen])
		offset += nameLen

		if offset >= len(data) {
			break
		}

		// Header type
		headerType := data[offset]
		offset++

		// Header value
		var value string
		switch headerType {
		case 7: // string
			if offset+2 > len(data) {
				break
			}
			strLen := binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
			if offset+int(strLen) > len(data) {
				break
			}
			value = string(data[offset : offset+int(strLen)])
			offset += int(strLen)
		default:
			continue
		}

		headers[name] = value
	}

	return headers
}

// Chat 非流式聊天
func (s *ChatService) Chat(ctx context.Context, messages []ChatMessage) (string, error) {
	var result strings.Builder

	err := s.ChatStreamWithModel(ctx, messages, "", func(content string, done bool) {
		result.WriteString(content)
	})

	if err != nil {
		return "", err
	}

	return result.String(), nil
}

// ChatStream 流式聊天（向后兼容，不指定模型）
func (s *ChatService) ChatStream(ctx context.Context, messages []ChatMessage, callback func(content string, done bool)) error {
	return s.ChatStreamWithModel(ctx, messages, "", callback)
}

// ChatWithModel 非流式聊天（支持指定模型）
func (s *ChatService) ChatWithModel(ctx context.Context, messages []ChatMessage, model string) (string, error) {
	var result strings.Builder

	err := s.ChatStreamWithModel(ctx, messages, model, func(content string, done bool) {
		result.WriteString(content)
	})

	if err != nil {
		return "", err
	}

	return result.String(), nil
}

// SimpleChat 简单聊天
func (s *ChatService) SimpleChat(ctx context.Context, prompt string) (string, error) {
	return s.Chat(ctx, []ChatMessage{
		{Role: "user", Content: prompt},
	})
}

// SimpleChatStream 简单流式聊天
func (s *ChatService) SimpleChatStream(ctx context.Context, prompt string, callback func(content string, done bool)) error {
	return s.ChatStream(ctx, []ChatMessage{
		{Role: "user", Content: prompt},
	}, callback)
}

// ToolUseCallback 工具调用回调
// content: 文本内容
// toolUse: 工具调用（可选）
// done: 是否结束
// isThinking: 是否为 thinking 模式内容（reasoningContentEvent）
// thinkingFormat: thinking 输出格式配置
type ToolUseCallback func(content string, toolUse *KiroToolUse, done bool, isThinking bool)

// ThinkingTextProcessor 处理文本中的 <thinking> 标签
// 参考 Kiro-account-manager proxyServer.ts 的 processText 函数
// 检测普通响应中的 <thinking> 标签并根据配置转换输出格式
type ThinkingTextProcessor struct {
	buffer          string               // 文本缓冲区
	inThinkingBlock bool                 // 是否在 thinking 块内
	format          ThinkingOutputFormat // 输出格式
	Callback        func(text string, isThinking bool)
}

// NewThinkingTextProcessor 创建 thinking 文本处理器
func NewThinkingTextProcessor(format ThinkingOutputFormat, callback func(text string, isThinking bool)) *ThinkingTextProcessor {
	if format == "" {
		format = ThinkingFormatReasoningContent
	}
	return &ThinkingTextProcessor{
		format:   format,
		Callback: callback,
	}
}

// ProcessText 处理文本，检测并转换 <thinking> 标签
// 参考 Kiro-account-manager proxyServer.ts 的 processText 函数
func (p *ThinkingTextProcessor) ProcessText(text string, forceFlush bool) {
	p.buffer += text

	for {
		if !p.inThinkingBlock {
			// 查找 <thinking> 开始标签
			thinkingStart := strings.Index(p.buffer, "<thinking>")
			if thinkingStart != -1 {
				// 输出 thinking 标签之前的内容
				if thinkingStart > 0 {
					beforeThinking := p.buffer[:thinkingStart]
					p.Callback(beforeThinking, false)
				}
				p.buffer = p.buffer[thinkingStart+10:] // 移除 <thinking>
				p.inThinkingBlock = true
			} else if forceFlush || len(p.buffer) > 50 {
				// 没有找到标签，安全输出（保留可能的部分标签）
				// 使用 rune 计算字符数，避免截断 UTF-8 多字节字符
				runes := []rune(p.buffer)
				safeRuneLength := len(runes)
				if !forceFlush {
					// 保留最后 15 个字符（而不是字节）以检测部分标签
					safeRuneLength = max(0, len(runes)-15)
				}
				if safeRuneLength > 0 {
					safeText := string(runes[:safeRuneLength])
					p.Callback(safeText, false)
					p.buffer = string(runes[safeRuneLength:])
				}
				break
			} else {
				break
			}
		} else {
			// 在 thinking 块内，查找 </thinking> 结束标签
			thinkingEnd := strings.Index(p.buffer, "</thinking>")
			if thinkingEnd != -1 {
				// 输出 thinking 内容
				thinkingContent := p.buffer[:thinkingEnd]
				if thinkingContent != "" {
					p.outputThinkingContent(thinkingContent)
				}
				p.buffer = p.buffer[thinkingEnd+11:] // 移除 </thinking>
				p.inThinkingBlock = false
			} else if forceFlush {
				// 强制刷新：输出剩余内容（未闭合的 thinking 块）
				if p.buffer != "" {
					p.outputThinkingContent(p.buffer)
					p.buffer = ""
				}
				break
			} else {
				break
			}
		}
	}
}

// outputThinkingContent 根据格式输出 thinking 内容
func (p *ThinkingTextProcessor) outputThinkingContent(content string) {
	switch p.format {
	case ThinkingFormatThinking:
		// 保持原始 <thinking> 标签
		p.Callback("<thinking>"+content+"</thinking>", false)
	case ThinkingFormatThink:
		// 转换为 <think> 标签
		p.Callback("<think>"+content+"</think>", false)
	default:
		// reasoning_content 格式：标记为 thinking 内容
		p.Callback(content, true)
	}
}

// Flush 刷新缓冲区中剩余的内容
func (p *ThinkingTextProcessor) Flush() {
	p.ProcessText("", true)
}

// KiroHistoryMessage Kiro API 历史消息格式
type KiroHistoryMessage struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// KiroUserInputMessage Kiro API 用户输入消息
type KiroUserInputMessage struct {
	Content                 string                       `json:"content"`
	Origin                  string                       `json:"origin"`
	Images                  []map[string]any             `json:"images,omitempty"`
	UserInputMessageContext *KiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// KiroAssistantResponseMessage Kiro API 助手响应消息
type KiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// ChatMessageWithToolInfo 带工具信息的聊天消息（内部使用）
type ChatMessageWithToolInfo struct {
	Role        string
	Content     string
	Images      []ImageBlock
	ToolUses    []KiroToolUse    // assistant 消息中的工具调用
	ToolResults []KiroToolResult // user 消息中的工具结果
}

// ChatStreamWithTools 流式聊天（支持工具调用）
// 向后兼容版本，不返回 usage 信息
func (s *ChatService) ChatStreamWithTools(
	ctx context.Context,
	messages []ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
	callback ToolUseCallback,
) error {
	_, err := s.ChatStreamWithToolsAndUsage(ctx, messages, model, tools, toolResults, callback)
	return err
}

// ChatStreamWithToolsAndUsage 流式聊天（支持工具调用，返回精确 usage）
// 返回 KiroUsage 包含从 Kiro API EventStream 解析的精确 token 使用量
func (s *ChatService) ChatStreamWithToolsAndUsage(
	ctx context.Context,
	messages []ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
	callback ToolUseCallback,
) (*KiroUsage, error) {
	token, accountID, err := s.authManager.GetAccessTokenWithAccountID()
	if err != nil {
		token, err = s.authManager.GetAccessToken()
		if err != nil {
			return nil, err
		}
		accountID = ""
	}

	// 线上环境已禁用调试日志

	conversationID := generateConversationID()

	// 构建 Kiro API 格式的历史消息和当前消息
	history, currentMessage := s.buildKiroMessages(messages, model, tools, toolResults)

	// 注意：customizationArn 需要 ARN 格式，简单模型 ID 不被接受
	// Kiro API 会根据账号配置自动选择模型，暂不传递 customizationArn
	reqBody := map[string]any{
		"conversationState": map[string]any{
			"conversationId":  conversationID,
			"currentMessage":  currentMessage,
			"history":         history,
			"chatTriggerType": "MANUAL",
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 【包2】记录发给 Kiro API 的请求 body
	DebugLog(ctx, s.logger, "【包2】发给Kiro API(Tools)", map[string]any{
		"body": string(body),
	})

	region := s.authManager.GetRegion()
	var endpoint string
	if region == "eu-central-1" {
		endpoint = "https://q.eu-central-1.amazonaws.com"
	} else {
		endpoint = "https://q.us-east-1.amazonaws.com"
	}

	url := endpoint + "/generateAssistantResponse"

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE %s %s", s.version, s.machineID))
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/3.x KiroIDE")
	req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	req.Header.Set("Accept", "application/vnd.amazon.eventstream")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("x-amzn-kiro-agent-mode", "chat")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// 【包3】HTTP 请求失败
		if s.logger != nil {
			s.logger.Error(getMsgIdFromCtx(ctx), "【包3】Kiro API请求失败(Tools)", map[string]any{
				"error": err.Error(),
			})
		}
		if !IsNonCircuitBreakingError(err) {
			s.authManager.RecordRequestResult(accountID, false)
		}
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		reqErr := fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))

		// 【包3】Kiro API 返回非200
		if s.logger != nil {
			s.logger.Error(getMsgIdFromCtx(ctx), "【包3】Kiro API返回非200(Tools)", map[string]any{
				"statusCode": resp.StatusCode,
				"body":       string(bodyBytes),
			})
		}
		if !IsNonCircuitBreakingError(reqErr) {
			s.authManager.RecordRequestResult(accountID, false)
		}
		return nil, reqErr
	}

	// 记录请求成功（headers）
	if s.logger != nil {
		headers := make(map[string]string)
		for k, v := range resp.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}
		DebugLog(ctx, s.logger, "【包3】Kiro API返回成功(Tools)", map[string]any{
			"statusCode":  resp.StatusCode,
			"contentType": resp.Header.Get("Content-Type"),
			"requestId":   resp.Header.Get("x-amzn-RequestId"),
			"headers":     headers,
		})
	}

	s.authManager.RecordRequestResult(accountID, true)

	// 解析 EventStream（每个事件的 payload 在 parseEventStreamWithTools 内逐条记录）
	usage, parseErr := s.parseEventStreamWithTools(ctx, resp.Body, callback)

	return usage, parseErr
}

// parseEventStreamWithTools 解析 EventStream（支持工具调用）
// 返回 KiroUsage 包含从 API 获取的精确 token 使用量
func (s *ChatService) parseEventStreamWithTools(ctx context.Context, body io.Reader, callback ToolUseCallback) (*KiroUsage, error) {
	usage := &KiroUsage{}
	utf8Buffer := &UTF8Buffer{} // UTF-8 缓冲处理器

	// 工具调用状态跟踪
	var currentToolUse *struct {
		ToolUseId   string
		Name        string
		InputBuffer string
	}
	processedIds := make(map[string]bool)

	for {
		msg, err := s.readEventStreamMessage(body)
		if err != nil {
			if err == io.EOF {
				// 刷新 UTF-8 缓冲区中剩余的内容
				if remaining := utf8Buffer.Flush(); remaining != "" {
					callback(remaining, nil, false, false)
				}
				// 完成未处理的工具调用
				if currentToolUse != nil && !processedIds[currentToolUse.ToolUseId] {
					input, ok, truncated := parseToolInput(currentToolUse.InputBuffer)
					if ok {
						callback("", &KiroToolUse{
							ToolUseId: currentToolUse.ToolUseId,
							Name:      currentToolUse.Name,
							Input:     input,
							Truncated: truncated,
						}, false, false)
					} else {
						// 无法解析，发送跳过通知并记录日志
						callback(fmt.Sprintf("\n\n⚠️ Tool \"%s\" was skipped: input truncated by API (output token limit exceeded)", currentToolUse.Name), nil, false, false)
						logToolSkipped(currentToolUse.Name, currentToolUse.InputBuffer)
					}
				}
				callback("", nil, true, false)

				// 【包3】记录每个 EventStream 事件的原始 payload
				// 先检查 payload 是否为合法 JSON，是则直接嵌入（避免双重转义），否则用 string 降级
				if s.logger != nil {
					DebugLog(ctx, s.logger, "【包3】EventStream事件(Tools)", map[string]any{
						"eventType": "streamEOF",
						"payload":   err.Error(),
					})
				}

				return usage, nil
			}

			// 【包3】记录每个 EventStream 事件的原始 payload
			// 先检查 payload 是否为合法 JSON，是则直接嵌入（避免双重转义），否则用 string 降级
			if s.logger != nil {
				DebugLog(ctx, s.logger, "【包3】EventStream事件(Tools)", map[string]any{
					"eventType": "readError",
					"payload":   err.Error(),
				})
			}
			return usage, err
		}

		msgType := msg.Headers[":message-type"]
		if msgType == "error" {
			return usage, fmt.Errorf("EventStream 错误: %s", msg.Headers[":error-message"])
		}

		if msgType != "event" {
			continue
		}

		eventType := msg.Headers[":event-type"]

		// 【包3】记录每个 EventStream 事件的原始 payload
		// 先检查 payload 是否为合法 JSON，是则直接嵌入（避免双重转义），否则用 string 降级
		if s.logger != nil {
			var payloadVal any
			if json.Valid(msg.Payload) {
				payloadVal = json.RawMessage(msg.Payload)
			} else {
				payloadVal = string(msg.Payload)
			}
			DebugLog(ctx, s.logger, "【包3】EventStream事件(Tools)", map[string]any{
				"eventType": eventType,
				"payload":   payloadVal,
			})
		}

		// 解析 assistantResponseEvent（文本内容）
		if eventType == "assistantResponseEvent" {
			// 直接从原始 payload 提取 content 字节，避免 json.Unmarshal 损坏 UTF-8
			if contentBytes, ok := extractContentFromPayload(msg.Payload); ok && len(contentBytes) > 0 {
				// 使用 UTF-8 缓冲处理器处理原始字节
				processed := utf8Buffer.ProcessBytes(contentBytes)
				if processed != "" {
					callback(processed, nil, false, false)
				}
			}
		}

		// 解析 messageMetadataEvent（token 使用量）
		// 参考 Kiro-account-manager kiroApi.ts 第 680-720 行
		if eventType == "messageMetadataEvent" {
			var event struct {
				TokenUsage *struct {
					UncachedInputTokens   int `json:"uncachedInputTokens"`
					CacheReadInputTokens  int `json:"cacheReadInputTokens"`
					CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
					OutputTokens          int `json:"outputTokens"`
					ReasoningTokens       int `json:"reasoningTokens"`
				} `json:"tokenUsage"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil && event.TokenUsage != nil {
				tu := event.TokenUsage
				// inputTokens = uncached + cacheRead + cacheWrite
				usage.InputTokens = tu.UncachedInputTokens + tu.CacheReadInputTokens + tu.CacheWriteInputTokens
				usage.OutputTokens = tu.OutputTokens
				usage.CacheReadTokens = tu.CacheReadInputTokens
				usage.CacheWriteTokens = tu.CacheWriteInputTokens
				usage.ReasoningTokens = tu.ReasoningTokens
			}
		}

		// 解析 meteringEvent（credits 消耗）
		// 参考 Kiro-account-manager kiroApi.ts 第 730-750 行
		if eventType == "meteringEvent" {
			var event struct {
				Usage float64 `json:"usage"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				usage.Credits += event.Usage
			}
		}

		// 解析 reasoningContentEvent（Thinking 模式推理内容）
		// 参考 Kiro-account-manager kiroApi.ts reasoningContentEvent 处理
		if eventType == "reasoningContentEvent" {
			// 直接从原始 payload 提取 text 字节，避免 json.Unmarshal 损坏 UTF-8
			if textBytes, ok := extractTextFromPayload(msg.Payload); ok && len(textBytes) > 0 {
				// 使用 UTF-8 缓冲处理器处理原始字节
				processed := utf8Buffer.ProcessBytes(textBytes)
				if processed != "" {
					// isThinking=true 标记这是思考内容
					callback(processed, nil, false, true)
				}
				// 累计 reasoning tokens
				usage.ReasoningTokens += len(textBytes) / 3
			}
		}

		// 解析 supplementaryWebLinksEvent（网页链接引用）
		if eventType == "supplementaryWebLinksEvent" {
			var event struct {
				SupplementaryWebLinks []struct {
					URL     string `json:"url"`
					Title   string `json:"title"`
					Snippet string `json:"snippet"`
				} `json:"supplementaryWebLinks"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil && len(event.SupplementaryWebLinks) > 0 {
				var links []string
				for _, link := range event.SupplementaryWebLinks {
					if link.URL != "" {
						title := link.Title
						if title == "" {
							title = link.URL
						}
						links = append(links, fmt.Sprintf("- [%s](%s)", title, link.URL))
					}
				}
				if len(links) > 0 {
					callback("\n\n🔗 **Web References:**\n"+strings.Join(links, "\n"), nil, false, false)
				}
			}
		}

		// 解析 codeReferenceEvent（代码引用/许可证信息）
		if eventType == "codeReferenceEvent" {
			var event struct {
				References []struct {
					LicenseName string `json:"licenseName"`
					Repository  string `json:"repository"`
					URL         string `json:"url"`
				} `json:"references"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil && len(event.References) > 0 {
				var refs []string
				for _, ref := range event.References {
					var parts []string
					if ref.LicenseName != "" {
						parts = append(parts, "License: "+ref.LicenseName)
					}
					if ref.Repository != "" {
						parts = append(parts, "Repo: "+ref.Repository)
					}
					if ref.URL != "" {
						parts = append(parts, "URL: "+ref.URL)
					}
					if len(parts) > 0 {
						refs = append(refs, strings.Join(parts, ", "))
					}
				}
				if len(refs) > 0 {
					callback("\n\n📚 **Code References:**\n"+strings.Join(refs, "\n"), nil, false, false)
				}
			}
		}

		// 解析 followupPromptEvent（后续提示建议）
		if eventType == "followupPromptEvent" {
			var event struct {
				FollowupPrompt struct {
					Content    string `json:"content"`
					UserIntent string `json:"userIntent"`
				} `json:"followupPrompt"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				suggestion := event.FollowupPrompt.Content
				if suggestion == "" {
					suggestion = event.FollowupPrompt.UserIntent
				}
				if suggestion != "" {
					callback("\n\n💡 **Suggested follow-up:** "+suggestion, nil, false, false)
				}
			}
		}

		// 解析 citationEvent（引用事件）
		if eventType == "citationEvent" {
			var event struct {
				Citations []struct {
					Title   string `json:"title"`
					URL     string `json:"url"`
					Content string `json:"content"`
				} `json:"citations"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil && len(event.Citations) > 0 {
				var cites []string
				for i, c := range event.Citations {
					var parts []string
					parts = append(parts, fmt.Sprintf("[%d]", i+1))
					if c.Title != "" {
						parts = append(parts, c.Title)
					}
					if c.URL != "" {
						parts = append(parts, fmt.Sprintf("(%s)", c.URL))
					}
					cites = append(cites, strings.Join(parts, " "))
				}
				if len(cites) > 0 {
					callback("\n\n📖 **Citations:**\n"+strings.Join(cites, "\n"), nil, false, false)
				}
			}
		}

		// 解析 contextUsageEvent（上下文使用百分比）
		if eventType == "contextUsageEvent" {
			var event struct {
				ContextUsagePercentage float64 `json:"contextUsagePercentage"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				// 上下文使用率超过 80% 时警告
				if event.ContextUsagePercentage > 80 {
					callback(fmt.Sprintf("\n\n⚠️ Context usage high: %.1f%%", event.ContextUsagePercentage), nil, false, false)
				}
			}
		}

		// 解析 invalidStateEvent（无效状态事件）
		if eventType == "invalidStateEvent" {
			var event struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				msg := event.Message
				if msg == "" {
					msg = "Invalid state detected"
				}
				callback(fmt.Sprintf("\n\n⚠️ **Warning:** %s (reason: %s)", msg, event.Reason), nil, false, false)
			}
		}

		// 解析 toolUseEvent（工具调用）
		if eventType == "toolUseEvent" {
			var event struct {
				ToolUseId string `json:"toolUseId"`
				Name      string `json:"name"`
				Input     any    `json:"input"`
				Stop      bool   `json:"stop"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err != nil {
				continue
			}

			// 新的工具调用开始（只有当 currentToolUse 为空或 ID 不同时才创建）
			if event.ToolUseId != "" && event.Name != "" {
				// 如果是不同的工具调用，先完成前一个
				if currentToolUse != nil && currentToolUse.ToolUseId != event.ToolUseId {
					if !processedIds[currentToolUse.ToolUseId] {
						input, ok, truncated := parseToolInput(currentToolUse.InputBuffer)
						if ok {
							callback("", &KiroToolUse{
								ToolUseId: currentToolUse.ToolUseId,
								Name:      currentToolUse.Name,
								Input:     input,
								Truncated: truncated,
							}, false, false)
						} else {
							// 无法解析，发送跳过通知并记录日志
							callback(fmt.Sprintf("\n\n⚠️ Tool \"%s\" was skipped: input truncated by Kiro API (output token limit exceeded)", currentToolUse.Name), nil, false, false)
							logToolSkipped(currentToolUse.Name, currentToolUse.InputBuffer)
						}
						processedIds[currentToolUse.ToolUseId] = true
					}
					currentToolUse = nil
				}
				// 只有当 currentToolUse 为空时才创建新的
				if currentToolUse == nil && !processedIds[event.ToolUseId] {
					currentToolUse = &struct {
						ToolUseId   string
						Name        string
						InputBuffer string
					}{
						ToolUseId: event.ToolUseId,
						Name:      event.Name,
					}
				}
			}

			// 累积输入片段
			if currentToolUse != nil {
				switch v := event.Input.(type) {
				case string:
					currentToolUse.InputBuffer += v
				case map[string]interface{}:
					data, _ := json.Marshal(v)
					currentToolUse.InputBuffer = string(data)
				}
			}

			// 工具调用完成
			if event.Stop && currentToolUse != nil {
				input, ok, truncated := parseToolInput(currentToolUse.InputBuffer)
				if ok {
					callback("", &KiroToolUse{
						ToolUseId: currentToolUse.ToolUseId,
						Name:      currentToolUse.Name,
						Input:     input,
						Truncated: truncated,
					}, false, false)
				} else {
					// 无法解析，发送跳过通知并记录日志
					callback(fmt.Sprintf("\n\n⚠️ Tool \"%s\" was skipped: input truncated by Kiro API (output token limit exceeded)", currentToolUse.Name), nil, false, false)
					logToolSkipped(currentToolUse.Name, currentToolUse.InputBuffer)
				}
				processedIds[currentToolUse.ToolUseId] = true
				currentToolUse = nil
			}
		}
	}
}

// parseToolInput 解析工具输入 JSON
// 返回值：
//   - result: 解析后的 map，如果无法解析则为 nil
//   - ok: 是否成功解析（包括修复后成功）
//
// 当 ok=false 时，调用方应跳过该工具调用，不再返回包含 _error 和 _partialInput 的错误 map
// Requirements: 2.4, 3.1, 3.2, 6.1, 6.2, 6.3
// parseToolInput 解析工具调用的 input JSON
// 返回值：(解析结果, 是否成功, 是否被截断修复)
// truncated=true 表示 JSON 是被修复过的，语义可能不完整
func parseToolInput(buffer string) (map[string]interface{}, bool, bool) {
	// 空字符串返回空 map 和 true（向后兼容）
	if buffer == "" {
		return make(map[string]interface{}), true, false
	}

	// 尝试标准 JSON 解析
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(buffer), &input); err == nil {
		// 解析成功，原始 JSON 完整
		return input, true, false
	}

	// JSON 解析失败，检测是否是截断
	truncType, _ := detectTruncation(buffer)

	// 非截断情况（语法错误），无法修复
	if truncType == TruncationNone {
		return nil, false, false
	}

	// 尝试修复截断的 JSON
	fixed, ok := fixTruncatedJSON(buffer, truncType)
	if !ok {
		// 修复失败，返回 nil 表示跳过
		return nil, false, false
	}

	// 修复成功，解析修复后的 JSON
	var fixedInput map[string]interface{}
	if err := json.Unmarshal([]byte(fixed), &fixedInput); err != nil {
		// 修复后仍无法解析，返回 nil 表示跳过
		return nil, false, false
	}

	// 修复成功但标记为截断，让调用方决定如何处理
	return fixedInput, true, true
}

// logToolSkipped 记录工具调用被跳过的日志
// 用于调试和监控截断问题
// Requirements: 5.1, 5.2, 5.3
func logToolSkipped(toolName string, inputBuffer string) {
	// 检测截断类型
	truncType, truncPos := detectTruncation(inputBuffer)

	// 截断部分输入到 500 字符，便于日志记录
	partialInput := inputBuffer
	if len(partialInput) > 500 {
		partialInput = partialInput[:500] + "..."
	}

	// 记录日志，格式符合设计文档要求
	log.Printf("[TOOL_SKIP] Tool \"%s\" skipped: truncation_type=%s, truncation_pos=%d, partial_input=\"%s\"",
		toolName, truncType.String(), truncPos, partialInput)
}

// buildKiroMessages 构建 Kiro API 格式的消息
// 参考 kiroApi.ts 的 sanitizeConversation 和 buildKiroPayload 实现
// 返回：history（历史消息数组）, currentMessage（当前消息）
// 关键：toolResults 参数只用于 currentMessage，历史消息从 ChatMessage.ToolResults 读取
func (s *ChatService) buildKiroMessages(
	messages []ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) ([]map[string]any, map[string]any) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 参考 Kiro-account-manager 的 sanitizeConversation 调用顺序：
	// 1. ensureStartsWithUserMessage
	// 2. removeEmptyUserMessages
	// 3. ensureValidToolUsesAndResults
	// 4. ensureAlternatingMessages
	// 5. ensureEndsWithUserMessage

	msgs := s.ensureStartsWithUser(messages)
	msgs = s.removeEmptyUserMessages(msgs)
	msgs = s.ensureValidToolUsesAndResults(msgs)
	msgs = s.ensureAlternating(msgs)
	msgs = s.ensureEndsWithUser(msgs)

	// 构建 Kiro 格式的消息
	history := make([]map[string]any, 0)

	// 历史消息（除了最后一条）
	for i := 0; i < len(msgs)-1; i++ {
		msg := msgs[i]
		kiroMsg := s.convertToKiroHistoryMessage(msg, model)
		if kiroMsg != nil {
			history = append(history, kiroMsg)
		}
	}

	// 当前消息（最后一条，必须是 user）
	var currentMessage map[string]any
	if len(msgs) > 0 {
		lastMsg := msgs[len(msgs)-1]
		currentMessage = s.buildCurrentMessage(lastMsg, model, tools, toolResults)
	}

	return history, currentMessage
}

// hasToolUses 检查 assistant 消息是否有 toolUses
func hasToolUses(msg ChatMessage) bool {
	return msg.Role == "assistant" && len(msg.ToolUses) > 0
}

// hasToolResults 检查 user 消息是否有 toolResults
func hasToolResults(msg ChatMessage) bool {
	return msg.Role == "user" && len(msg.ToolResults) > 0
}

// hasMatchingToolResults 检查 toolResults 是否与 toolUses 匹配
func hasMatchingToolResults(toolUses []KiroToolUse, toolResults []KiroToolResult) bool {
	if len(toolUses) == 0 {
		return true
	}
	if len(toolResults) == 0 {
		return false
	}
	// 检查所有 toolUses 是否都有对应的 toolResults
	for _, tu := range toolUses {
		found := false
		for _, tr := range toolResults {
			if tr.ToolUseId == tu.ToolUseId {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// createFailedToolResultMessage 创建失败的工具结果消息
func createFailedToolResultMessage(toolUseIds []string) ChatMessage {
	results := make([]KiroToolResult, 0, len(toolUseIds))
	for _, id := range toolUseIds {
		results = append(results, KiroToolResult{
			ToolUseId: id,
			Content:   []KiroToolContent{{Text: "Tool execution failed"}},
			Status:    "error",
		})
	}
	return ChatMessage{
		Role:        "user",
		Content:     "",
		ToolResults: results,
	}
}

// ensureValidToolUsesAndResults 确保每个有 toolUses 的 assistant 消息后面都有对应的 toolResults
// 参考 Kiro-account-manager 的 sanitizeConversation 实现
func (s *ChatService) ensureValidToolUsesAndResults(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	result := make([]ChatMessage, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		result = append(result, msg)

		// 如果是 assistant 消息且有 toolUses
		if hasToolUses(msg) {
			// 检查下一条消息
			var nextMsg *ChatMessage
			if i+1 < len(messages) {
				nextMsg = &messages[i+1]
			}

			// 如果没有下一条消息，或下一条不是 user，或没有 toolResults
			if nextMsg == nil || nextMsg.Role != "user" || !hasToolResults(*nextMsg) {
				// 添加失败的工具结果消息
				toolUseIds := make([]string, 0, len(msg.ToolUses))
				for idx, tu := range msg.ToolUses {
					id := tu.ToolUseId
					if id == "" {
						id = fmt.Sprintf("toolUse_%d", idx+1)
					}
					toolUseIds = append(toolUseIds, id)
				}
				result = append(result, createFailedToolResultMessage(toolUseIds))
			} else if !hasMatchingToolResults(msg.ToolUses, nextMsg.ToolResults) {
				// toolResults 不匹配，添加失败消息
				toolUseIds := make([]string, 0, len(msg.ToolUses))
				for idx, tu := range msg.ToolUses {
					id := tu.ToolUseId
					if id == "" {
						id = fmt.Sprintf("toolUse_%d", idx+1)
					}
					toolUseIds = append(toolUseIds, id)
				}
				result = append(result, createFailedToolResultMessage(toolUseIds))
			}
		}
	}

	return result
}

// ensureStartsWithUser 确保消息以 user 开始
func (s *ChatService) ensureStartsWithUser(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	// 如果第一条不是 user，在前面插入一个空的 user 消息
	if messages[0].Role != "user" {
		placeholder := ChatMessage{
			Role:    "user",
			Content: "Hello",
		}
		return append([]ChatMessage{placeholder}, messages...)
	}

	return messages
}

// removeEmptyUserMessages 移除空的 user 消息
// 参考 Kiro-account-manager 的实现：保留第一条 user 消息和有 toolResults 的消息
func (s *ChatService) removeEmptyUserMessages(messages []ChatMessage) []ChatMessage {
	if len(messages) <= 1 {
		return messages
	}

	// 找到第一条 user 消息的索引
	firstUserIdx := -1
	for i, msg := range messages {
		if msg.Role == "user" {
			firstUserIdx = i
			break
		}
	}

	result := make([]ChatMessage, 0, len(messages))
	for i, msg := range messages {
		// assistant 消息保留
		if msg.Role == "assistant" {
			result = append(result, msg)
			continue
		}
		// 第一条 user 消息保留
		if msg.Role == "user" && i == firstUserIdx {
			result = append(result, msg)
			continue
		}
		// 有内容或有 toolResults 的 user 消息保留
		if msg.Role == "user" {
			hasContent := strings.TrimSpace(msg.Content) != ""
			if hasContent || len(msg.ToolResults) > 0 {
				result = append(result, msg)
			}
			continue
		}
		// 其他消息保留
		result = append(result, msg)
	}

	return result
}

// ensureEndsWithUser 确保消息以 user 结束
func (s *ChatService) ensureEndsWithUser(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}

	// 如果最后一条不是 user，在后面追加一个空的 user 消息
	if messages[len(messages)-1].Role != "user" {
		placeholder := ChatMessage{
			Role:    "user",
			Content: "Continue.",
		}
		return append(messages, placeholder)
	}

	return messages
}

// ensureAlternating 确保消息交替
// 参考 Kiro-account-manager 实现：在连续同角色消息之间插入占位消息
// 不合并消息，以保持 ToolUses 和 ToolResults 的完整性
func (s *ChatService) ensureAlternating(messages []ChatMessage) []ChatMessage {
	if len(messages) <= 1 {
		return messages
	}

	result := make([]ChatMessage, 0, len(messages)*2)
	result = append(result, messages[0])

	for i := 1; i < len(messages); i++ {
		curr := messages[i]
		prev := result[len(result)-1]

		// 如果当前消息和前一条角色相同，插入占位消息
		if curr.Role == prev.Role {
			if prev.Role == "user" {
				// 两个连续 user 消息，插入 assistant 占位消息
				result = append(result, ChatMessage{
					Role:    "assistant",
					Content: "Understood.",
				})
			} else {
				// 两个连续 assistant 消息，插入 user 占位消息
				result = append(result, ChatMessage{
					Role:    "user",
					Content: "Continue.",
				})
			}
		}
		result = append(result, curr)
	}

	return result
}

// convertToKiroHistoryMessage 转换单条消息为 Kiro 历史消息格式
// 注意：历史消息中的 user 消息不需要 tools，只需要 toolResults
// tools 只放在 currentMessage 中（参考 Kiro-account-manager 的 buildKiroPayload）
func (s *ChatService) convertToKiroHistoryMessage(msg ChatMessage, model string) map[string]any {
	switch msg.Role {
	case "user":
		userMsg := map[string]any{
			"content": msg.Content,
			"origin":  "AI_EDITOR",
		}

		// 只有 model 非空时才添加 modelId
		if model != "" {
			userMsg["modelId"] = model
		}

		// 添加图片
		if len(msg.Images) > 0 {
			images := make([]map[string]any, 0, len(msg.Images))
			for _, img := range msg.Images {
				images = append(images, map[string]any{
					"format": img.Format,
					"source": map[string]any{"bytes": img.Source.Bytes},
				})
			}
			userMsg["images"] = images
		}

		// 关键：历史消息中的 user 消息只需要 toolResults，不需要 tools
		// toolResults 从 ChatMessage.ToolResults 读取
		if len(msg.ToolResults) > 0 {
			resultsData := make([]map[string]any, 0, len(msg.ToolResults))
			for _, r := range msg.ToolResults {
				contentData := make([]map[string]any, 0, len(r.Content))
				for _, c := range r.Content {
					contentData = append(contentData, map[string]any{"text": c.Text})
				}
				resultsData = append(resultsData, map[string]any{
					"toolUseId": r.ToolUseId,
					"content":   contentData,
					"status":    r.Status,
				})
			}
			userMsg["userInputMessageContext"] = map[string]any{
				"toolResults": resultsData,
			}
		}

		return map[string]any{"userInputMessage": userMsg}

	case "assistant":
		assistantMsg := map[string]any{
			"content": msg.Content,
		}
		// 关键：如果有 toolUses，必须放到 assistantResponseMessage 中
		if len(msg.ToolUses) > 0 {
			toolUsesData := make([]map[string]any, 0, len(msg.ToolUses))
			for _, tu := range msg.ToolUses {
				toolUsesData = append(toolUsesData, map[string]any{
					"toolUseId": tu.ToolUseId,
					"name":      tu.Name,
					"input":     tu.Input,
				})
			}
			assistantMsg["toolUses"] = toolUsesData
		}
		return map[string]any{"assistantResponseMessage": assistantMsg}
	}

	return nil
}

// buildCurrentMessage 构建当前消息（最后一条 user 消息）
func (s *ChatService) buildCurrentMessage(
	msg ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) map[string]any {
	userMsg := map[string]any{
		"content": msg.Content,
		"origin":  "AI_EDITOR",
	}

	// 只有 model 非空时才添加 modelId
	if model != "" {
		userMsg["modelId"] = model
	}

	// 添加图片
	if len(msg.Images) > 0 {
		images := make([]map[string]any, 0, len(msg.Images))
		for _, img := range msg.Images {
			images = append(images, map[string]any{
				"format": img.Format,
				"source": map[string]any{"bytes": img.Source.Bytes},
			})
		}
		userMsg["images"] = images
	}

	// 添加 userInputMessageContext（tools 和 toolResults）
	if len(tools) > 0 || len(toolResults) > 0 {
		ctx := s.buildUserInputMessageContext(tools, toolResults)
		userMsg["userInputMessageContext"] = ctx
	}

	return map[string]any{"userInputMessage": userMsg}
}

// buildUserInputMessageContext 构建用户输入消息上下文
func (s *ChatService) buildUserInputMessageContext(
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) map[string]any {
	ctx := map[string]any{}

	// 添加 tools
	if len(tools) > 0 {
		toolsData := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			toolsData = append(toolsData, map[string]any{
				"toolSpecification": map[string]any{
					"name":        t.ToolSpecification.Name,
					"description": t.ToolSpecification.Description,
					"inputSchema": map[string]any{"json": t.ToolSpecification.InputSchema},
				},
			})
		}
		ctx["tools"] = toolsData
	}

	// 添加 toolResults
	if len(toolResults) > 0 {
		resultsData := make([]map[string]any, 0, len(toolResults))
		for _, r := range toolResults {
			contentData := make([]map[string]any, 0, len(r.Content))
			for _, c := range r.Content {
				contentData = append(contentData, map[string]any{"text": c.Text})
			}
			resultsData = append(resultsData, map[string]any{
				"toolUseId": r.ToolUseId,
				"content":   contentData,
				"status":    r.Status,
			})
		}
		ctx["toolResults"] = resultsData
	}

	return ctx
}
