package main

import (
	"strings"
	"testing"
)

// TestNotifHash 验证 hash 生成的格式和一致性
func TestNotifHash(t *testing.T) {
	msg := "测试通知"
	hash := notifHash(msg)

	if !strings.HasPrefix(hash, notifHashPrefix) {
		t.Errorf("hash 缺少前缀: %s", hash)
	}
	if !strings.HasSuffix(hash, notifHashSuffix) {
		t.Errorf("hash 缺少后缀: %s", hash)
	}
	// 同一内容 hash 必须一致
	if notifHash(msg) != hash {
		t.Errorf("同一内容的 hash 不一致")
	}
	// 不同内容 hash 必须不同
	if notifHash("另一条通知") == hash {
		t.Errorf("不同内容的 hash 不应该相同")
	}
}

// TestFormatNotificationBlock 验证格式化输出包含 hash 标记
func TestFormatNotificationBlock(t *testing.T) {
	msg := "测试通知"
	hashTag := notifHash(msg)
	result := formatNotificationBlock(msg, hashTag)

	if !strings.Contains(result, notifSeparator) {
		t.Errorf("缺少分隔符")
	}
	if !strings.Contains(result, msg) {
		t.Errorf("缺少通知正文")
	}
	if !strings.Contains(result, hashTag) {
		t.Errorf("缺少 hash 标记")
	}
}

// TestIsNotificationText_HashBased 只看预存的 hashTag
func TestIsNotificationText_HashBased(t *testing.T) {
	notification := "### 📣 网站通知\nAPI-KEY: `123456`"
	hashTag := notifHash(notification)

	// 包含 hashTag 的文本应该匹配
	textWithHash := "一些AI回复" + hashTag
	if !isNotificationText(textWithHash, hashTag) {
		t.Errorf("包含 hashTag 的文本应该匹配")
	}

	// 格式化后的通知（包含 hashTag）应该匹配
	formatted := formatNotificationBlock(notification, hashTag)
	if !isNotificationText(formatted, hashTag) {
		t.Errorf("格式化后的通知应该匹配")
	}

	// 完全无关的文本
	if isNotificationText("普通文本", hashTag) {
		t.Errorf("无关文本不应该匹配")
	}

	// 空值边界
	if isNotificationText("", hashTag) {
		t.Errorf("空文本不应该匹配")
	}
	if isNotificationText("任意文本", "") {
		t.Errorf("空 hashTag 不应该匹配")
	}
}

// TestStripNotificationFromText_HashBased 用预存 hashTag 移除通知
func TestStripNotificationFromText_HashBased(t *testing.T) {
	notification := "### 📣 网站通知\nAPI-KEY: `123456`"
	hashTag := notifHash(notification)
	formatted := formatNotificationBlock(notification, hashTag)
	content := "AI的回答内容" + formatted

	result := stripNotificationFromText(content, hashTag)
	if strings.Contains(result, "网站通知") {
		t.Errorf("strip 后仍包含通知: %s", result)
	}
	if result != "AI的回答内容" {
		t.Errorf("期望 'AI的回答内容'，实际: '%s'", result)
	}
}

// TestStripNotificationFromText_NoMatch 没有 hashTag 时原样返回
func TestStripNotificationFromText_NoMatch(t *testing.T) {
	content := "正常的AI回复内容"
	hashTag := notifHash("某条通知")
	result := stripNotificationFromText(content, hashTag)
	if result != content {
		t.Errorf("没有匹配时应原样返回")
	}
}

// TestStripNotificationFromText_Empty 空 hashTag 时原样返回
func TestStripNotificationFromText_Empty(t *testing.T) {
	content := "正常的AI回复内容"
	result := stripNotificationFromText(content, "")
	if result != content {
		t.Errorf("空 hashTag 时应原样返回")
	}
}

// TestShouldInjectNotification_ClaudeBlock Claude 格式判重
func TestShouldInjectNotification_ClaudeBlock(t *testing.T) {
	notification := "### 📣 网站通知\nAPI-KEY: `123456`"
	hashTag := notifHash(notification)

	notificationMutex.Lock()
	notificationConfig = NotificationConfig{
		Enabled: true,
		Message: notification,
		Hash:    hashTag,
	}
	notificationMutex.Unlock()

	messages := []map[string]any{
		{"role": "user", "content": "你好"},
		{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "你好！"},
			map[string]any{"type": "text", "text": formatNotificationBlock(notification, hashTag)},
		}},
		{"role": "user", "content": "再问一次"},
	}

	if shouldInjectNotification(messages) {
		t.Errorf("历史中已有通知 block，不应重复注入")
	}
}

// TestShouldInjectNotification_OpenAI OpenAI 格式判重
func TestShouldInjectNotification_OpenAI(t *testing.T) {
	notification := "### 📣 网站通知\nAPI-KEY: `123456`"
	hashTag := notifHash(notification)

	notificationMutex.Lock()
	notificationConfig = NotificationConfig{
		Enabled: true,
		Message: notification,
		Hash:    hashTag,
	}
	notificationMutex.Unlock()

	messages := []map[string]any{
		{"role": "user", "content": "你好"},
		{"role": "assistant", "content": "你好！" + formatNotificationBlock(notification, hashTag)},
		{"role": "user", "content": "再问一次"},
	}

	if shouldInjectNotification(messages) {
		t.Errorf("历史中已有通知文本，不应重复注入")
	}
}

// TestShouldInjectNotification_First 首次请求应注入
func TestShouldInjectNotification_First(t *testing.T) {
	notification := "### 📣 网站通知\nAPI-KEY: `123456`"
	hashTag := notifHash(notification)

	notificationMutex.Lock()
	notificationConfig = NotificationConfig{
		Enabled: true,
		Message: notification,
		Hash:    hashTag,
	}
	notificationMutex.Unlock()

	messages := []map[string]any{
		{"role": "user", "content": "你好"},
	}

	if !shouldInjectNotification(messages) {
		t.Errorf("首次请求应该注入通知")
	}
}

// TestShouldInjectNotification_Disabled 通知关闭时不注入
func TestShouldInjectNotification_Disabled(t *testing.T) {
	notificationMutex.Lock()
	notificationConfig = NotificationConfig{
		Enabled: false,
		Message: "任意通知",
		Hash:    notifHash("任意通知"),
	}
	notificationMutex.Unlock()

	messages := []map[string]any{
		{"role": "user", "content": "你好"},
	}

	if shouldInjectNotification(messages) {
		t.Errorf("通知关闭时不应注入")
	}
}

// TestNotificationConfig_HashPrecomputed 验证保存时预算 hash
func TestNotificationConfig_HashPrecomputed(t *testing.T) {
	msg := "测试预算 hash"
	expected := notifHash(msg)

	cfg := NotificationConfig{
		Enabled: true,
		Message: msg,
		Hash:    expected,
	}

	// Hash 应该和 notifHash 算出来的一致
	if cfg.Hash != expected {
		t.Errorf("预算 hash 不一致")
	}

	// 运行时直接用 cfg.Hash 做对比，不需要重算
	text := "AI回复" + cfg.Hash
	if !isNotificationText(text, cfg.Hash) {
		t.Errorf("用预存 hash 对比应该匹配")
	}
}
