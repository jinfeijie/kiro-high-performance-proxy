package kiroclient

import (
	"encoding/json"
	"fmt"
	"sync"
)

// SearchService 搜索服务
type SearchService struct {
	mcpClient *MCPClient
}

// NewSearchService 创建搜索服务
// 参数：
// - mcpClient: MCP 客户端
func NewSearchService(mcpClient *MCPClient) *SearchService {
	return &SearchService{
		mcpClient: mcpClient,
	}
}

// Search 执行搜索
func (s *SearchService) Search(query string, maxResults int) ([]SearchResult, error) {
	arguments := map[string]any{
		"query":      query,
		"maxResults": maxResults,
	}

	content, err := s.mcpClient.ToolsCall("web_search", arguments)
	if err != nil {
		return nil, err
	}

	if len(content) == 0 {
		return nil, fmt.Errorf("搜索结果为空")
	}

	// 解析结果 - 返回格式是 {"results": [...], "totalResults": N}
	var response struct {
		Results []SearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(content[0].Text), &response); err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}

	return response.Results, nil
}

// BatchSearch 批量搜索
func (s *SearchService) BatchSearch(queries []string, maxResults int) (*BatchSearchResult, error) {
	result := &BatchSearchResult{
		Results: make(map[string][]SearchResult),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, query := range queries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()

			results, err := s.Search(q, maxResults)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				result.Failed++
			} else {
				result.Results[q] = results
				result.Success++
			}
		}(query)
	}

	wg.Wait()
	return result, nil
}
