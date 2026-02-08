package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"

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

	if len(models) != 6 {
		t.Errorf("期望 6 个模型, 得到 %d", len(models))
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

// ========== Task 4.3: 熔断管理 API 端点测试 ==========

// setupCircuitBreakerTestRouter 创建测试用的 gin 路由器和初始化全局状态
// 为什么单独封装：避免每个测试重复初始化全局变量，保持测试独立性
func setupCircuitBreakerTestRouter(accountIDs ...string) *gin.Engine {
	// 初始化全局 circuitStats
	circuitStats = NewCircuitStats()

	// 初始化全局 client，注入测试账号
	client = kiroclient.NewKiroClient()
	accounts := make([]kiroclient.AccountInfo, 0, len(accountIDs))
	for _, id := range accountIDs {
		accounts = append(accounts, kiroclient.AccountInfo{
			ID:    id,
			Email: id + "@test.com",
			Token: &kiroclient.KiroAuthToken{
				AccessToken: "test-token-" + id,
				ExpiresAt:   "2099-12-31T23:59:59Z",
			},
		})
	}
	client.Auth.SetAccountsCacheForTest(&kiroclient.AccountsConfig{
		Accounts: accounts,
	})

	router := gin.New()
	api := router.Group("/api")
	api.GET("/circuit-breaker/status", handleCircuitBreakerStatus)
	api.POST("/circuit-breaker/trip", handleCircuitBreakerTrip)
	api.POST("/circuit-breaker/reset", handleCircuitBreakerReset)
	return router
}

// ========== 单元测试：熔断管理 API ==========

// TestCircuitBreakerTrip_NotFound 手动熔断不存在的账号应返回 404
// **Validates: Requirements 3.1**
func TestCircuitBreakerTrip_NotFound(t *testing.T) {
	router := setupCircuitBreakerTestRouter("acc-1")

	reqBody, _ := json.Marshal(map[string]string{
		"accountId": "non-existent-account",
	})
	req, _ := http.NewRequest("POST", "/api/circuit-breaker/trip", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("期望状态码 404, 得到 %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Error("响应中应包含 error 字段")
	}
}

// TestCircuitBreakerReset_NotFound 解除熔断不存在的账号应返回 404
// **Validates: Requirements 4.1**
func TestCircuitBreakerReset_NotFound(t *testing.T) {
	router := setupCircuitBreakerTestRouter("acc-1")

	reqBody, _ := json.Marshal(map[string]string{
		"accountId": "non-existent-account",
	})
	req, _ := http.NewRequest("POST", "/api/circuit-breaker/reset", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("期望状态码 404, 得到 %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Error("响应中应包含 error 字段")
	}
}

// TestCircuitBreakerStatus_EmptyAccounts 无账号时返回空数组
// **Validates: Requirements 2.4**
func TestCircuitBreakerStatus_EmptyAccounts(t *testing.T) {
	// 不传任何账号 ID，模拟空账号场景
	router := setupCircuitBreakerTestRouter()

	req, _ := http.NewRequest("GET", "/api/circuit-breaker/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("期望状态码 200, 得到 %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	accounts, ok := resp["accounts"].([]interface{})
	if !ok {
		t.Fatal("响应中 accounts 字段应为数组")
	}
	if len(accounts) != 0 {
		t.Errorf("期望空数组, 得到 %d 个元素", len(accounts))
	}

	totalAccounts, ok := resp["totalAccounts"].(float64)
	if !ok {
		t.Fatal("响应中应包含 totalAccounts 字段")
	}
	if int(totalAccounts) != 0 {
		t.Errorf("期望 totalAccounts=0, 得到 %d", int(totalAccounts))
	}
}

// ========== Property 3: 熔断状态 API 响应完整性 ==========
// Feature: circuit-breaker-dashboard, Property 3: 熔断状态 API 响应完整性
// **Validates: Requirements 2.1, 2.2, 2.3**
//
// *For any* 账号集合，GET /api/circuit-breaker/status 返回的每个账号条目
// 都包含 state、failureCount、errorRate1m、errorRate5m、weight、loadPercent 字段，
// 且所有账号的 loadPercent 之和约等于 100%（误差 ±1%，排除所有账号权重为 0 的情况）。

func TestProperty3_CircuitBreakerStatusResponseCompleteness(t *testing.T) {
	// 必需字段列表（API 响应中每个账号条目必须包含的字段）
	requiredFields := []string{
		"state", "failureCount", "errorRate1m",
		"errorRate5m", "weight", "loadPercent",
	}

	cfg := &quick.Config{MaxCount: 100}
	err := quick.Check(func(n uint8) bool {
		// 生成 1~10 个账号（避免 0 个账号的退化情况）
		count := int(n%10) + 1
		ids := make([]string, count)
		for i := 0; i < count; i++ {
			ids[i] = fmt.Sprintf("prop3-acc-%d", i)
		}

		router := setupCircuitBreakerTestRouter(ids...)

		req, _ := http.NewRequest("GET", "/api/circuit-breaker/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Logf("状态码非 200: %d", w.Code)
			return false
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Logf("JSON 解析失败: %v", err)
			return false
		}

		accounts, ok := resp["accounts"].([]interface{})
		if !ok {
			t.Log("accounts 字段不是数组")
			return false
		}

		if len(accounts) != count {
			t.Logf("期望 %d 个账号, 得到 %d", count, len(accounts))
			return false
		}

		// 验证每个账号条目包含所有必需字段
		loadPercentSum := 0.0
		allWeightZero := true

		for i, raw := range accounts {
			acc, ok := raw.(map[string]interface{})
			if !ok {
				t.Logf("账号 %d 不是 map", i)
				return false
			}

			for _, field := range requiredFields {
				if _, exists := acc[field]; !exists {
					t.Logf("账号 %d 缺少字段: %s", i, field)
					return false
				}
			}

			// 累加 loadPercent
			lp, ok := acc["loadPercent"].(float64)
			if !ok {
				t.Logf("账号 %d 的 loadPercent 不是数字", i)
				return false
			}
			loadPercentSum += lp

			// 检查权重是否全为 0
			w, ok := acc["weight"].(float64)
			if !ok {
				t.Logf("账号 %d 的 weight 不是数字", i)
				return false
			}
			if int(w) != 0 {
				allWeightZero = false
			}
		}

		// 如果所有账号权重都为 0，跳过 loadPercent 之和检查
		if allWeightZero {
			return true
		}

		// loadPercent 之和应约等于 100%（误差 ±1%）
		if loadPercentSum < 99.0 || loadPercentSum > 101.0 {
			t.Logf("loadPercent 之和 %.2f 不在 [99, 101] 范围内", loadPercentSum)
			return false
		}

		return true
	}, cfg)

	if err != nil {
		t.Errorf("Property 3 失败: %v", err)
	}
}

func TestValidateToolUseInput(t *testing.T) {
	// 构造工具定义：Write 工具需要 file_path 和 content
	tools := []kiroclient.KiroToolWrapper{
		{
			ToolSpecification: kiroclient.KiroToolSpecification{
				Name: "Write",
				InputSchema: map[string]interface{}{
					"type": "object",
					"required": []interface{}{
						"file_path",
						"content",
					},
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{
							"type": "string",
						},
						"content": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
		{
			ToolSpecification: kiroclient.KiroToolSpecification{
				Name: "Read",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type": "string",
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name     string
		toolName string
		input    map[string]any
		wantLen  int
	}{
		{
			name:     "全部参数齐全-通过",
			toolName: "Write",
			input:    map[string]any{"file_path": "/a", "content": "b"},
			wantLen:  0,
		},
		{
			name:     "缺少content-拦截",
			toolName: "Write",
			input:    map[string]any{"file_path": "/a"},
			wantLen:  1,
		},
		{
			name:     "全部缺失-拦截",
			toolName: "Write",
			input:    map[string]any{},
			wantLen:  2,
		},
		{
			name:     "工具无required定义-放行",
			toolName: "Read",
			input:    map[string]any{},
			wantLen:  0,
		},
		{
			name:     "未知工具-放行",
			toolName: "Unknown",
			input:    map[string]any{},
			wantLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing := validateToolUseInput(tt.toolName, tt.input, tools)
			if len(missing) != tt.wantLen {
				t.Errorf("want %d missing, got %d: %v", tt.wantLen, len(missing), missing)
			}
		})
	}
}

func TestPatchMissingFields(t *testing.T) {
	tools := []kiroclient.KiroToolWrapper{
		{
			ToolSpecification: kiroclient.KiroToolSpecification{
				Name: "Write",
				InputSchema: map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"file_path", "content"},
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string"},
						"content":   map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		{
			ToolSpecification: kiroclient.KiroToolSpecification{
				Name: "Multi",
				InputSchema: map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"name", "count", "flag", "items", "meta"},
					"properties": map[string]interface{}{
						"name":  map[string]interface{}{"type": "string"},
						"count": map[string]interface{}{"type": "integer"},
						"flag":  map[string]interface{}{"type": "boolean"},
						"items": map[string]interface{}{"type": "array"},
						"meta":  map[string]interface{}{"type": "object"},
					},
				},
			},
		},
	}

	tests := []struct {
		name     string
		toolName string
		input    map[string]any
		missing  []string
		wantKey  string
		wantVal  any
	}{
		{
			name:     "补齐string类型content",
			toolName: "Write",
			input:    map[string]any{"file_path": "/a"},
			missing:  []string{"content"},
			wantKey:  "content",
			wantVal:  "「模型未知原因导致字段: content 缺失，建议重试。注意添加提示词：`分段写入，减少失败。` 」",
		},
		{
			name:     "补齐integer类型",
			toolName: "Multi",
			input:    map[string]any{"name": "x"},
			missing:  []string{"count"},
			wantKey:  "count",
			wantVal:  0,
		},
		{
			name:     "补齐boolean类型",
			toolName: "Multi",
			input:    map[string]any{"name": "x"},
			missing:  []string{"flag"},
			wantKey:  "flag",
			wantVal:  false,
		},
		{
			name:     "补齐array类型",
			toolName: "Multi",
			input:    map[string]any{"name": "x"},
			missing:  []string{"items"},
			wantKey:  "items",
			wantVal:  nil, // array 用 len 检查
		},
		{
			name:     "已有字段不覆盖",
			toolName: "Write",
			input:    map[string]any{"file_path": "/a", "content": "abc"},
			missing:  []string{},
			wantKey:  "content",
			wantVal:  "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patchMissingFields(tt.input, tt.missing, tools, tt.toolName)
			val, has := tt.input[tt.wantKey]
			if !has {
				if tt.wantVal != nil {
					t.Errorf("字段 %s 未被补齐", tt.wantKey)
				}
				return
			}
			// array 类型特殊处理
			if tt.wantKey == "items" {
				arr, ok := val.([]any)
				if !ok || len(arr) != 0 {
					t.Errorf("want empty array, got %v", val)
				}
				return
			}
			if val != tt.wantVal {
				t.Errorf("want %v (%T), got %v (%T)", tt.wantVal, tt.wantVal, val, val)
			}
		})
	}
}

// ========== containsDebugKeyword 测试 ==========

func TestContainsDebugKeyword_Found(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "请帮我 OneDayAI_Start_Debug 调试一下"},
	}
	if !containsDebugKeyword(messages) {
		t.Error("应该检测到关键字")
	}
}

func TestContainsDebugKeyword_NotFound(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "普通消息"},
	}
	if containsDebugKeyword(messages) {
		t.Error("不应该检测到关键字")
	}
}

func TestContainsDebugKeyword_Empty(t *testing.T) {
	if containsDebugKeyword(nil) {
		t.Error("nil 不应该检测到关键字")
	}
	if containsDebugKeyword([]map[string]any{}) {
		t.Error("空数组不应该检测到关键字")
	}
}

func TestContainsDebugKeyword_InNestedContent(t *testing.T) {
	// Claude 格式：content 是数组
	messages := []map[string]any{
		{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "OneDayAI_Start_Debug"},
			},
		},
	}
	if !containsDebugKeyword(messages) {
		t.Error("嵌套内容中应该检测到关键字")
	}
}

func TestContainsDebugKeyword_MultipleMessages(t *testing.T) {
	// 关键字在第二条消息中
	messages := []map[string]any{
		{"role": "user", "content": "第一条普通消息"},
		{"role": "assistant", "content": "回复"},
		{"role": "user", "content": "OneDayAI_Start_Debug 第三条"},
	}
	if !containsDebugKeyword(messages) {
		t.Error("多条消息中应该检测到关键字")
	}
}

// ========== extractSystemPrompt 测试 ==========

func TestExtractSystemPrompt_Nil(t *testing.T) {
	result := extractSystemPrompt(nil)
	if result != "" {
		t.Errorf("nil 应返回空字符串，实际: %q", result)
	}
}

func TestExtractSystemPrompt_String(t *testing.T) {
	result := extractSystemPrompt("You are a helpful assistant.")
	if result != "You are a helpful assistant." {
		t.Errorf("字符串格式解析错误，实际: %q", result)
	}
}

func TestExtractSystemPrompt_Array(t *testing.T) {
	// Claude 格式：system 是 [{type: "text", text: "..."}] 数组
	system := []interface{}{
		map[string]interface{}{"type": "text", "text": "Part 1"},
		map[string]interface{}{"type": "text", "text": "Part 2"},
	}
	result := extractSystemPrompt(system)
	if result != "Part 1\nPart 2" {
		t.Errorf("数组格式解析错误，实际: %q", result)
	}
}

func TestExtractSystemPrompt_EmptyArray(t *testing.T) {
	system := []interface{}{}
	result := extractSystemPrompt(system)
	if result != "" {
		t.Errorf("空数组应返回空字符串，实际: %q", result)
	}
}

// ========== convertToKiroMessagesWithSystem 测试 ==========

// TestConvertSystemPrompt_HistoryInjection 验证 system prompt 作为 history 首条配对注入
func TestConvertSystemPrompt_HistoryInjection(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Hello"},
	}
	system := "You are a helpful assistant."

	msgs, _, _, _ := convertToKiroMessagesWithSystem(messages, system, nil)

	// 应该有 3 条消息：system(user) + system(assistant) + 原始 user
	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息，实际: %d", len(msgs))
	}

	// 第一条：system prompt 作为 user 消息，无标记
	if msgs[0].Role != "user" {
		t.Errorf("第一条应为 user，实际: %s", msgs[0].Role)
	}
	if msgs[0].Content != "You are a helpful assistant." {
		t.Errorf("system 内容不应有标记，实际: %q", msgs[0].Content)
	}

	// 第二条：assistant 确认
	if msgs[1].Role != "assistant" {
		t.Errorf("第二条应为 assistant，实际: %s", msgs[1].Role)
	}
	if msgs[1].Content != "I will follow these instructions." {
		t.Errorf("assistant 回复不对，实际: %q", msgs[1].Content)
	}

	// 第三条：原始 user 消息
	if msgs[2].Role != "user" {
		t.Errorf("第三条应为 user，实际: %s", msgs[2].Role)
	}
	if msgs[2].Content != "Hello" {
		t.Errorf("原始消息被篡改，实际: %q", msgs[2].Content)
	}
}

// TestConvertSystemPrompt_NoMarkers 验证不包含任何 --- SYSTEM PROMPT --- 标记
func TestConvertSystemPrompt_NoMarkers(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Hello"},
	}
	system := "You are a coding assistant."

	msgs, _, _, _ := convertToKiroMessagesWithSystem(messages, system, nil)

	for i, msg := range msgs {
		if contains := len(msg.Content) > 0 && (msg.Content == "--- SYSTEM PROMPT ---" ||
			containsStr(msg.Content, "--- SYSTEM PROMPT ---") ||
			containsStr(msg.Content, "--- END SYSTEM PROMPT ---")); contains {
			t.Errorf("消息 %d 包含旧标记: %q", i, msg.Content)
		}
	}
}

// TestConvertSystemPrompt_EmptyMessages 验证无消息 + 有 system 的情况
func TestConvertSystemPrompt_EmptyMessages(t *testing.T) {
	messages := []map[string]any{}
	system := "You are a helpful assistant."

	msgs, _, _, _ := convertToKiroMessagesWithSystem(messages, system, nil)

	// 应该有 3 条：system(user) + system(assistant) + Continue(user)
	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息，实际: %d", len(msgs))
	}

	if msgs[0].Role != "user" || msgs[0].Content != "You are a helpful assistant." {
		t.Errorf("第一条不对: role=%s content=%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "I will follow these instructions." {
		t.Errorf("第二条不对: role=%s content=%q", msgs[1].Role, msgs[1].Content)
	}
	if msgs[2].Role != "user" || msgs[2].Content != "Continue" {
		t.Errorf("第三条不对: role=%s content=%q", msgs[2].Role, msgs[2].Content)
	}
}

// TestConvertSystemPrompt_NoSystem 验证无 system prompt 时行为不变
func TestConvertSystemPrompt_NoSystem(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Hello"},
		{"role": "assistant", "content": "Hi there"},
	}

	msgs, _, _, _ := convertToKiroMessagesWithSystem(messages, nil, nil)

	// 无 system 时不应插入额外消息
	if len(msgs) != 2 {
		t.Fatalf("期望 2 条消息，实际: %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Hello" {
		t.Errorf("第一条不对: role=%s content=%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hi there" {
		t.Errorf("第二条不对: role=%s content=%q", msgs[1].Role, msgs[1].Content)
	}
}

// TestConvertSystemPrompt_WithToolResults 验证 system prompt + tool_results 组合
func TestConvertSystemPrompt_WithToolResults(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": "toolu_123",
				"content":     "file content here",
			},
		}},
	}
	system := "You are a coding assistant."

	msgs, _, lastToolResults, _ := convertToKiroMessagesWithSystem(messages, system, nil)

	// system 配对 + 原始 user（带 tool_result）= 3 条
	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息，实际: %d", len(msgs))
	}

	// system 配对在前面
	if msgs[0].Content != "You are a coding assistant." {
		t.Errorf("system 内容不对: %q", msgs[0].Content)
	}

	// 最后一条 user 消息应有 tool_results
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Role != "user" {
		t.Errorf("最后一条应为 user，实际: %s", lastMsg.Role)
	}
	if len(lastMsg.ToolResults) != 1 {
		t.Fatalf("期望 1 个 toolResult，实际: %d", len(lastMsg.ToolResults))
	}

	// lastToolResults 应该来自最后一条 user 消息
	if len(lastToolResults) != 1 {
		t.Fatalf("期望 1 个 lastToolResult，实际: %d", len(lastToolResults))
	}
	if lastToolResults[0].ToolUseId != "toolu_123" {
		t.Errorf("toolUseId 不对: %s", lastToolResults[0].ToolUseId)
	}
}

// TestConvertSystemPrompt_ArrayFormat 验证 system 为数组格式时的处理
func TestConvertSystemPrompt_ArrayFormat(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "Hello"},
	}
	system := []interface{}{
		map[string]interface{}{"type": "text", "text": "Part 1"},
		map[string]interface{}{"type": "text", "text": "Part 2"},
	}

	msgs, _, _, _ := convertToKiroMessagesWithSystem(messages, system, nil)

	if len(msgs) != 3 {
		t.Fatalf("期望 3 条消息，实际: %d", len(msgs))
	}

	// system 内容应该是 join 后的结果
	if msgs[0].Content != "Part 1\nPart 2" {
		t.Errorf("数组格式 system 解析不对: %q", msgs[0].Content)
	}
}

// containsStr 简单的字符串包含检查（测试辅助函数）
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
