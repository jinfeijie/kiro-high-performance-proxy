# Kiro API Client - 快速参考

## 🚀 一分钟快速开始

```bash
# 1. 安装
go get github.com/jinfeijie/kiro-api-client-go

# 2. 启动服务器
cd server && go run main.go

# 3. 访问 Web UI
open http://localhost:8080
```

---

## 📦 基本用法

### 创建客户端

```go
import kiroclient "github.com/jinfeijie/kiro-api-client-go"

client := kiroclient.NewKiroClient()
```

### Token 管理

```go
// 获取有效 Token（自动刷新）
token, err := client.Auth.GetAccessToken()

// 检查 Token 状态
tokenInfo, err := client.Auth.ReadToken()
if tokenInfo.IsExpired() {
    // Token 已过期
}
```

### Chat 聊天

```go
messages := []kiroclient.ChatMessage{
    {Role: "user", Content: "Hello!"},
}

// 流式聊天
client.Chat.ChatStreamWithModel(messages, "claude-sonnet-4.5", func(content string, done bool) {
    if !done {
        fmt.Print(content)
    }
})

// 非流式聊天
response, err := client.Chat.ChatWithModel(messages, "claude-sonnet-4.5")
```

### Web Search

```go
// 单个搜索
results, err := client.Search.Search("golang best practices", 10)

// 批量搜索
queries := []string{"query1", "query2", "query3"}
batchResults, err := client.Search.BatchSearch(queries, 10)
```

### MCP 工具

```go
// 列出工具
tools, err := client.MCP.ToolsList()

// 调用工具
args := map[string]any{"param": "value"}
result, err := client.MCP.ToolsCall("tool_name", args)
```

---

## 🌐 HTTP API

### 端点列表

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/token/status` | Token 状态 |
| POST | `/api/token/config` | 配置 Token |
| GET | `/api/models` | 模型列表 |
| POST | `/api/chat` | Chat 接口 |
| POST | `/api/search` | 搜索接口 |
| GET | `/api/tools` | MCP 工具列表 |
| POST | `/api/tools/call` | 调用 MCP 工具 |
| POST | `/v1/chat/completions` | OpenAI 格式 |
| POST | `/v1/messages` | Claude 格式 |
| POST | `/anthropic/v1/messages` | Anthropic 格式 |

### 示例请求

#### Chat（OpenAI 格式）

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }'
```

#### Chat（Claude 格式）

```bash
curl -X POST http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 1000
  }'
```

#### Search

```bash
curl -X POST http://localhost:8080/api/search \
  -H "Content-Type: application/json" \
  -d '{
    "query": "golang best practices",
    "maxResults": 5
  }'
```

---

## 🎨 可用模型

| 模型 ID | 名称 | Credit | 说明 |
|---------|------|--------|------|
| `auto` | Auto | 0x | 自动选择最优模型 |
| `claude-sonnet-4.5` | Claude Sonnet 4.5 | 1.3x | 最新 Sonnet（推荐） |
| `claude-sonnet-4` | Claude Sonnet 4 | 1.3x | Sonnet 4 |
| `claude-haiku-4.5` | Claude Haiku 4.5 | 0.4x | 最新 Haiku（快速） |
| `claude-opus-4.5` | Claude Opus 4.5 | 2.2x | Opus 4.5（高级） |

---

## 🔧 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `KIRO_AUTH_TOKEN_PATH` | Token 文件路径 | `~/.aws/sso/cache/kiro-auth-token.json` |
| `KIRO_ACCESS_TOKEN` | 直接设置 Token | - |
| `KIRO_AUTO_REFRESH` | 自动刷新 Token | `true` |
| `KIRO_REGION` | AWS 区域 | `us-east-1` |
| `KIRO_MACHINE_ID` | 机器 ID | 自动生成 |
| `KIRO_VERSION` | 版本号 | `0.8.140` |

---

## 🧪 测试命令

```bash
# 运行所有测试
./test_all.sh

# 运行单元测试
go test -v ./...

# 运行服务器测试
cd server && go test -v

# 运行特定测试
go test -v -run TestChatStream

# 查看测试覆盖率
go test -cover ./...
```

---

## 🐛 常见问题

### Q: Token 文件找不到？

```bash
# 检查文件是否存在
ls -la ~/.aws/sso/cache/kiro-auth-token.json

# 设置自定义路径
export KIRO_AUTH_TOKEN_PATH="/path/to/token.json"
```

### Q: 端口被占用？

```bash
# 查找占用进程
lsof -i :8080

# 使用其他端口
PORT=8081 go run main.go
```

### Q: 模型列表为空？

- 检查网络连接
- 检查 Token 是否有效
- 服务会自动降级到预定义列表

### Q: Chat 响应慢？

- 检查网络延迟
- 尝试使用 `claude-haiku-4.5`（更快）
- 使用流式响应获得更好体验

---

## 📚 更多文档

- [README.md](README.md) - 项目介绍
- [USAGE.md](USAGE.md) - 详细使用指南
- [DEMO.md](DEMO.md) - 完整演示
- [ALIGNMENT.md](ALIGNMENT.md) - 对齐说明
- [CHANGELOG.md](CHANGELOG.md) - 变更日志
- [PROJECT_SUMMARY.md](PROJECT_SUMMARY.md) - 项目总结
- [DEPLOYMENT_CHECKLIST.md](DEPLOYMENT_CHECKLIST.md) - 部署清单

---

## 🔗 快速链接

- **GitHub**: https://github.com/jinfeijie/kiro-api-client-go
- **Issues**: https://github.com/jinfeijie/kiro-api-client-go/issues
- **Web UI**: http://localhost:8080
- **API Docs**: http://localhost:8080/api/models

---

**提示**: 这是快速参考，详细信息请查看完整文档。
