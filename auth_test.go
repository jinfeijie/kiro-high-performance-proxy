package kiroclient

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"testing"
)

// TestClientIdHashGeneration 测试 clientIdHash 生成是否与 Kiro IDE 一致
// IDE 实现: crypto.createHash("sha1").update(JSON.stringify({ startUrl })).digest("hex")
func TestClientIdHashGeneration(t *testing.T) {
	testCases := []struct {
		name     string
		startUrl string
		expected string // 从 IDE 或已知正确值获取
	}{
		{
			name:     "Builder ID 默认 URL",
			startUrl: "https://view.awsapps.com/start",
			expected: "", // 需要从 IDE 获取实际值
		},
		{
			name:     "企业 SSO URL 示例",
			startUrl: "https://d-906xxxxx.awsapps.com/start",
			expected: "", // 需要从 IDE 获取实际值
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 使用与 IDE 相同的方式生成 clientIdHash
			h := sha1.New()
			jsonStr := fmt.Sprintf(`{"startUrl":"%s"}`, tc.startUrl)
			h.Write([]byte(jsonStr))
			result := hex.EncodeToString(h.Sum(nil))

			t.Logf("startUrl: %s", tc.startUrl)
			t.Logf("JSON string: %s", jsonStr)
			t.Logf("clientIdHash: %s", result)

			if tc.expected != "" && result != tc.expected {
				t.Errorf("clientIdHash 不匹配\n期望: %s\n实际: %s", tc.expected, result)
			}
		})
	}
}

// TestClientIdHashCompareWithIDE 与 IDE 生成的实际值对比
func TestClientIdHashCompareWithIDE(t *testing.T) {
	// 从 kiro-accounts.json 中获取的实际 IDE 生成值
	// 第一个账号的 clientIdHash: dffeef37587922df57fd4035c8ecff10cad43d10
	// 对应的 startUrl: https://d-90661cd500.awsapps.com/start (无末尾斜杠)

	startUrl := "https://d-90661cd500.awsapps.com/start"
	h := sha1.New()
	jsonStr := fmt.Sprintf(`{"startUrl":"%s"}`, startUrl)
	h.Write([]byte(jsonStr))
	result := hex.EncodeToString(h.Sum(nil))

	expected := "dffeef37587922df57fd4035c8ecff10cad43d10"

	t.Logf("startUrl: %s", startUrl)
	t.Logf("JSON string: %s", jsonStr)
	t.Logf("生成的 clientIdHash: %s", result)
	t.Logf("IDE 生成的 clientIdHash: %s", expected)

	if result != expected {
		t.Errorf("clientIdHash 不匹配！\n期望: %s\n实际: %s", expected, result)
	} else {
		t.Log("✓ clientIdHash 生成方式与 IDE 完全一致！")
	}
}
