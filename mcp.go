package kiroclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// MCPClient MCP 客户端
type MCPClient struct {
	authManager *AuthManager
	httpClient  *http.Client
	machineID   string
	version     string
}

// NewMCPClient 创建 MCP 客户端
func NewMCPClient(authManager *AuthManager) *MCPClient {
	return &MCPClient{
		authManager: authManager,
		httpClient:  &http.Client{Timeout: 60 * time.Second},
		machineID:   generateMachineID(),
		version:     "0.8.140",
	}
}

// generateRequestID 生成请求 ID
func (c *MCPClient) generateRequestID() string {
	return uuid.New().String()
}

// CallMCP 调用 MCP API
func (c *MCPClient) CallMCP(method string, params any) (*MCPResponse, error) {
	token, err := c.authManager.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("获取 token 失败: %w", err)
	}

	// 构建请求
	req := MCPRequest{
		JSONRPC: "2.0",
		ID:      c.generateRequestID(),
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	// 确定 endpoint
	region := c.authManager.GetRegion()
	var endpoint string
	if region == "eu-central-1" {
		endpoint = "https://q.eu-central-1.amazonaws.com"
	} else {
		endpoint = "https://q.us-east-1.amazonaws.com"
	}

	url := endpoint + "/mcp"

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 设置请求头
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", fmt.Sprintf("KiroIDE %s %s", c.version, c.machineID))
	httpReq.Header.Set("X-Amz-User-Agent", "aws-sdk-js/3.x KiroIDE")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("请求失败 [%d]: %s", resp.StatusCode, string(bodyBytes))
	}

	var mcpResp MCPResponse
	if err := json.NewDecoder(resp.Body).Decode(&mcpResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	if mcpResp.Error != nil {
		return nil, fmt.Errorf("MCP 错误 [%d]: %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}

	return &mcpResp, nil
}

// ToolsList 获取工具列表
func (c *MCPClient) ToolsList() ([]MCPTool, error) {
	resp, err := c.CallMCP("tools/list", nil)
	if err != nil {
		return nil, err
	}

	if resp.Result == nil {
		return nil, fmt.Errorf("响应结果为空")
	}

	return resp.Result.Tools, nil
}

// ToolsCall 调用工具
func (c *MCPClient) ToolsCall(toolName string, arguments map[string]any) ([]MCPContent, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
	}

	resp, err := c.CallMCP("tools/call", params)
	if err != nil {
		return nil, err
	}

	if resp.Result == nil {
		return nil, fmt.Errorf("响应结果为空")
	}

	return resp.Result.Content, nil
}
