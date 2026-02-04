package kiroclient

import "time"

// KiroAuthToken Kiro 认证 Token
type KiroAuthToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	ClientIDHash string `json:"clientIdHash"`
	AuthMethod   string `json:"authMethod"`
	Provider     string `json:"provider"`
	Region       string `json:"region"`
}

// ClientRegistration 客户端注册信息
type ClientRegistration struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// MCPRequest MCP 请求
type MCPRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// MCPResponse MCP 响应
type MCPResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      string     `json:"id"`
	Result  *MCPResult `json:"result,omitempty"`
	Error   *MCPError  `json:"error,omitempty"`
}

// MCPResult MCP 结果
type MCPResult struct {
	Tools   []MCPTool    `json:"tools,omitempty"`
	Content []MCPContent `json:"content,omitempty"`
	IsError bool         `json:"isError,omitempty"`
}

// MCPError MCP 错误
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCPTool MCP 工具
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// MCPContent MCP 内容
type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// SearchResult 搜索结果
type SearchResult struct {
	Title          string `json:"title"`
	URL            string `json:"url"`
	Snippet        string `json:"snippet"`
	PublishedDate  *int64 `json:"publishedDate,omitempty"` // 时间戳（毫秒）
	IsPublicDomain bool   `json:"publicDomain"`            // 注意：JSON 字段名是 publicDomain
	ID             string `json:"id"`
	Domain         string `json:"domain"`
}

// BatchSearchResult 批量搜索结果
type BatchSearchResult struct {
	Results map[string][]SearchResult `json:"results"`
	Success int                       `json:"success"`
	Failed  int                       `json:"failed"`
}

// TokenRefreshRequest Token 刷新请求
// 注意：AWS OIDC API 使用 camelCase 字段名
type TokenRefreshRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken"`
}

// TokenRefreshResponse Token 刷新响应
// 注意：AWS OIDC API 返回 camelCase 字段名
type TokenRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// IsExpired 检查 Token 是否过期（提前 5 分钟）
func (t *KiroAuthToken) IsExpired() bool {
	if t.ExpiresAt == "" {
		return true
	}

	expiresAt, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return true
	}

	// 提前 5 分钟视为过期
	return time.Now().Add(5 * time.Minute).After(expiresAt)
}

// Model 模型信息
type Model struct {
	ID          string  `json:"id"`          // 模型 ID
	Name        string  `json:"name"`        // 显示名称
	Description string  `json:"description"` // 描述
	Credit      float64 `json:"credit"`      // 消耗的 credit 倍数
}

// ListAvailableModelsRequest 列出可用模型请求
type ListAvailableModelsRequest struct {
	Origin        string `json:"origin"`        // 必需：AI_EDITOR
	MaxResults    int    `json:"maxResults"`    // 可选：最大结果数
	NextToken     string `json:"nextToken"`     // 可选：分页token
	ProfileArn    string `json:"profileArn"`    // 可选：profile ARN
	ModelProvider string `json:"modelProvider"` // 可选：模型提供商
}

// ListAvailableModelsResponse 列出可用模型响应
type ListAvailableModelsResponse struct {
	Models    []Model `json:"models"`    // 可用模型列表
	NextToken string  `json:"nextToken"` // 下一页token
}

// 预定义的模型列表（基于 Kiro IDE 截图）
var AvailableModels = []Model{
	{
		ID:          "auto",
		Name:        "Auto",
		Description: "Models chosen by task for optimal usage and consistent quality",
		Credit:      0,
	},
	{
		ID:          "claude-sonnet-4.5",
		Name:        "Claude Sonnet 4.5",
		Description: "The latest Claude Sonnet model",
		Credit:      1.3,
	},
	{
		ID:          "claude-sonnet-4",
		Name:        "Claude Sonnet 4",
		Description: "Hybrid reasoning and coding for regular use",
		Credit:      1.3,
	},
	{
		ID:          "claude-haiku-4.5",
		Name:        "Claude Haiku 4.5",
		Description: "The latest Claude Haiku model",
		Credit:      0.4,
	},
	{
		ID:          "claude-opus-4.5",
		Name:        "Claude Opus 4.5",
		Description: "Claude Sonnet 4.5",
		Credit:      2.2,
	},
}

// IsValidModel 检查模型 ID 是否有效
func IsValidModel(modelID string) bool {
	if modelID == "" {
		return false
	}
	for _, model := range AvailableModels {
		if model.ID == modelID {
			return true
		}
	}
	return false
}

// ModelMapping 模型映射配置
type ModelMapping map[string]string

// DefaultModelMapping 默认的模型映射关系
var DefaultModelMapping = ModelMapping{
	// Claude Opus 4.5 映射
	"claude-opus-4-5-20251101": "claude-opus-4.5",
	"claude-opus-4-5":          "claude-opus-4.5",

	// Claude Sonnet 4.5 映射
	"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
	"claude-sonnet-4-5":          "claude-sonnet-4.5",

	// Claude Haiku 4.5 映射
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",
	"claude-haiku-4-5":          "claude-haiku-4.5",
}

// NormalizeModelID 将模型 ID 标准化（应用映射规则）
// 如果模型 ID 在映射表中，返回映射后的标准 ID
// 否则返回原始 ID
func NormalizeModelID(modelID string, mapping ModelMapping) string {
	if modelID == "" {
		return modelID
	}

	// 如果映射表为空，使用默认映射
	if mapping == nil {
		mapping = DefaultModelMapping
	}

	// 查找映射
	if normalized, exists := mapping[modelID]; exists {
		return normalized
	}

	// 没有映射规则，返回原始 ID
	return modelID
}

// UsageLimitsResponse 额度限制响应
type UsageLimitsResponse struct {
	DaysUntilReset     int              `json:"daysUntilReset"`
	NextDateReset      float64          `json:"nextDateReset"`
	SubscriptionInfo   SubscriptionInfo `json:"subscriptionInfo"`
	UsageBreakdownList []UsageBreakdown `json:"usageBreakdownList"`
	UserInfo           UserInfo         `json:"userInfo"`
}

// UserInfo 用户信息
type UserInfo struct {
	UserId string `json:"userId"`
	Email  string `json:"email"`
}

// SubscriptionInfo 订阅信息
type SubscriptionInfo struct {
	SubscriptionTitle string `json:"subscriptionTitle"`
}

// UsageBreakdown 额度使用明细
type UsageBreakdown struct {
	ResourceType              string  `json:"resourceType"`
	CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
	UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
	DisplayName               string  `json:"displayName"`
	Currency                  string  `json:"currency"`
}

// ========== 多账号登录相关类型 ==========

// AccountInfo 账号信息（包含设备码和 Token 的一一映射）
type AccountInfo struct {
	ID           string         `json:"id"`           // 账号唯一标识（使用 deviceCode 的 hash）
	DeviceCode   string         `json:"deviceCode"`   // 设备码（用于 Token 刷新）
	UserCode     string         `json:"userCode"`     // 用户码（显示给用户）
	Token        *KiroAuthToken `json:"token"`        // Token 数据
	ClientID     string         `json:"clientId"`     // OIDC 客户端 ID
	ClientSecret string         `json:"clientSecret"` // OIDC 客户端密钥
	UserId       string         `json:"userId"`       // 用户 ID
	Email        string         `json:"email"`        // 用户邮箱
	ProfileArn   string         `json:"profileArn"`   // Profile ARN（服务器部署必需）
	CreatedAt    string         `json:"createdAt"`    // 创建时间
	LastUsedAt   string         `json:"lastUsedAt"`   // 最后使用时间
}

// AccountsConfig 多账号配置
type AccountsConfig struct {
	Accounts []AccountInfo `json:"accounts"` // 账号列表
}

// RegisterClientRequest OIDC 客户端注册请求
type RegisterClientRequest struct {
	ClientName string   `json:"clientName"`
	ClientType string   `json:"clientType"`
	Scopes     []string `json:"scopes,omitempty"`
}

// RegisterClientResponse OIDC 客户端注册响应
type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
	AuthorizationEndpoint string `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint         string `json:"tokenEndpoint,omitempty"`
}

// StartDeviceAuthRequest 设备授权请求
type StartDeviceAuthRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	StartURL     string `json:"startUrl"`
}

// StartDeviceAuthResponse 设备授权响应
type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// CreateTokenRequest 创建 Token 请求
type CreateTokenRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	DeviceCode   string `json:"deviceCode,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// CreateTokenResponse 创建 Token 响应
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
}

// LoginSession 登录会话（用于轮询）
type LoginSession struct {
	SessionID    string `json:"sessionId"`
	DeviceCode   string `json:"deviceCode"`
	UserCode     string `json:"userCode"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	VerifyURL    string `json:"verifyUrl"`
	ExpiresAt    int64  `json:"expiresAt"`
	Interval     int    `json:"interval"`
	Region       string `json:"region"`
	StartUrl     string `json:"startUrl"` // SSO 起始 URL
	AuthType     string `json:"authType"` // "BuilderId" 或 "Enterprise"
}

// ========== 熔断器和负载均衡相关类型 ==========

// CircuitState 熔断器状态
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // 正常（关闭状态，允许请求）
	CircuitOpen                         // 熔断（打开状态，拒绝请求）
	CircuitHalfOpen                     // 半开（试探状态，允许少量请求）
)

// CircuitBreaker 熔断器
// 用于保护单个账号，避免频繁失败导致雪崩
type CircuitBreaker struct {
	State           CircuitState // 当前状态
	FailureCount    int          // 连续失败次数
	SuccessCount    int          // 半开状态下的连续成功次数
	LastFailureTime time.Time    // 最后失败时间
	OpenedAt        time.Time    // 熔断开始时间
	HalfOpenAt      time.Time    // 进入半开状态时间
}

// CircuitBreakerConfig 熔断器配置
type CircuitBreakerConfig struct {
	FailureThreshold   int           // 触发熔断的失败次数阈值（默认3次）
	FailureWindow      time.Duration // 失败计数窗口（默认5分钟）
	OpenDuration       time.Duration // 熔断持续时间（默认5分钟）
	HalfOpenMaxSuccess int           // 半开状态下成功多少次后关闭熔断（默认2次）
}

// DefaultCircuitBreakerConfig 默认熔断器配置
var DefaultCircuitBreakerConfig = CircuitBreakerConfig{
	FailureThreshold:   3,
	FailureWindow:      5 * time.Minute,
	OpenDuration:       5 * time.Minute,
	HalfOpenMaxSuccess: 2,
}

// AccountUsageCache 账号额度缓存
type AccountUsageCache struct {
	UsedCredits  float64   // 已使用额度
	TotalCredits float64   // 总额度
	LastUpdated  time.Time // 最后更新时间
	UpdateFailed bool      // 上次更新是否失败
}

// GetRemainingCredits 获取剩余额度
func (c *AccountUsageCache) GetRemainingCredits() float64 {
	if c.TotalCredits <= 0 {
		return 0
	}
	remaining := c.TotalCredits - c.UsedCredits
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetUsageRatio 获取使用比例 (0-1)
func (c *AccountUsageCache) GetUsageRatio() float64 {
	if c.TotalCredits <= 0 {
		return 1 // 无额度信息视为已用完
	}
	return c.UsedCredits / c.TotalCredits
}

// IsStale 检查缓存是否过期（超过10分钟）
func (c *AccountUsageCache) IsStale() bool {
	return time.Since(c.LastUpdated) > 10*time.Minute
}

// ========== Token 估算相关类型 ==========

// InputTokenDetails OpenAI 输入 token 详情
type InputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
	TextTokens   int `json:"text_tokens"`
	AudioTokens  int `json:"audio_tokens"`
	ImageTokens  int `json:"image_tokens"`
}

// OutputTokenDetails OpenAI 输出 token 详情
type OutputTokenDetails struct {
	TextTokens      int `json:"text_tokens"`
	AudioTokens     int `json:"audio_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

// OpenAIUsage OpenAI 格式的 usage（完整版，对齐 new-api）
type OpenAIUsage struct {
	PromptTokens           int                `json:"prompt_tokens"`
	CompletionTokens       int                `json:"completion_tokens"`
	TotalTokens            int                `json:"total_tokens"`
	PromptCacheHitTokens   int                `json:"prompt_cache_hit_tokens,omitempty"`
	PromptTokensDetails    InputTokenDetails  `json:"prompt_tokens_details"`
	CompletionTokenDetails OutputTokenDetails `json:"completion_tokens_details"`
	InputTokens            int                `json:"input_tokens,omitempty"`
	OutputTokens           int                `json:"output_tokens,omitempty"`
	InputTokensDetails     *InputTokenDetails `json:"input_tokens_details,omitempty"`
}

// ClaudeCacheCreationUsage Claude 缓存创建使用量
type ClaudeCacheCreationUsage struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// ClaudeServerToolUse Claude 服务端工具使用
type ClaudeServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
}

// ClaudeUsage Claude 格式的 usage（完整版，对齐 new-api）
type ClaudeUsage struct {
	InputTokens                 int                       `json:"input_tokens"`
	OutputTokens                int                       `json:"output_tokens"`
	CacheCreationInputTokens    int                       `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens        int                       `json:"cache_read_input_tokens,omitempty"`
	CacheCreation               *ClaudeCacheCreationUsage `json:"cache_creation,omitempty"`
	ClaudeCacheCreation5mTokens int                       `json:"claude_cache_creation_5_m_tokens,omitempty"`
	ClaudeCacheCreation1hTokens int                       `json:"claude_cache_creation_1_h_tokens,omitempty"`
	ServerToolUse               *ClaudeServerToolUse      `json:"server_tool_use,omitempty"`
}

// Usage 简化版 token 使用量（内部使用）
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ========== 图片支持相关类型 ==========

// ImageSource 图片源（Kiro API 格式）
type ImageSource struct {
	Bytes string `json:"bytes"` // base64 编码的图片数据（不含 data:xxx;base64, 前缀）
}

// ImageBlock 图片块（Kiro API 格式）
type ImageBlock struct {
	Format string      `json:"format"` // 图片格式：png, jpeg, gif, webp
	Source ImageSource `json:"source"` // 图片源
}

// ImageFormat 支持的图片格式
var SupportedImageFormats = map[string]bool{
	"png":  true,
	"jpeg": true,
	"jpg":  true,
	"gif":  true,
	"webp": true,
}

// ParseDataURL 解析 data URL，提取格式和 base64 数据
// 输入: data:image/png;base64,iVBORw0KGgo...
// 输出: format="png", data="iVBORw0KGgo...", ok=true
func ParseDataURL(dataURL string) (format string, data string, ok bool) {
	// 检查最小长度（data:image/x;base64,y 至少需要 22 字符）
	if len(dataURL) < 22 {
		return "", "", false
	}
	if dataURL[:5] != "data:" {
		return "", "", false
	}

	// 查找 ;base64,（限制搜索范围避免越界）
	idx := -1
	maxSearch := len(dataURL) - 8 // 确保有足够空间读取 ";base64,"
	if maxSearch > 50 {
		maxSearch = 50
	}
	for i := 5; i < maxSearch; i++ {
		if dataURL[i:i+8] == ";base64," {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "", "", false
	}

	// 提取 MIME 类型
	mimeType := dataURL[5:idx] // image/png
	if len(mimeType) < 6 || mimeType[:6] != "image/" {
		return "", "", false
	}
	format = mimeType[6:] // png

	// 提取 base64 数据
	data = dataURL[idx+8:]
	if len(data) == 0 {
		return "", "", false
	}

	return format, data, true
}
