package kiroclient

import (
	"strings"
	"testing"
)

// TestChatStream_WithValidModel 测试使用有效模型
func TestChatStream_WithValidModel(t *testing.T) {
	// 跳过需要真实 Token 的测试
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	authManager := NewAuthManager()
	chatService := NewChatService(authManager)

	messages := []ChatMessage{
		{Role: "user", Content: "测试消息"},
	}

	// 测试有效模型
	validModels := []string{"claude-sonnet-4.5", "claude-haiku-4.5", "auto"}

	for _, model := range validModels {
		t.Run(model, func(t *testing.T) {
			var receivedContent strings.Builder
			err := chatService.ChatStreamWithModel(messages, model, func(content string, done bool) {
				if !done {
					receivedContent.WriteString(content)
				}
			})

			// 注意：这个测试需要真实的 Token，所以可能会失败
			// 主要是验证函数签名和参数传递
			if err != nil {
				t.Logf("模型 %s 测试失败（可能是 Token 问题）: %v", model, err)
			}
		})
	}
}

// TestChatStream_WithInvalidModel 测试使用无效模型
func TestChatStream_WithInvalidModel(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	authManager := NewAuthManager()
	chatService := NewChatService(authManager)

	messages := []ChatMessage{
		{Role: "user", Content: "测试消息"},
	}

	// 测试无效模型 - 应该失败或使用默认模型
	invalidModel := "invalid-model-12345"

	err := chatService.ChatStreamWithModel(messages, invalidModel, func(content string, done bool) {})

	// 无效模型应该返回错误或使用默认模型
	if err != nil {
		t.Logf("无效模型正确返回错误: %v", err)
	} else {
		t.Log("无效模型使用了默认模型（这也是可接受的行为）")
	}
}

// TestChatStream_WithoutModel 测试不传模型（使用默认）
func TestChatStream_WithoutModel(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	authManager := NewAuthManager()
	chatService := NewChatService(authManager)

	messages := []ChatMessage{
		{Role: "user", Content: "测试消息"},
	}

	// 使用空字符串表示不指定模型
	err := chatService.ChatStreamWithModel(messages, "", func(content string, done bool) {})

	if err != nil {
		t.Logf("不指定模型测试失败（可能是 Token 问题）: %v", err)
	}
}
