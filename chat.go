package kiroclient

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"
)

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
}

// NewChatService 创建聊天服务
func NewChatService(authManager *AuthManager) *ChatService {
	return &ChatService{
		authManager: authManager,
		httpClient:  &http.Client{Timeout: 120 * time.Second},
		machineID:   generateMachineID(),
		version:     "0.8.140",
	}
}

// generateConversationID 生成会话 ID
func generateConversationID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ChatStreamWithModel 流式聊天（支持指定模型）
// 向后兼容版本，不返回 usage 信息
func (s *ChatService) ChatStreamWithModel(messages []ChatMessage, model string, callback func(content string, done bool)) error {
	_, err := s.ChatStreamWithModelAndUsage(messages, model, callback)
	return err
}

// ChatStreamWithModelAndUsage 流式聊天（支持指定模型，返回精确 usage）
// 返回 KiroUsage 包含从 Kiro API EventStream 解析的精确 token 使用量
func (s *ChatService) ChatStreamWithModelAndUsage(messages []ChatMessage, model string, callback func(content string, done bool)) (*KiroUsage, error) {
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
				"modelId": model,
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
			"modelId": model,
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

	// 确定 endpoint
	region := s.authManager.GetRegion()
	var endpoint string
	if region == "eu-central-1" {
		endpoint = "https://q.eu-central-1.amazonaws.com"
	} else {
		endpoint = "https://q.us-east-1.amazonaws.com"
	}

	url := endpoint + "/generateAssistantResponse"

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
		// 记录请求失败（网络错误）
		s.authManager.RecordRequestResult(accountID, false)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		// 记录请求失败（HTTP错误）
		s.authManager.RecordRequestResult(accountID, false)
		return nil, fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	// 记录请求成功
	s.authManager.RecordRequestResult(accountID, true)

	// 解析 EventStream
	return s.parseEventStream(resp.Body, callback)
}

// parseEventStream 解析 EventStream
// 返回 KiroUsage 包含从 API 获取的精确 token 使用量
func (s *ChatService) parseEventStream(body io.Reader, callback func(content string, done bool)) (*KiroUsage, error) {
	usage := &KiroUsage{}

	for {
		msg, err := s.readEventStreamMessage(body)
		if err != nil {
			if err == io.EOF {
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

		// 解析 assistantResponseEvent（文本内容）
		if eventType == "assistantResponseEvent" {
			var event struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				if event.Content != "" {
					callback(event.Content, false)
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
func (s *ChatService) Chat(messages []ChatMessage) (string, error) {
	var result strings.Builder

	err := s.ChatStreamWithModel(messages, "", func(content string, done bool) {
		result.WriteString(content)
	})

	if err != nil {
		return "", err
	}

	return result.String(), nil
}

// ChatStream 流式聊天（向后兼容，不指定模型）
func (s *ChatService) ChatStream(messages []ChatMessage, callback func(content string, done bool)) error {
	return s.ChatStreamWithModel(messages, "", callback)
}

// ChatWithModel 非流式聊天（支持指定模型）
func (s *ChatService) ChatWithModel(messages []ChatMessage, model string) (string, error) {
	var result strings.Builder

	err := s.ChatStreamWithModel(messages, model, func(content string, done bool) {
		result.WriteString(content)
	})

	if err != nil {
		return "", err
	}

	return result.String(), nil
}

// SimpleChat 简单聊天
func (s *ChatService) SimpleChat(prompt string) (string, error) {
	return s.Chat([]ChatMessage{
		{Role: "user", Content: prompt},
	})
}

// SimpleChatStream 简单流式聊天
func (s *ChatService) SimpleChatStream(prompt string, callback func(content string, done bool)) error {
	return s.ChatStream([]ChatMessage{
		{Role: "user", Content: prompt},
	}, callback)
}

// ToolUseCallback 工具调用回调（content 为文本，toolUse 为工具调用，done 为结束标志）
type ToolUseCallback func(content string, toolUse *KiroToolUse, done bool)

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
	messages []ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
	callback ToolUseCallback,
) error {
	_, err := s.ChatStreamWithToolsAndUsage(messages, model, tools, toolResults, callback)
	return err
}

// ChatStreamWithToolsAndUsage 流式聊天（支持工具调用，返回精确 usage）
// 返回 KiroUsage 包含从 Kiro API EventStream 解析的精确 token 使用量
func (s *ChatService) ChatStreamWithToolsAndUsage(
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

	region := s.authManager.GetRegion()
	var endpoint string
	if region == "eu-central-1" {
		endpoint = "https://q.eu-central-1.amazonaws.com"
	} else {
		endpoint = "https://q.us-east-1.amazonaws.com"
	}

	url := endpoint + "/generateAssistantResponse"

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
		s.authManager.RecordRequestResult(accountID, false)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		s.authManager.RecordRequestResult(accountID, false)
		return nil, fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	s.authManager.RecordRequestResult(accountID, true)

	return s.parseEventStreamWithTools(resp.Body, callback)
}

// parseEventStreamWithTools 解析 EventStream（支持工具调用）
// 返回 KiroUsage 包含从 API 获取的精确 token 使用量
func (s *ChatService) parseEventStreamWithTools(body io.Reader, callback ToolUseCallback) (*KiroUsage, error) {
	usage := &KiroUsage{}

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
				// 完成未处理的工具调用
				if currentToolUse != nil && !processedIds[currentToolUse.ToolUseId] {
					input := parseToolInput(currentToolUse.InputBuffer)
					callback("", &KiroToolUse{
						ToolUseId: currentToolUse.ToolUseId,
						Name:      currentToolUse.Name,
						Input:     input,
					}, false)
				}
				callback("", nil, true)
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

		// 解析 assistantResponseEvent（文本内容）
		if eventType == "assistantResponseEvent" {
			var event struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(msg.Payload, &event); err == nil {
				if event.Content != "" {
					callback(event.Content, nil, false)
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
						input := parseToolInput(currentToolUse.InputBuffer)
						callback("", &KiroToolUse{
							ToolUseId: currentToolUse.ToolUseId,
							Name:      currentToolUse.Name,
							Input:     input,
						}, false)
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
				input := parseToolInput(currentToolUse.InputBuffer)
				callback("", &KiroToolUse{
					ToolUseId: currentToolUse.ToolUseId,
					Name:      currentToolUse.Name,
					Input:     input,
				}, false)
				processedIds[currentToolUse.ToolUseId] = true
				currentToolUse = nil
			}
		}
	}
}

// parseToolInput 解析工具输入 JSON
func parseToolInput(buffer string) map[string]interface{} {
	if buffer == "" {
		return make(map[string]interface{})
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(buffer), &input); err != nil {
		return map[string]interface{}{
			"_error":        "Tool input parse failed",
			"_partialInput": buffer,
		}
	}
	return input
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
			"modelId": model,
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
		"modelId": model,
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
