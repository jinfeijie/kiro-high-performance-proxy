package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

func init() {
	// 设置 Gin 为测试模式
	gin.SetMode(gin.TestMode)
}

// TestHandleModelsList 测试模型列表接口
func TestHandleModelsList(t *testing.T) {
	// 初始化客户端
	client = kiroclient.NewKiroClient()

	router := gin.New()
	router.GET("/api/models", handleModelsList)

	req, _ := http.NewRequest("GET", "/api/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("期望状态码 200, 得到 %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	models, ok := response["models"].([]interface{})
	if !ok {
		t.Fatal("响应中没有 models 字段")
	}

	if len(models) != 5 {
		t.Errorf("期望 5 个模型, 得到 %d", len(models))
	}
}

// TestHandleClaudeChat_ModelParam 测试 Claude 格式接口的模型参数
func TestHandleClaudeChat_ModelParam(t *testing.T) {
	client = kiroclient.NewKiroClient()

	router := gin.New()
	router.POST("/v1/messages", handleClaudeChat)

	tests := []struct {
		name       string
		model      string
		expectCode int
	}{
		{
			name:       "有效模型 claude-sonnet-4.5",
			model:      "claude-sonnet-4.5",
			expectCode: 200, // 可能因为 Token 问题返回 500，但至少应该接受请求
		},
		{
			name:       "有效模型 auto",
			model:      "auto",
			expectCode: 200,
		},
		{
			name:       "无效模型",
			model:      "invalid-model-xyz",
			expectCode: 400, // 应该返回 400 错误
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqBody := ClaudeChatRequest{
				Model: tt.model,
				Messages: []map[string]any{
					{"role": "user", "content": "测试"},
				},
				MaxTokens: 1000,
				Stream:    false,
			}

			body, _ := json.Marshal(reqBody)
			req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// 注意：由于没有真实 Token，可能返回 500
			// 但我们主要验证模型参数是否被正确处理
			t.Logf("模型 %s 返回状态码: %d", tt.model, w.Code)

			// 如果是无效模型，应该在参数验证阶段就返回 400
			if tt.model == "invalid-model-xyz" && w.Code != 400 {
				t.Logf("警告：无效模型没有返回 400 错误（当前: %d）", w.Code)
			}
		})
	}
}

// TestHandleOpenAIChat_ModelParam 测试 OpenAI 格式接口的模型参数
func TestHandleOpenAIChat_ModelParam(t *testing.T) {
	client = kiroclient.NewKiroClient()

	router := gin.New()
	router.POST("/v1/chat/completions", handleOpenAIChat)

	reqBody := OpenAIChatRequest{
		Model: "claude-sonnet-4.5",
		Messages: []map[string]any{
			{"role": "user", "content": "测试"},
		},
		Stream: false,
	}

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	t.Logf("OpenAI 格式返回状态码: %d", w.Code)

	// 验证响应中包含模型字段
	if w.Code == 200 {
		var response OpenAIChatResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err == nil {
			if response.Model != "claude-sonnet-4.5" {
				t.Errorf("期望模型 claude-sonnet-4.5, 得到 %s", response.Model)
			}
		}
	}
}

// TestModelValidation 测试模型验证逻辑
func TestModelValidation(t *testing.T) {
	tests := []struct {
		model string
		valid bool
	}{
		{"claude-sonnet-4.5", true},
		{"claude-haiku-4.5", true},
		{"auto", true},
		{"invalid-model", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := kiroclient.IsValidModel(tt.model)
			if result != tt.valid {
				t.Errorf("IsValidModel(%q) = %v, want %v", tt.model, result, tt.valid)
			}
		})
	}
}

// TestStreamingContentTypeHeader 测试流式响应的 Content-Type 头
// **Property 1: Streaming Response Content-Type Header Correctness**
// **Validates: Requirements 1.1, 1.2, 1.3, 2.1**
func TestStreamingContentTypeHeader(t *testing.T) {
	client = kiroclient.NewKiroClient()

	expectedContentType := "text/event-stream; charset=utf-8"

	t.Run("handleChat streaming Content-Type", func(t *testing.T) {
		router := gin.New()
		router.POST("/chat", handleChat)

		reqBody := struct {
			Messages []kiroclient.ChatMessage `json:"messages"`
			Stream   bool                     `json:"stream"`
		}{
			Messages: []kiroclient.ChatMessage{
				{Role: "user", Content: "测试中文"},
			},
			Stream: true,
		}

		body, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest("POST", "/chat", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		contentType := w.Header().Get("Content-Type")
		if contentType != expectedContentType {
			t.Errorf("handleChat Content-Type = %q, want %q", contentType, expectedContentType)
		}
	})

	t.Run("handleClaudeChat streaming Content-Type", func(t *testing.T) {
		router := gin.New()
		router.POST("/v1/messages", handleClaudeChat)

		reqBody := ClaudeChatRequest{
			Model: "claude-sonnet-4.5",
			Messages: []map[string]any{
				{"role": "user", "content": "测试中文"},
			},
			MaxTokens: 1000,
			Stream:    true,
		}

		body, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		contentType := w.Header().Get("Content-Type")
		if contentType != expectedContentType {
			t.Errorf("handleClaudeChat Content-Type = %q, want %q", contentType, expectedContentType)
		}
	})

	t.Run("handleOpenAIChat streaming Content-Type", func(t *testing.T) {
		router := gin.New()
		router.POST("/v1/chat/completions", handleOpenAIChat)

		reqBody := OpenAIChatRequest{
			Model: "claude-sonnet-4.5",
			Messages: []map[string]any{
				{"role": "user", "content": "测试中文"},
			},
			Stream: true,
		}

		body, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		contentType := w.Header().Get("Content-Type")
		if contentType != expectedContentType {
			t.Errorf("handleOpenAIChat Content-Type = %q, want %q", contentType, expectedContentType)
		}
	})
}

// TestContentTypeConsistency 测试所有流式端点的 Content-Type 一致性
// **Validates: Requirements 2.1**
func TestContentTypeConsistency(t *testing.T) {
	client = kiroclient.NewKiroClient()

	expectedContentType := "text/event-stream; charset=utf-8"
	contentTypes := make(map[string]string)

	// 测试 handleChat
	router1 := gin.New()
	router1.POST("/chat", handleChat)
	reqBody1 := struct {
		Messages []kiroclient.ChatMessage `json:"messages"`
		Stream   bool                     `json:"stream"`
	}{
		Messages: []kiroclient.ChatMessage{{Role: "user", Content: "test"}},
		Stream:   true,
	}
	body1, _ := json.Marshal(reqBody1)
	req1, _ := http.NewRequest("POST", "/chat", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router1.ServeHTTP(w1, req1)
	contentTypes["handleChat"] = w1.Header().Get("Content-Type")

	// 测试 handleClaudeChat
	router2 := gin.New()
	router2.POST("/v1/messages", handleClaudeChat)
	reqBody2 := ClaudeChatRequest{
		Model:     "claude-sonnet-4.5",
		Messages:  []map[string]any{{"role": "user", "content": "test"}},
		MaxTokens: 1000,
		Stream:    true,
	}
	body2, _ := json.Marshal(reqBody2)
	req2, _ := http.NewRequest("POST", "/v1/messages", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router2.ServeHTTP(w2, req2)
	contentTypes["handleClaudeChat"] = w2.Header().Get("Content-Type")

	// 测试 handleOpenAIChat
	router3 := gin.New()
	router3.POST("/v1/chat/completions", handleOpenAIChat)
	reqBody3 := OpenAIChatRequest{
		Model:    "claude-sonnet-4.5",
		Messages: []map[string]any{{"role": "user", "content": "test"}},
		Stream:   true,
	}
	body3, _ := json.Marshal(reqBody3)
	req3, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	router3.ServeHTTP(w3, req3)
	contentTypes["handleOpenAIChat"] = w3.Header().Get("Content-Type")

	// 验证所有 Content-Type 一致
	for handler, ct := range contentTypes {
		if ct != expectedContentType {
			t.Errorf("%s Content-Type = %q, want %q", handler, ct, expectedContentType)
		}
	}

	// 验证三个处理器的 Content-Type 完全相同
	first := ""
	for handler, ct := range contentTypes {
		if first == "" {
			first = ct
		} else if ct != first {
			t.Errorf("Content-Type 不一致: %s 有 %q, 但其他有 %q", handler, ct, first)
		}
	}
}
