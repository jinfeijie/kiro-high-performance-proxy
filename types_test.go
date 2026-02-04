package kiroclient

import (
	"testing"
)

// TestIsValidModel 测试模型ID验证
func TestIsValidModel(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"有效模型: auto", "auto", true},
		{"有效模型: claude-sonnet-4.5", "claude-sonnet-4.5", true},
		{"有效模型: claude-sonnet-4", "claude-sonnet-4", true},
		{"有效模型: claude-haiku-4.5", "claude-haiku-4.5", true},
		{"有效模型: claude-opus-4.5", "claude-opus-4.5", true},
		{"无效模型: gpt-4", "gpt-4", false},
		{"无效模型: 空字符串", "", false},
		{"无效模型: 随机字符串", "random-model", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidModel(tt.modelID); got != tt.want {
				t.Errorf("IsValidModel(%q) = %v, want %v", tt.modelID, got, tt.want)
			}
		})
	}
}
