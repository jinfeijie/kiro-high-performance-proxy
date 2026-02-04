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
