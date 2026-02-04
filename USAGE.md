# Kiro API Client 详细使用指南

本文档提供 Kiro API Client 的详细使用说明和最佳实践。

## 目录

1. [快速开始](#快速开始)
2. [Token 管理](#token-管理)
3. [MCP 协议调用](#mcp-协议调用)
4. [Web Search 功能](#web-search-功能)
5. [Chat 功能](#chat-功能)
6. [HTTP API 代理](#http-api-代理)
7. [命令行工具](#命令行工具)
8. [最佳实践](#最佳实践)
9. [故障排查](#故障排查)

---

## 快速开始

### 安装

```bash
go get github.com/jinfeijie/kiro-api-client-go
```

### 最简单的例子

```go
package main

import (
    "fmt"
    kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

func main() {
    client := kiroclient.NewKiroClient()
    response, _ := client.Chat.SimpleChat("你好")
    fmt.Println(response)
}
```

---

## Token 管理

### 1. Token 自动管理

客户端会自动从以下位置读取 Token：

- **Token 文件**: `~/.aws/sso/cache/kiro-auth-token.json`
- **Client 注册**: `~/.aws/sso/cache/{clientIdHash}.json`
- **Profile ARN**: `~/Library/Application Support/Kiro/User/globalStorage/kiro.kiroagent/profile.json`

**无需手动配置**，只要登录过 Kiro IDE，客户端就能自动读取这些文件。

### 2. Token 自动刷新

Token 会在过期前 5 分钟自动刷新，无需手动干预：

```go
client := kiroclient.NewKiroClient()

// GetAccessToken 会自动检查过期并刷新
// 你只需要调用这个方法，其他的都自动处理
token, err := client.Auth.GetAccessToken()
if err != nil {
    log.Fatal(err)
}
```

### 3. 手动管理 Token（高级用法）

如果需要更精细的控制，可以手动管理 Token：

```go
authManager := kiroclient.NewAuthManager()

// 1. 读取 Token
token, err := authManager.ReadToken()

// 2. 检查是否过期
if token.IsExpired() {
    fmt.Println("Token 已过期或即将过期（5分钟内）")
    
    // 3. 手动刷新
    err = authManager.RefreshToken()
}

// 4. 保存 Token（刷新后会自动保存，通常不需要手动调用）
err = authManager.SaveToken(token)
```

### 4. 环境变量配置

```bash
# 自定义 Token 路径
export KIRO_AUTH_TOKEN_PATH="/path/to/token.json"

# 直接设置 Access Token
export KIRO_ACCESS_TOKEN="your-access-token"

# 禁用自动刷新
export KIRO_AUTO_REFRESH=false

# 设置区域
export KIRO_REGION="eu-central-1"
```

---

## MCP 协议调用

### 1. 获取工具列表

```go
client := kiroclient.NewKiroClient()

// 获取所有可用的 MCP 工具
tools, err := client.MCP.ToolsList()
if err != nil {
    log.Fatal(err)
}

// 遍历工具列表
for _, tool := range tools {
    fmt.Printf("工具名称: %s\n", tool.Name)
    fmt.Printf("工具描述: %s\n", tool.Description)
    fmt.Printf("输入参数: %+v\n", tool.InputSchema)
}
```

### 2. 调用工具

```go
// 调用 web_search 工具进行搜索
content, err := client.MCP.ToolsCall("web_search", map[string]any{
    "query":      "Golang",      // 搜索关键词
    "maxResults": 10,            // 最多返回 10 条结果
})

if err != nil {
    log.Fatal(err)
}

// 解析返回的内容
for _, c := range content {
    fmt.Printf("内容类型: %s\n", c.Type)
    fmt.Printf("内容文本: %s\n", c.Text)
}
```

### 3. 直接调用 MCP API（高级用法）

```go
// 调用任意 MCP 方法
resp, err := client.MCP.CallMCP("tools/list", nil)

// 或者带参数调用
params := map[string]any{
    "name": "web_search",
    "arguments": map[string]any{
        "query": "test",
    },
}
resp, err := client.MCP.CallMCP("tools/call", params)
```

---

## Web Search 功能

### 1. 单个搜索

```go
client := kiroclient.NewKiroClient()

// 搜索并获取最多 10 条结果
results, err := client.Search.Search("Golang 最佳实践", 10)
if err != nil {
    log.Fatal(err)
}

// 遍历搜索结果
for _, r := range results {
    fmt.Printf("标题: %s\n", r.Title)
    fmt.Printf("链接: %s\n", r.URL)
    fmt.Printf("摘要: %s\n", r.Snippet)
    fmt.Printf("域名: %s\n", r.Domain)
    
    // 发布日期（可能为空）
    if r.PublishedDate != nil {
        fmt.Printf("发布时间: %d\n", *r.PublishedDate)
    }
    
    // 是否为公共域
    fmt.Printf("公共域: %v\n", r.IsPublicDomain)
}
```

### 2. 批量并发搜索

```go
// 同时搜索多个关键词（最大 10 个并发）
queries := []string{
    "Golang 并发编程",
    "Golang 性能优化",
    "Golang 微服务架构",
}

// 每个关键词返回最多 5 条结果
batchResults, err := client.Search.BatchSearch(queries, 5)
if err != nil {
    log.Fatal(err)
}

// 查看统计信息
fmt.Printf("成功: %d, 失败: %d\n", batchResults.Success, batchResults.Failed)

// 遍历每个查询的结果
for query, results := range batchResults.Results {
    fmt.Printf("\n关键词: %s\n", query)
    for _, r := range results {
        fmt.Printf("  - %s\n", r.Title)
    }
}
```

---

## Chat 功能

### 1. 简单聊天（非流式）

```go
client := kiroclient.NewKiroClient()

// 发送一个简单的问题，等待完整回答
response, err := client.Chat.SimpleChat("你好，请介绍一下自己")
if err != nil {
    log.Fatal(err)
}

// 打印完整回答
fmt.Println(response)
```

### 2. 流式聊天（实时输出）

```go
// 流式聊天会实时返回内容，适合长回答
err := client.Chat.SimpleChatStream("介绍一下 Golang", func(content string, done bool) {
    if done {
        fmt.Println() // 回答结束，换行
        return
    }
    fmt.Print(content) // 实时打印每一块内容
})

if err != nil {
    log.Fatal(err)
}
```

### 3. 多轮对话

```go
// 构建对话历史
messages := []kiroclient.ChatMessage{
    {Role: "user", Content: "我想学习编程"},
    {Role: "assistant", Content: "很好！你想学习哪种编程语言？"},
    {Role: "user", Content: "Golang"},
}

// 发送多轮对话
response, err := client.Chat.Chat(messages)
if err != nil {
    log.Fatal(err)
}

fmt.Println(response)
```

### 4. 高级流式对话

```go
// 多轮对话 + 流式输出
messages := []kiroclient.ChatMessage{
    {Role: "user", Content: "解释一下 Golang 的 goroutine"},
}

err := client.Chat.ChatStream(messages, func(content string, done bool) {
    if done {
        fmt.Println("\n[对话结束]")
        return
    }
    fmt.Print(content) // 实时输出
})
```

---

## HTTP API 代理

### 1. 启动服务器

```bash
cd kiro-api-client-go/server
go run main.go
```

或编译后运行：

```bash
go build -o kiro-proxy ./server/main.go
./kiro-proxy
```

服务器将在 `http://localhost:8080` 启动。

### 2. OpenAI 格式调用

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "stream": false
  }'
```

**流式响应**：

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "介绍一下 Golang"}
    ],
    "stream": true
  }'
```

### 3. Claude 格式调用

```bash
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "max_tokens": 1024,
    "stream": false
  }'
```

### 4. Anthropic 格式调用

```bash
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "你好"}
    ],
    "max_tokens": 1024
  }'
```

### 5. 在代码中使用

```go
import "net/http"

// OpenAI 格式
resp, err := http.Post(
    "http://localhost:8080/v1/chat/completions",
    "application/json",
    strings.NewReader(`{
        "model": "claude-sonnet-4.5",
        "messages": [{"role": "user", "content": "你好"}]
    }`),
)
```

---

## 命令行工具

### 1. 编译

```bash
cd kiro-api-client-go
go build -o kiroclient ./cmd/main.go
```

### 2. 聊天功能

```bash
# 非流式聊天
./kiroclient -cmd=chat -p="你好"

# 流式聊天
./kiroclient -cmd=chat -p="介绍一下 Golang" -stream
```

### 3. 搜索功能

```bash
./kiroclient -cmd=search -q="Golang 最佳实践"
```

### 4. 工具列表

```bash
./kiroclient -cmd=tools
```

---

## 最佳实践

### 1. 错误处理（推荐方式）

```go
client := kiroclient.NewKiroClient()

response, err := client.Chat.SimpleChat("你好")
if err != nil {
    // 记录详细错误日志（用于调试）
    log.Printf("聊天失败: %v", err)
    
    // 给用户友好的提示信息
    fmt.Println("抱歉，服务暂时不可用，请稍后重试")
    return
}

fmt.Println(response)
```

### 2. 超时控制

```go
// 默认超时时间：
// - AuthManager: 30 秒
// - MCPClient: 60 秒
// - ChatService: 120 秒

// 如需自定义超时，可以修改 httpClient
// 示例：创建自定义超时的客户端
import "time"

authManager := kiroclient.NewAuthManager()
authManager.httpClient.Timeout = 60 * time.Second  // 设置为 60 秒
```

### 3. 并发使用（线程安全）

```go
// 客户端是并发安全的，可以在多个 goroutine 中使用
client := kiroclient.NewKiroClient()

// 同时发起 10 个聊天请求
for i := 0; i < 10; i++ {
    go func(id int) {
        response, err := client.Chat.SimpleChat(fmt.Sprintf("问题 %d", id))
        if err != nil {
            log.Printf("请求 %d 失败: %v", id, err)
            return
        }
        fmt.Printf("回答 %d: %s\n", id, response)
    }(i)
}

// 等待所有请求完成
time.Sleep(30 * time.Second)
```

### 4. 资源管理

```go
// 客户端会自动管理 HTTP 连接池
// 通常无需手动关闭或清理

client := kiroclient.NewKiroClient()

// 如果需要，可以在程序退出时做清理
defer func() {
    // 这里可以添加清理逻辑
    // 但通常不需要，Go 会自动处理
}()

// 正常使用客户端
response, _ := client.Chat.SimpleChat("你好")
```

---

## 故障排查

### 1. Token 读取失败

**错误信息**: `读取 token 文件失败`

**可能原因**:
- Token 文件不存在
- 文件权限不正确
- 未登录 Kiro IDE

**解决方案**:
```bash
# 1. 检查文件是否存在
ls ~/.aws/sso/cache/kiro-auth-token.json

# 2. 检查文件权限
chmod 600 ~/.aws/sso/cache/kiro-auth-token.json

# 3. 确保已登录 Kiro IDE
# 打开 Kiro IDE，完成登录流程
```

### 2. Token 刷新失败

**错误信息**: `刷新 token 失败`

**可能原因**:
- 网络连接问题
- Client 注册文件不存在
- Refresh Token 已失效

**解决方案**:
```bash
# 1. 检查网络连接
ping oidc.us-east-1.amazonaws.com

# 2. 检查 Client 注册文件
ls ~/.aws/sso/cache/*.json

# 3. 重新登录 Kiro IDE
# 打开 Kiro IDE，退出登录后重新登录
```

### 3. ProfileArn 读取失败

**错误信息**: `获取 profileArn 失败`

**可能原因**:
- Profile 文件不存在
- Kiro IDE 未完成初始化

**解决方案**:
```bash
# 1. 检查文件路径
ls ~/Library/Application\ Support/Kiro/User/globalStorage/kiro.kiroagent/profile.json

# 2. 确保 Kiro IDE 已完成初始化
# 打开 Kiro IDE，等待初始化完成

# 3. 如果文件不存在，重新打开 Kiro IDE
```

### 4. MCP 调用失败

**错误信息**: `请求失败 [403]` 或 `请求失败 [401]`

**可能原因**:
- Token 无效或过期
- 区域设置不正确
- 权限不足

**解决方案**:
```go
// 1. 检查 Token 是否有效
authManager := kiroclient.NewAuthManager()
token, _ := authManager.ReadToken()
if token.IsExpired() {
    fmt.Println("Token 已过期，正在刷新...")
    authManager.RefreshToken()
}

// 2. 检查区域设置
region := authManager.GetRegion()
fmt.Printf("当前区域: %s\n", region)

// 3. 尝试手动刷新 Token
err := authManager.RefreshToken()
if err != nil {
    fmt.Printf("刷新失败: %v\n", err)
}
```

### 5. Chat 功能失败

**错误信息**: `请求失败 [500]` 或 `获取 profileArn 失败`

**可能原因**:
- ProfileArn 不正确
- 请求体格式错误
- 服务端问题

**解决方案**:
```go
// 1. 检查 ProfileArn
chatService := kiroclient.NewChatService(authManager)
arn, err := chatService.readProfileArn()
if err != nil {
    fmt.Printf("ProfileArn 读取失败: %v\n", err)
} else {
    fmt.Printf("ProfileArn: %s\n", arn)
}

// 2. 查看详细错误日志
response, err := client.Chat.SimpleChat("你好")
if err != nil {
    fmt.Printf("详细错误: %+v\n", err)
}

// 3. 尝试重新登录 Kiro IDE
```

### 6. 搜索结果解析失败

**错误信息**: `解析搜索结果失败`

**可能原因**:
- 返回格式变化
- JSON 解析错误

**解决方案**:
```go
// 使用 MCP 直接调用查看原始返回
content, err := client.MCP.ToolsCall("web_search", map[string]any{
    "query": "test",
    "maxResults": 1,
})

if err != nil {
    log.Fatal(err)
}

// 打印原始返回内容
fmt.Println("原始返回:")
fmt.Println(content[0].Text)
```

---

## 示例代码

完整的示例代码位于 `examples/` 目录：

- `chat_example.go` - Chat 功能示例
- `search_example.go` - Web Search 功能示例
- `mcp_example.go` - MCP 协议示例
- `token_example.go` - Token 管理示例

运行示例：

```bash
cd kiro-api-client-go/examples
go run chat_example.go
go run search_example.go
go run mcp_example.go
go run token_example.go
```

---

## 技术支持

如有问题，请查看：

- [README.md](README.md) - 快速开始指南
- [GitHub Issues](https://github.com/jinfeijie/kiro-api-client-go/issues) - 提交问题
- [设计文档](.kiro/specs/kiro-api-client/design.md) - 技术细节

---

## License

MIT
