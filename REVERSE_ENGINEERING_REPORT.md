# Kiro Server 逆向工程报告

## 基本信息

- **文件名**: `kiro-server`
- **文件类型**: Mach-O 64-bit executable arm64
- **文件大小**: 14MB
- **编译语言**: Go
- **主包路径**: `github.com/jinfeijie/kiro-api-client-go/server`

## 依赖库分析

### 核心框架
- `github.com/gin-gonic/gin` - Web框架
- `github.com/gin-contrib/pprof` - 性能分析
- `github.com/gin-contrib/sse` - Server-Sent Events支持

### 工具库
- `github.com/google/uuid` - UUID生成
- `github.com/pkoukk/tiktoken-go` - Token计数（OpenAI tokenizer）
- `github.com/go-playground/validator/v10` - 参数验证
- `github.com/dlclark/regexp2` - 正则表达式
- `github.com/gabriel-vasile/mimetype` - MIME类型检测

## 数据结构分析

### 核心数据结构

```go
// 日志相关
type LogEntry struct {}
type LogLevel int
type StructuredLogger struct {}

// Token统计
type TokenStats struct {}
type TokenDelta struct {}
type TokenStatusResponse struct {}
type TokenConfigRequest struct {}
type CountTokensRequest struct {}

// 配额管理
type QuotaDetail struct {}

// 账号管理
type AccountStats struct {}
type AccountWithUsage struct {}
type AccountDetailResponse struct {}
type AccountCircuitStats struct {}

// 熔断器
type CircuitStats struct {}
type TimeBucket struct {}

// 错误处理
type ErrorContext struct {}

// 请求统计
type RequestCounter struct {}

// 限流配置
type RateLimitConfig struct {}

// 搜索
type SearchRequest struct {}

// OpenAI兼容
type OpenAIChatRequest struct {}
type OpenAIChatResponse struct {}
type OpenAIChatMessage struct {}
type OpenAIChatChoice struct {}

// Claude兼容
type ClaudeChatRequest struct {}
type ClaudeChatResponse struct {}
type ClaudeContentBlock struct {}
```

## API端点分析

### 认证相关
- `POST /auth/import` - 导入账号Token
- `GET /auth/poll/:sessionId` - 轮询登录状态

### 账号管理
- `GET /api/accounts` - 获取账号列表（handleListAccounts）
- `GET /accounts/:id/detail` - 获取账号详情（handleAccountDetail）
- `POST /accounts/:id/refresh` - 刷新单个账号（handleRefreshAccount）
- `POST /accounts/refresh-all` - 刷新所有账号（handleRefreshAllAccounts）
- `DELETE /accounts/:id` - 删除账号（handleDeleteAccount）

### Token管理
- `GET /api/token/status` - 获取Token状态（handleTokenStatus）
- `POST /api/token/config` - 配置Token（handleTokenConfig）
- `POST /v1/messages/count_tokens` - 计算Token数量（handleCountTokens）

### 熔断器管理
- `GET /circuit-breaker/status` - 获取熔断状态（handleCircuitBreakerStatus）
- `POST /circuit-breaker/trip` - 手动熔断（handleCircuitBreakerTrip）
- `POST /circuit-breaker/reset` - 解除熔断（handleCircuitBreakerReset）

### 统计数据
- `GET /api/stats` - 获取统计数据（handleGetStats）
- `GET /api/stats/accounts` - 获取账号统计（handleGetAccountStats）

### 配置管理
- `GET /settings/api-keys` - 获取API密钥（handleGetApiKeys）
- `POST /settings/api-keys` - 更新API密钥（handleUpdateApiKeys）
- `GET /settings/rate-limit` - 获取限流配置（handleGetRateLimit）
- `POST /settings/rate-limit` - 更新限流配置（handleUpdateRateLimit）
- `GET /settings/ip-blacklist` - 获取IP黑名单（handleGetIpBlacklist）
- `POST /settings/ip-blacklist` - 更新IP黑名单（handleUpdateIpBlacklist）
- `GET /settings/model-mapping` - 获取模型映射（handleGetModelMapping）
- `POST /settings/model-mapping` - 更新模型映射（handleUpdateModelMapping）
- `GET /settings/proxy` - 获取代理配置（handleGetProxyConfig）
- `POST /settings/proxy` - 更新代理配置（handleUpdateProxyConfig）
- `GET /settings/log-level` - 获取日志级别（handleGetLogLevel）
- `POST /settings/log-level` - 更新日志级别（handleUpdateLogLevel）

### OpenAI兼容API
- `POST /v1/chat/completions` - OpenAI聊天接口（handleOpenAIChat）
- `GET /v1/models` - 模型列表（handleModelsList）

### Claude兼容API
- `POST /anthropic/v1/messages` - Claude聊天接口（handleClaudeChat）
- `POST /v1/messages` - Claude聊天接口（handleChat）

### MCP工具
- `GET /api/tools` - 工具列表（handleToolsList）
- `POST /api/tools/call` - 调用工具（handleToolsCall）

### 搜索
- `POST /api/search` - Web搜索（handleSearch）

## 核心功能分析

### 1. 熔断器系统（Circuit Breaker）

**核心方法**：
```go
func (*CircuitStats).Record(accountId string, success bool)
func (*CircuitStats).GetErrorRate(accountId string, minutes int) (float64, int64)
func (*CircuitStats).ClearAccount(accountId string)
func (*CircuitStats).cleanupAccount(accountId string)
func alignToBucket(t time.Time) time.Time
```

**功能**：
- 记录每个账号的请求成功/失败
- 计算错误率（1分钟、5分钟窗口）
- 自动清理过期数据
- 支持手动熔断和解除熔断

### 2. Token统计系统

**核心方法**：
```go
func loadTokenStats() (*TokenStats, error)
func saveTokenStats(stats *TokenStats) error
func tokenStatsWorker()
func getTokenStats() *TokenStats
```

**功能**：
- 实时统计Token使用量
- 持久化Token统计数据
- 后台异步更新统计

### 3. 账号统计系统

**核心方法**：
```go
func loadAccountStats() (map[string]*AccountStats, error)
func saveAccountStats(stats map[string]*AccountStats) error
func recordAccountRequest(accountId string, success bool, statusCode int, errorMsg string)
func getAccountStats() map[string]*AccountStats
func accountStatsWorker()
```

**功能**：
- 记录每个账号的请求统计
- 统计成功率、状态码分布、错误类型
- 持久化统计数据

### 4. 日志系统

**核心方法**：
```go
func NewStructuredLogger() *StructuredLogger
func (*StructuredLogger).Log(level LogLevel, msg string, fields map[string]interface{})
func (*StructuredLogger).Info(msg string, fields map[string]interface{})
func (*StructuredLogger).Warn(msg string, fields map[string]interface{})
func (*StructuredLogger).Error(msg string, fields map[string]interface{})
func (*StructuredLogger).SetLevel(level LogLevel)
func (*StructuredLogger).GetLevel() LogLevel
func ParseLogLevel(s string) LogLevel
func sanitizeHeaders(headers map[string]string) map[string]string
func isSensitiveHeader(key string) bool
```

**功能**：
- 结构化日志输出
- 动态调整日志级别
- 敏感信息脱敏

### 5. 流式响应处理

**核心方法**：
```go
func handleStreamResponse(...)
func handleStreamResponseWithTools(...)
func handleNonStreamResponse(...)
func handleNonStreamResponseWithTools(...)
```

**功能**：
- 支持SSE流式输出
- 支持工具调用（Function Calling）
- OpenAI和Claude双协议支持

### 6. 限流系统

**核心方法**：
```go
func loadRateLimitConfig() (*RateLimitConfig, error)
func saveRateLimitConfig(config *RateLimitConfig) error
```

**功能**：
- IP级别限流
- 可配置的请求频率限制

### 7. 配置持久化

**数据文件**：
- `token-stats.json` - Token统计数据
- `account-stats.json` - 账号统计数据
- `kiro-accounts.json` - 账号配置
- `rate-limit.json` - 限流配置

## 安全特性

### 1. 敏感信息保护
- HTTP头部脱敏（X-Auth-Token, Authorization等）
- 日志中敏感字段过滤

### 2. 认证机制
- API-KEY认证
- Token导入验证

### 3. 访问控制
- IP黑名单
- 请求频率限制

## 性能优化

### 1. 并发控制
- 使用channel进行异步统计更新
- 读写锁保护共享数据

### 2. 数据清理
- 自动清理过期的时间桶数据
- 定期持久化统计数据

### 3. 性能监控
- 集成pprof性能分析工具
- 支持CPU、内存、goroutine分析

## 环境变量

- `KIRO_ACCESS_TOKEN` - 访问令牌
- `KIRO_AUTH_TOKEN_PATH` - Token文件路径

## 特殊功能

### 1. 模型映射
- 支持外部模型ID到内部模型ID的映射
- 动态配置模型别名

### 2. 代理支持
- HTTP代理配置
- 请求转发

### 3. Token计数
- 使用tiktoken-go进行精确的Token计数
- 支持多种模型的Token计算

## 错误处理

### 错误类型
- `authentication_error` - 认证错误
- `invalid_request_body` - 请求体无效
- `Input is too long` - 输入过长
- `Streaming not supported` - 不支持流式

### 错误上下文
```go
type ErrorContext struct {
    // 包含错误详细信息
}
```

## 总结

这是一个功能完整的AI API代理服务器，主要特点：

1. **多协议支持**：同时兼容OpenAI和Claude API
2. **完善的监控**：Token统计、账号统计、熔断器监控
3. **高可用性**：熔断器保护、限流、负载均衡
4. **灵活配置**：支持动态配置、模型映射、代理设置
5. **安全可靠**：API-KEY认证、IP黑名单、敏感信息脱敏
6. **性能优化**：异步统计、数据持久化、并发控制

该服务器是一个生产级别的AI API网关，提供了完整的管理、监控和保护功能。
