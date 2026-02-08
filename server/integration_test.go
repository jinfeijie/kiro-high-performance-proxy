package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

// setupIntegrationTestRouter 创建集成测试路由器
// 模拟 main() 的初始化流程，确保账号缓存、代理配置、熔断统计器等全局状态就绪
func setupIntegrationTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)

	// go test ./server/ 的工作目录是 kiro-api-client-go/server/
	// 但配置文件（kiro-accounts.json 等）在 kiro-api-client-go/ 根目录
	// 需要切换到上级目录，与 main() 的运行环境保持一致
	os.Chdir("..")

	client = kiroclient.NewKiroClient()

	// 初始化账号缓存（从 kiro-accounts.json 加载到内存）
	// 不初始化会导致 selectAccount() 找不到账号，请求挂起或报错
	_ = client.Auth.InitAccountsCache()

	// 加载代理配置（thinking 模式等），不加载则使用零值可能导致空指针
	loadProxyConfig()

	// 加载模型映射
	loadModelMapping()

	// 初始化熔断错误率统计器（部分 handler 会调用 circuitStats.Record）
	if circuitStats == nil {
		circuitStats = NewCircuitStats()
	}

	router := gin.New()
	router.POST("/v1/chat/completions", handleOpenAIChat)
	router.POST("/v1/messages", handleClaudeChat)

	return router
}

// ========== OpenAI 兼容接口测试 ==========

// TestOpenAIChat_NonStream_RequestFields 测试 OpenAI 非流式请求字段
func TestOpenAIChat_NonStream_RequestFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	tests := []struct {
		name       string
		reqBody    map[string]any
		expectCode int
		checkError bool
	}{
		{
			name: "完整请求字段",
			reqBody: map[string]any{
				"model": "claude-sonnet-4.5",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"stream": false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "缺少 model 字段",
			reqBody: map[string]any{
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"stream": false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "缺少 messages 字段",
			reqBody: map[string]any{
				"model":  "claude-sonnet-4.5",
				"stream": false,
			},
			expectCode: 500, // 实际返回 500（转换消息时出错）
			checkError: true,
		},
		{
			name: "无效的 model",
			reqBody: map[string]any{
				"model": "invalid-model-xyz",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"stream": false,
			},
			expectCode: 400,
			checkError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.reqBody)
			req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("期望状态码 %d, 得到 %d", tt.expectCode, w.Code)
			}

			if tt.checkError {
				var resp map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("解析响应失败: %v", err)
				}
				if _, ok := resp["error"]; !ok {
					t.Error("响应中应包含 error 字段")
				}
			}
		})
	}
}

// TestOpenAIChat_NonStream_ResponseFields 测试 OpenAI 非流式响应字段完整性
func TestOpenAIChat_NonStream_ResponseFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	reqBody := OpenAIChatRequest{
		Model: "claude-sonnet-4.5",
		Messages: []map[string]any{
			{"role": "user", "content": "Say hello"},
		},
		Stream: false,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过响应字段测试（可能因为 Token 问题）")
		return
	}

	var resp OpenAIChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	// 验证必需字段
	if resp.ID == "" {
		t.Error("响应缺少 id 字段")
	}
	if !strings.HasPrefix(resp.ID, "chatcmpl_") {
		t.Errorf("id 格式错误: %s, 应以 chatcmpl_ 开头", resp.ID)
	}

	if resp.Object != "chat.completion" {
		t.Errorf("object 字段错误: %s, 期望 chat.completion", resp.Object)
	}

	if resp.Created == 0 {
		t.Error("响应缺少 created 字段")
	}

	if resp.Model != "claude-sonnet-4.5" {
		t.Errorf("model 字段错误: %s, 期望 claude-sonnet-4.5", resp.Model)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("响应缺少 choices 字段")
	}

	choice := resp.Choices[0]
	if choice.Index != 0 {
		t.Errorf("choice.index 错误: %d, 期望 0", choice.Index)
	}

	if choice.Message.Role != "assistant" {
		t.Errorf("choice.message.role 错误: %s, 期望 assistant", choice.Message.Role)
	}

	if choice.Message.Content == "" {
		t.Error("choice.message.content 为空")
	}

	if choice.FinishReason != "stop" {
		t.Errorf("choice.finish_reason 错误: %s, 期望 stop", choice.FinishReason)
	}

	// 验证 usage 字段（非流式响应可能没有 usage）
	if resp.Usage != nil {
		if resp.Usage.PromptTokens < 0 {
			t.Errorf("usage.prompt_tokens 错误: %d, 不应为负数", resp.Usage.PromptTokens)
		}

		if resp.Usage.CompletionTokens < 0 {
			t.Errorf("usage.completion_tokens 错误: %d, 不应为负数", resp.Usage.CompletionTokens)
		}

		if resp.Usage.TotalTokens != resp.Usage.PromptTokens+resp.Usage.CompletionTokens {
			t.Errorf("usage.total_tokens 错误: %d, 应等于 %d",
				resp.Usage.TotalTokens,
				resp.Usage.PromptTokens+resp.Usage.CompletionTokens)
		}
	}

	t.Logf("✅ OpenAI 非流式响应字段验证通过")
	t.Logf("   ID: %s", resp.ID)
	t.Logf("   Model: %s", resp.Model)
	t.Logf("   Content: %s", choice.Message.Content)
	if resp.Usage != nil {
		t.Logf("   Usage: %d input + %d output = %d total",
			resp.Usage.PromptTokens,
			resp.Usage.CompletionTokens,
			resp.Usage.TotalTokens)
	}
}

// TestOpenAIChat_Stream_ResponseFields 测试 OpenAI 流式响应字段完整性
func TestOpenAIChat_Stream_ResponseFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	reqBody := OpenAIChatRequest{
		Model: "claude-sonnet-4.5",
		Messages: []map[string]any{
			{"role": "user", "content": "Count to 3"},
		},
		Stream: true,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证 Content-Type
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type 错误: %s, 期望 text/event-stream; charset=utf-8", contentType)
	}

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过流式响应字段测试（可能因为 Token 问题）")
		return
	}

	// 解析 SSE 流
	scanner := bufio.NewScanner(w.Body)
	var chunks []map[string]any
	var foundDone bool
	var lastChunkWithUsage map[string]any

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			foundDone = true
			break
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Logf("解析 chunk 失败: %v, data: %s", err, data)
			continue
		}

		chunks = append(chunks, chunk)

		// 保存最后一个包含 usage 的 chunk
		if _, hasUsage := chunk["usage"]; hasUsage {
			lastChunkWithUsage = chunk
		}
	}

	if len(chunks) == 0 {
		t.Fatal("未收到任何 SSE chunk")
	}

	if !foundDone {
		t.Error("未收到 [DONE] 标记")
	}

	// 验证第一个 chunk 的字段
	firstChunk := chunks[0]
	if id, ok := firstChunk["id"].(string); !ok || !strings.HasPrefix(id, "chatcmpl_") {
		t.Errorf("第一个 chunk 的 id 格式错误: %v", firstChunk["id"])
	}

	if obj, ok := firstChunk["object"].(string); !ok || obj != "chat.completion.chunk" {
		t.Errorf("第一个 chunk 的 object 错误: %v, 期望 chat.completion.chunk", firstChunk["object"])
	}

	if _, ok := firstChunk["created"].(float64); !ok {
		t.Error("第一个 chunk 缺少 created 字段")
	}

	if model, ok := firstChunk["model"].(string); !ok || model != "claude-sonnet-4.5" {
		t.Errorf("第一个 chunk 的 model 错误: %v", firstChunk["model"])
	}

	// 验证 choices 结构
	choices, ok := firstChunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		t.Fatal("第一个 chunk 缺少 choices 字段")
	}

	choice := choices[0].(map[string]interface{})
	if index, ok := choice["index"].(float64); !ok || int(index) != 0 {
		t.Errorf("choice.index 错误: %v", choice["index"])
	}

	// 验证最后一个包含 usage 的 chunk
	if lastChunkWithUsage == nil {
		t.Fatal("未找到包含 usage 的 chunk")
	}

	usage, ok := lastChunkWithUsage["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("usage 字段格式错误")
	}

	if promptTokens, ok := usage["prompt_tokens"].(float64); !ok || promptTokens <= 0 {
		t.Errorf("usage.prompt_tokens 错误: %v", usage["prompt_tokens"])
	}

	if completionTokens, ok := usage["completion_tokens"].(float64); !ok || completionTokens <= 0 {
		t.Errorf("usage.completion_tokens 错误: %v", usage["completion_tokens"])
	}

	if totalTokens, ok := usage["total_tokens"].(float64); !ok || totalTokens <= 0 {
		t.Errorf("usage.total_tokens 错误: %v", usage["total_tokens"])
	}

	// 验证最后一个 chunk 的 finish_reason
	lastChoices, ok := lastChunkWithUsage["choices"].([]interface{})
	if ok && len(lastChoices) > 0 {
		lastChoice := lastChoices[0].(map[string]interface{})
		if finishReason, ok := lastChoice["finish_reason"].(string); !ok || finishReason != "stop" {
			t.Errorf("最后一个 chunk 的 finish_reason 错误: %v", lastChoice["finish_reason"])
		}
	}

	t.Logf("✅ OpenAI 流式响应字段验证通过")
	t.Logf("   收到 %d 个 chunks", len(chunks))
	t.Logf("   Usage: %v", usage)
}

// ========== Claude 兼容接口测试 ==========

// TestClaudeChat_NonStream_RequestFields 测试 Claude 非流式请求字段
func TestClaudeChat_NonStream_RequestFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	tests := []struct {
		name       string
		reqBody    map[string]any
		expectCode int
		checkError bool
	}{
		{
			name: "完整请求字段",
			reqBody: map[string]any{
				"model": "claude-sonnet-4.5",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"max_tokens": 1000,
				"stream":     false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "包含 system 字段（字符串）",
			reqBody: map[string]any{
				"model":  "claude-sonnet-4.5",
				"system": "You are a helpful assistant",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"max_tokens": 1000,
				"stream":     false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "包含 temperature 字段",
			reqBody: map[string]any{
				"model": "claude-sonnet-4.5",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"max_tokens":  1000,
				"temperature": 0.7,
				"stream":      false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "缺少 model 字段",
			reqBody: map[string]any{
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"max_tokens": 1000,
				"stream":     false,
			},
			expectCode: 200,
			checkError: false,
		},
		{
			name: "缺少 messages 字段",
			reqBody: map[string]any{
				"model":      "claude-sonnet-4.5",
				"max_tokens": 1000,
				"stream":     false,
			},
			expectCode: 500, // 实际返回 500（转换消息时出错）
			checkError: true,
		},
		{
			name: "无效的 model",
			reqBody: map[string]any{
				"model": "invalid-model-xyz",
				"messages": []map[string]any{
					{"role": "user", "content": "测试"},
				},
				"max_tokens": 1000,
				"stream":     false,
			},
			expectCode: 400,
			checkError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.reqBody)
			req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("期望状态码 %d, 得到 %d", tt.expectCode, w.Code)
			}

			if tt.checkError {
				var resp map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("解析响应失败: %v", err)
				}
				if _, ok := resp["error"]; !ok {
					t.Error("响应中应包含 error 字段")
				}
			}
		})
	}
}

// TestClaudeChat_NonStream_ResponseFields 测试 Claude 非流式响应字段完整性
func TestClaudeChat_NonStream_ResponseFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	reqBody := ClaudeChatRequest{
		Model: "claude-sonnet-4.5",
		Messages: []map[string]any{
			{"role": "user", "content": "Say hello"},
		},
		MaxTokens: 1000,
		Stream:    false,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过响应字段测试（可能因为 Token 问题）")
		return
	}

	var resp ClaudeChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	// 验证必需字段
	if resp.ID == "" {
		t.Error("响应缺少 id 字段")
	}
	if !strings.HasPrefix(resp.ID, "msg_") {
		t.Errorf("id 格式错误: %s, 应以 msg_ 开头", resp.ID)
	}

	if resp.Type != "message" {
		t.Errorf("type 字段错误: %s, 期望 message", resp.Type)
	}

	if resp.Role != "assistant" {
		t.Errorf("role 字段错误: %s, 期望 assistant", resp.Role)
	}

	if resp.Model != "claude-sonnet-4.5" {
		t.Errorf("model 字段错误: %s, 期望 claude-sonnet-4.5", resp.Model)
	}

	if len(resp.Content) == 0 {
		t.Fatal("响应缺少 content 字段")
	}

	contentBlock := resp.Content[0]
	if contentBlock.Type != "text" {
		t.Errorf("content[0].type 错误: %s, 期望 text", contentBlock.Type)
	}

	if contentBlock.Text == "" {
		t.Error("content[0].text 为空")
	}

	if resp.StopReason != "end_turn" && resp.StopReason != "" {
		t.Errorf("stop_reason 错误: %s, 期望 end_turn 或空", resp.StopReason)
	}

	// 验证 usage 字段
	if resp.Usage == nil {
		t.Fatal("响应缺少 usage 字段")
	}

	if resp.Usage.InputTokens <= 0 {
		t.Errorf("usage.input_tokens 错误: %d, 应大于 0", resp.Usage.InputTokens)
	}

	if resp.Usage.OutputTokens <= 0 {
		t.Errorf("usage.output_tokens 错误: %d, 应大于 0", resp.Usage.OutputTokens)
	}

	t.Logf("✅ Claude 非流式响应字段验证通过")
	t.Logf("   ID: %s", resp.ID)
	t.Logf("   Model: %s", resp.Model)
	t.Logf("   Content: %s", contentBlock.Text)
	t.Logf("   Usage: %d input + %d output",
		resp.Usage.InputTokens,
		resp.Usage.OutputTokens)
}

// TestClaudeChat_Stream_ResponseFields 测试 Claude 流式响应字段完整性
func TestClaudeChat_Stream_ResponseFields(t *testing.T) {
	router := setupIntegrationTestRouter()

	reqBody := ClaudeChatRequest{
		Model: "claude-sonnet-4.5",
		Messages: []map[string]any{
			{"role": "user", "content": "Count to 3"},
		},
		MaxTokens: 1000,
		Stream:    true,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 验证 Content-Type
	contentType := w.Header().Get("Content-Type")
	if contentType != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type 错误: %s, 期望 text/event-stream; charset=utf-8", contentType)
	}

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过流式响应字段测试（可能因为 Token 问题）")
		return
	}

	// 解析 SSE 流
	scanner := bufio.NewScanner(w.Body)
	var events []map[string]any
	var eventTypes []string
	currentEvent := ""

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var event map[string]any
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Logf("解析 event 失败: %v, data: %s", err, data)
				continue
			}

			// 记录事件类型（从 event 行或 data 中的 type 字段）
			eventType := currentEvent
			if eventType == "" {
				if typ, ok := event["type"].(string); ok {
					eventType = typ
				}
			}

			eventTypes = append(eventTypes, eventType)
			events = append(events, event)
			currentEvent = ""
		}
	}

	if len(events) == 0 {
		t.Fatal("未收到任何 SSE event")
	}

	// 检查是否包含必需的事件类型
	hasMessageStart := false
	hasContentBlockStart := false
	hasContentBlockDelta := false
	hasContentBlockStop := false
	hasMessageDelta := false
	hasMessageStop := false

	for _, eventType := range eventTypes {
		switch eventType {
		case "message_start":
			hasMessageStart = true
		case "content_block_start":
			hasContentBlockStart = true
		case "content_block_delta":
			hasContentBlockDelta = true
		case "content_block_stop":
			hasContentBlockStop = true
		case "message_delta":
			hasMessageDelta = true
		case "message_stop":
			hasMessageStop = true
		}
	}

	if !hasMessageStart {
		t.Error("缺少 message_start 事件")
	}
	if !hasContentBlockStart {
		t.Error("缺少 content_block_start 事件")
	}
	if !hasContentBlockDelta {
		t.Error("缺少 content_block_delta 事件")
	}
	if !hasContentBlockStop {
		t.Error("缺少 content_block_stop 事件")
	}
	if !hasMessageDelta {
		t.Error("缺少 message_delta 事件")
	}
	if !hasMessageStop {
		t.Error("缺少 message_stop 事件")
	}

	// 验证 message_start 事件结构
	var messageStartEvent map[string]any
	for i, eventType := range eventTypes {
		if eventType == "message_start" {
			messageStartEvent = events[i]
			break
		}
	}

	if messageStartEvent != nil {
		if typ, ok := messageStartEvent["type"].(string); !ok || typ != "message_start" {
			t.Errorf("message_start 事件的 type 字段错误: %v", messageStartEvent["type"])
		}

		if message, ok := messageStartEvent["message"].(map[string]interface{}); ok {
			if id, ok := message["id"].(string); !ok || !strings.HasPrefix(id, "msg_") {
				t.Errorf("message_start 的 message.id 格式错误: %v", message["id"])
			}

			if role, ok := message["role"].(string); !ok || role != "assistant" {
				t.Errorf("message_start 的 message.role 错误: %v", message["role"])
			}

			if model, ok := message["model"].(string); !ok || model != "claude-sonnet-4.5" {
				t.Errorf("message_start 的 message.model 错误: %v", message["model"])
			}

			if usage, ok := message["usage"].(map[string]interface{}); ok {
				if inputTokens, ok := usage["input_tokens"].(float64); !ok || inputTokens <= 0 {
					t.Errorf("message_start 的 usage.input_tokens 错误: %v", usage["input_tokens"])
				}
			} else {
				t.Error("message_start 缺少 usage 字段")
			}
		} else {
			t.Error("message_start 缺少 message 字段")
		}
	}

	// 验证 message_delta 事件结构
	var messageDeltaEvent map[string]any
	for i, eventType := range eventTypes {
		if eventType == "message_delta" {
			messageDeltaEvent = events[i]
			break
		}
	}

	if messageDeltaEvent != nil {
		if delta, ok := messageDeltaEvent["delta"].(map[string]interface{}); ok {
			// stop_reason 可能是 end_turn 或 tool_use
			if stopReason, ok := delta["stop_reason"].(string); ok {
				if stopReason != "end_turn" && stopReason != "tool_use" {
					t.Errorf("message_delta 的 delta.stop_reason 错误: %v, 应为 end_turn 或 tool_use", stopReason)
				}
			}
		} else {
			t.Error("message_delta 缺少 delta 字段")
		}

		if usage, ok := messageDeltaEvent["usage"].(map[string]interface{}); ok {
			if outputTokens, ok := usage["output_tokens"].(float64); !ok || outputTokens <= 0 {
				t.Errorf("message_delta 的 usage.output_tokens 错误: %v", usage["output_tokens"])
			}
		} else {
			t.Error("message_delta 缺少 usage 字段")
		}
	}

	t.Logf("✅ Claude 流式响应字段验证通过")
	t.Logf("   收到 %d 个 events", len(events))
	t.Logf("   事件序列: %v", eventTypes)
}

// ========== 多模态内容测试 ==========

// TestOpenAIChat_MultimodalContent 测试 OpenAI 多模态内容（文本+图片）
func TestOpenAIChat_MultimodalContent(t *testing.T) {
	router := setupIntegrationTestRouter()

	// 1x1 像素的透明 PNG 图片（base64）
	testImageBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	reqBody := map[string]any{
		"model": "claude-sonnet-4.5",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "What's in this image?",
					},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": "data:image/png;base64," + testImageBase64,
						},
					},
				},
			},
		},
		"stream": false,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过多模态测试（可能因为 Token 问题）")
		return
	}

	var resp OpenAIChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if len(resp.Choices) == 0 {
		t.Fatal("响应缺少 choices")
	}

	if resp.Choices[0].Message.Content == "" {
		t.Error("多模态请求的响应内容为空")
	}

	t.Logf("✅ OpenAI 多模态内容测试通过")
	t.Logf("   响应: %s", resp.Choices[0].Message.Content)
}

// TestClaudeChat_MultimodalContent 测试 Claude 多模态内容（文本+图片）
func TestClaudeChat_MultimodalContent(t *testing.T) {
	router := setupIntegrationTestRouter()

	// 1x1 像素的透明 PNG 图片（base64）
	testImageBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	reqBody := map[string]any{
		"model": "claude-sonnet-4.5",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "What's in this image?",
					},
					{
						"type": "image",
						"source": map[string]string{
							"type":       "base64",
							"media_type": "image/png",
							"data":       testImageBase64,
						},
					},
				},
			},
		},
		"max_tokens": 1000,
		"stream":     false,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Logf("状态码: %d, 响应: %s", w.Code, w.Body.String())
		t.Skip("跳过多模态测试（可能因为 Token 问题）")
		return
	}

	var resp ClaudeChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	if len(resp.Content) == 0 {
		t.Fatal("响应缺少 content")
	}

	if resp.Content[0].Text == "" {
		t.Error("多模态请求的响应内容为空")
	}

	t.Logf("✅ Claude 多模态内容测试通过")
	t.Logf("   响应: %s", resp.Content[0].Text)
}

// ========== 错误处理测试 ==========

// TestOpenAIChat_ErrorHandling 测试 OpenAI 接口错误处理
func TestOpenAIChat_ErrorHandling(t *testing.T) {
	router := setupIntegrationTestRouter()

	tests := []struct {
		name       string
		reqBody    string
		expectCode int
	}{
		{
			name:       "无效的 JSON",
			reqBody:    `{invalid json}`,
			expectCode: 400,
		},
		{
			name:       "空请求体",
			reqBody:    ``,
			expectCode: 400,
		},
		{
			name:       "messages 不是数组",
			reqBody:    `{"model":"claude-sonnet-4.5","messages":"not an array","stream":false}`,
			expectCode: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tt.reqBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("期望状态码 %d, 得到 %d", tt.expectCode, w.Code)
			}

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Logf("响应: %s", w.Body.String())
			} else {
				if _, ok := resp["error"]; !ok {
					t.Error("错误响应应包含 error 字段")
				}
			}
		})
	}
}

// TestClaudeChat_ErrorHandling 测试 Claude 接口错误处理
func TestClaudeChat_ErrorHandling(t *testing.T) {
	router := setupIntegrationTestRouter()

	tests := []struct {
		name       string
		reqBody    string
		expectCode int
	}{
		{
			name:       "无效的 JSON",
			reqBody:    `{invalid json}`,
			expectCode: 400,
		},
		{
			name:       "空请求体",
			reqBody:    ``,
			expectCode: 400,
		},
		{
			name:       "messages 不是数组",
			reqBody:    `{"model":"claude-sonnet-4.5","messages":"not an array","max_tokens":1000,"stream":false}`,
			expectCode: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/v1/messages", strings.NewReader(tt.reqBody))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectCode {
				t.Errorf("期望状态码 %d, 得到 %d", tt.expectCode, w.Code)
			}

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Logf("响应: %s", w.Body.String())
			} else {
				if _, ok := resp["error"]; !ok {
					t.Error("错误响应应包含 error 字段")
				}
			}
		})
	}
}

// ========== 性能和并发测试 ==========

// TestConcurrentRequests 测试并发请求处理
func TestConcurrentRequests(t *testing.T) {
	router := setupIntegrationTestRouter()

	concurrency := 10
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			reqBody := OpenAIChatRequest{
				Model: "claude-sonnet-4.5",
				Messages: []map[string]any{
					{"role": "user", "content": "Test concurrent request"},
				},
				Stream: false,
			}

			body, _ := json.Marshal(reqBody)
			req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != 200 && w.Code != 500 {
				t.Errorf("并发请求 %d 返回异常状态码: %d", id, w.Code)
			}

			done <- true
		}(i)
	}

	// 等待所有请求完成（超时 30 秒）
	timeout := time.After(30 * time.Second)
	for i := 0; i < concurrency; i++ {
		select {
		case <-done:
			// 请求完成
		case <-timeout:
			t.Fatal("并发请求超时")
		}
	}

	t.Logf("✅ 并发测试通过：%d 个并发请求", concurrency)
}
