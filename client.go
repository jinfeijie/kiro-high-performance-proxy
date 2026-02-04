package kiroclient

// KiroClient Kiro API 客户端
type KiroClient struct {
	Auth   *AuthManager
	MCP    *MCPClient
	Search *SearchService
	Chat   *ChatService
}

// NewKiroClient 创建 Kiro 客户端
func NewKiroClient() *KiroClient {
	auth := NewAuthManager()
	mcp := NewMCPClient(auth)
	search := NewSearchService(mcp)
	chat := NewChatService(auth)

	return &KiroClient{
		Auth:   auth,
		MCP:    mcp,
		Search: search,
		Chat:   chat,
	}
}
