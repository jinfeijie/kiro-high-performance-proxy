package kiroclient

import (
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// tokenizer 全局 tokenizer 实例（使用 cl100k_base 编码）
// Claude 的 tokenizer 与 GPT-4 的 cl100k_base 非常接近
var (
	tokenizer     *tiktoken.Tiktoken
	tokenizerOnce sync.Once
	tokenizerErr  error
)

// getTokenizer 获取 tokenizer 实例（懒加载，线程安全）
func getTokenizer() (*tiktoken.Tiktoken, error) {
	tokenizerOnce.Do(func() {
		tokenizer, tokenizerErr = tiktoken.GetEncoding("cl100k_base")
	})
	return tokenizer, tokenizerErr
}

// CountTokens 使用 tiktoken 计算文本的 token 数
func CountTokens(text string) int {
	if text == "" {
		return 0
	}

	tkm, err := getTokenizer()
	if err != nil {
		// 降级：粗略估算（平均 4 字符/token）
		return len(text) / 4
	}

	tokens := tkm.Encode(text, nil, nil)
	return len(tokens)
}

// CountMessagesTokens 计算消息列表的 token 数
func CountMessagesTokens(messages []ChatMessage) int {
	total := 0
	for _, msg := range messages {
		// 每条消息有格式开销：<|im_start|>role\ncontent<|im_end|>
		total += 4
		total += CountTokens(msg.Role)
		total += CountTokens(msg.Content)
	}
	// 回复的 priming tokens
	total += 3
	return total
}
