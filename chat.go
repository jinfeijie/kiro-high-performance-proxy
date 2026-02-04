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

// ChatMessage 聊天消息（支持多模态）
type ChatMessage struct {
	Role    string       `json:"role"`
	Content string       `json:"content"`
	Images  []ImageBlock `json:"images,omitempty"` // 图片列表（可选）
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
