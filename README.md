# Kiro API Client (Go)

基于 Kiro.app 逆向工程实现的 Golang 客户端，支持 Token 认证、自动刷新、MCP 协议调用和 Chat 功能。

## 功能特性

- ✅ **Token 管理**: 自动读取、刷新和保存 Kiro 认证 Token
- ✅ **MCP 协议**: 完整实现 JSON-RPC 2.0 MCP 协议
- ✅ **Web Search**: 支持单个和批量并发搜索
- ✅ **Chat 功能**: 支持流式和非流式聊天，完整实现 EventStream 协议
- ✅ **HTTP API 代理**: 提供 OpenAI/Claude/Anthropic 格式的 HTTP 接口

## 快速开始

### 安装

```bash
go get github.com/jinfeijie/kiro-api-client-go
```

### 使用示例

#### 1. 聊天功能

```go
package main

import (
    "fmt"
    kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

func main() {
    // 创建客户端
    client := kiroclient.NewKiroClient()
    
    // 简单聊天（非流式）
    response, err := client.Chat.SimpleChat("你好")
    if err != nil {
        panic(err)
    }
    fmt.Println(response)
    
    // 流式聊天（实时输出）
    err = client.Chat.SimpleChatStream("介绍一下自己", func(content string, done bool) {
        if done {
            fmt.Println() // 结束时换行
            return
        }
        fmt.Print(content) // 实时打印内容
    })
}
```

#### 2. Web 搜索

```go
// 创建客户端
client := kiroclient.NewKiroClient()

// 单个搜索（最多返回 10 条结果）
results, err := client.Search.Search("Golang", 10)
if err != nil {
    panic(err)
}

// 遍历搜索结果
for _, r := range results {
    fmt.Printf("%s\n%s\n\n", r.Title, r.URL)
}

// 批量并发搜索（同时搜索多个关键词）
queries := []string{"Golang", "Rust", "Python"}
batchResults, err := client.Search.BatchSearch(queries, 5)
```

#### 3. MCP 工具调用

```go
// 创建客户端
client := kiroclient.NewKiroClient()

// 获取可用工具列表
tools, err := client.MCP.ToolsList()

// 调用 web_search 工具
content, err := client.MCP.ToolsCall("web_search", map[string]any{
    "query": "Golang",
    "maxResults": 10,
})
```

## 命令行工具

### 编译

```bash
cd kiro-api-client-go
go build -o kiroclient ./cmd/main.go
```

### 使用

```bash
# 聊天（非流式）
./kiroclient -cmd=chat -p="你好"

# 流式聊天（实时输出）
./kiroclient -cmd=chat -p="介绍一下自己" -stream

# Web 搜索
./kiroclient -cmd=search -q="Golang"

# 查看可用工具列表
./kiroclient -cmd=tools
```

## HTTP API 代理服务器

提供兼容 OpenAI/Claude/Anthropic 格式的 HTTP 接口，可用于集成到现有应用。

### 启动服务器

```bash
# 进入服务器目录
cd kiro-api-client-go/server

# 运行服务器
go run main.go

# 或者编译后运行
go build -o kiro-proxy main.go
./kiro-proxy
```

服务器将在 `http://localhost:8080` 启动，提供 OpenAI/Claude/Anthropic 兼容的 HTTP 接口。

### API 接口

#### 1. OpenAI 格式（非流式）

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'
```

#### 2. Claude 格式（非流式）

```bash
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 1024
  }'
```

#### 3. Anthropic 格式（非流式）

```bash
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 1024
  }'
```

#### 4. 流式响应（SSE）

设置 `"stream": true` 即可启用 Server-Sent Events 流式响应：

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "介绍一下 Golang"}],
    "stream": true
  }'
```

## 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `KIRO_AUTH_TOKEN_PATH` | Token 文件路径 | `~/.aws/sso/cache/kiro-auth-token.json` |
| `KIRO_ACCESS_TOKEN` | 直接设置 Token | - |
| `KIRO_AUTO_REFRESH` | 是否自动刷新 | `true` |
| `KIRO_REGION` | AWS 区域 | `us-east-1` |

## 文件路径

- Token 文件: `~/.aws/sso/cache/kiro-auth-token.json`
- Client 注册: `~/.aws/sso/cache/{clientIdHash}.json`
- Profile ARN: `~/Library/Application Support/Kiro/User/globalStorage/kiro.kiroagent/profile.json`

## 技术实现

- **Token 管理**: 自动检测过期（提前 5 分钟），使用 OIDC 刷新
- **MCP 协议**: JSON-RPC 2.0 标准实现
- **EventStream**: 完整实现 AWS EventStream 二进制协议（CRC32 校验）
- **并发安全**: 使用 sync.RWMutex 保护共享状态

## 项目结构

```
kiro-api-client-go/
├── auth.go          # Token 管理
├── mcp.go           # MCP 客户端
├── search.go        # 搜索服务
├── chat.go          # 聊天服务
├── types.go         # 数据类型定义
├── client.go        # 主客户端
├── cmd/
│   └── main.go      # 命令行工具
└── server/
    └── main.go      # HTTP API 代理服务器
```

## License

MIT
