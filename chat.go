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
func (s *ChatService) ChatStreamWithModel(messages []ChatMessage, model string, callback func(content string, done bool)) error {
	// 使用带账号ID的方法，便于熔断器追踪
	token, accountID, err := s.authManager.GetAccessTokenWithAccountID()
	if err != nil {
		// 降级：使用旧方法
		token, err = s.authManager.GetAccessToken()
		if err != nil {
			return err
		}
		accountID = ""
	}

	// 打印使用的账号（用于调试轮询）
	if accountID != "" {
		fmt.Printf("[轮询] 使用账号: %s\n", accountID[:8])
	}

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
	reqBody := map[string]any{
		"conversationState": map[string]any{
			"conversationId":  conversationID,
			"currentMessage":  currentMessage,
			"history":         history,
			"chatTriggerType": "MANUAL",
		},
	}

	// 添加 customizationArn（模型参数）
	// 如果指定了模型且模型有效，则添加到请求体
	if model != "" && IsValidModel(model) {
		reqBody["customizationArn"] = model
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
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
		return err
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
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		// 记录请求失败（HTTP错误）
		s.authManager.RecordRequestResult(accountID, false)
		return fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	// 记录请求成功
	s.authManager.RecordRequestResult(accountID, true)

	// 解析 EventStream
	return s.parseEventStream(resp.Body, callback)
}

// parseEventStream 解析 EventStream
func (s *ChatService) parseEventStream(body io.Reader, callback func(content string, done bool)) error {
	for {
		msg, err := s.readEventStreamMessage(body)
		if err != nil {
			if err == io.EOF {
				callback("", true)
				return nil
			}
			return err
		}

		msgType := msg.Headers[":message-type"]
		if msgType == "error" {
			return fmt.Errorf("EventStream 错误: %s", msg.Headers[":error-message"])
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
func (s *ChatService) ChatStreamWithTools(
	messages []ChatMessage,
	model string,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
	callback ToolUseCallback,
) error {
	token, accountID, err := s.authManager.GetAccessTokenWithAccountID()
	if err != nil {
		token, err = s.authManager.GetAccessToken()
		if err != nil {
			return err
		}
		accountID = ""
	}

	if accountID != "" {
		fmt.Printf("[轮询] 使用账号: %s\n", accountID[:8])
	}

	conversationID := generateConversationID()

	// 构建 Kiro API 格式的历史消息和当前消息
	history, currentMessage := s.buildKiroMessages(messages, tools, toolResults)

	reqBody := map[string]any{
		"conversationState": map[string]any{
			"conversationId":  conversationID,
			"currentMessage":  currentMessage,
			"history":         history,
			"chatTriggerType": "MANUAL",
		},
	}

	if model != "" && IsValidModel(model) {
		reqBody["customizationArn"] = model
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	// 调试：打印请求体
	fmt.Printf("[DEBUG] Kiro API 请求体:\n%s\n", string(body))

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
		return err
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
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		s.authManager.RecordRequestResult(accountID, false)
		return fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	s.authManager.RecordRequestResult(accountID, true)

	return s.parseEventStreamWithTools(resp.Body, callback)
}

// parseEventStreamWithTools 解析 EventStream（支持工具调用）
func (s *ChatService) parseEventStreamWithTools(body io.Reader, callback ToolUseCallback) error {
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
				return nil
			}
			return err
		}

		msgType := msg.Headers[":message-type"]
		if msgType == "error" {
			return fmt.Errorf("EventStream 错误: %s", msg.Headers[":error-message"])
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
// 参考 kiroApi.ts 的 sanitizeConversation 实现
// 返回：history（历史消息数组）, currentMessage（当前消息）
func (s *ChatService) buildKiroMessages(
	messages []ChatMessage,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) ([]map[string]any, map[string]any) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 步骤1：确保消息以 user 开始
	msgs := s.ensureStartsWithUser(messages)

	// 步骤2：确保消息以 user 结束
	msgs = s.ensureEndsWithUser(msgs)

	// 步骤3：确保消息交替（合并连续的同角色消息）
	msgs = s.ensureAlternating(msgs)

	// 步骤4：构建 Kiro 格式的消息
	history := make([]map[string]any, 0)

	// 历史消息（除了最后一条）
	for i := 0; i < len(msgs)-1; i++ {
		msg := msgs[i]
		kiroMsg := s.convertToKiroHistoryMessage(msg, tools, nil)
		if kiroMsg != nil {
			history = append(history, kiroMsg)
		}
	}

	// 当前消息（最后一条，必须是 user）
	var currentMessage map[string]any
	if len(msgs) > 0 {
		lastMsg := msgs[len(msgs)-1]
		currentMessage = s.buildCurrentMessage(lastMsg, tools, toolResults)
	}

	return history, currentMessage
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
			Content: "Continue the conversation.",
		}
		return append([]ChatMessage{placeholder}, messages...)
	}

	return messages
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

// ensureAlternating 确保消息交替（合并连续的同角色消息）
func (s *ChatService) ensureAlternating(messages []ChatMessage) []ChatMessage {
	if len(messages) <= 1 {
		return messages
	}

	result := make([]ChatMessage, 0, len(messages))
	result = append(result, messages[0])

	for i := 1; i < len(messages); i++ {
		curr := messages[i]
		prev := &result[len(result)-1]

		// 如果当前消息和前一条角色相同，合并内容
		if curr.Role == prev.Role {
			if prev.Content != "" && curr.Content != "" {
				prev.Content = prev.Content + "\n\n" + curr.Content
			} else if curr.Content != "" {
				prev.Content = curr.Content
			}
			// 合并图片
			prev.Images = append(prev.Images, curr.Images...)
		} else {
			result = append(result, curr)
		}
	}

	return result
}

// convertToKiroHistoryMessage 转换单条消息为 Kiro 历史消息格式
func (s *ChatService) convertToKiroHistoryMessage(
	msg ChatMessage,
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) map[string]any {
	switch msg.Role {
	case "user":
		userMsg := map[string]any{
			"content": msg.Content,
			"origin":  "AI_EDITOR",
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

		// 添加 tools（每条 user 消息都需要带上 tools）
		if len(tools) > 0 {
			ctx := s.buildUserInputMessageContext(tools, nil)
			userMsg["userInputMessageContext"] = ctx
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
	tools []KiroToolWrapper,
	toolResults []KiroToolResult,
) map[string]any {
	userMsg := map[string]any{
		"content": msg.Content,
		"origin":  "AI_EDITOR",
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
