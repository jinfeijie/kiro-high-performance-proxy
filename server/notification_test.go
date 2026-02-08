package main

import (
	"strings"
	"testing"
)

// TestStripNotification_ExactMatch 精确匹配能移除通知
func TestStripNotification_ExactMatch(t *testing.T) {
	notification := ">            \n### 📣 网站通知\n>            \n>            API-KEY: `123456`"
	injected := "AI的回答内容\n\n---\n" + notification + "\n---"

	result := stripNotificationFromContent(injected, notification)
	if strings.Contains(result, "网站通知") {
		t.Errorf("精确匹配应该能移除通知，但没有移除")
	}
}

// TestStripNotification_ClientReformat 模拟客户端回传时文本被重新格式化
// 这是根本问题的复现：客户端会去掉多余空格、改变换行
func TestStripNotification_ClientReformat(t *testing.T) {
	// 原始通知（notification.json 中的内容，带大量空格）
	notification := ">            \n### 📣 网站通知\n>            \n>            API-KEY: `123456` 即将在今日`14:00`进行撤销。\n>            新API-KEY已更新在官网，请及时前往 `https://onedayai.autocode.space` 获取\n>            \n>            当前已使用日本节点，国内延迟100ms以下，国内可直连\n>            \n>            晚些时间会推送交流群信息，欢迎进群交流。"

	// 模拟客户端回传的版本（空格被压缩、blockquote 标记被去掉）
	clientVersion := "AI的回答内容\n\n---\n" +
		"\n### 📣 网站通知\n\n" +
		"API-KEY: `123456` 即将在今日`14:00`进行撤销。\n" +
		"新API-KEY已更新在官网，请及时前往 `https://onedayai.autocode.space` 获取\n\n" +
		"当前已使用日本节点，国内延迟100ms以下，国内可直连\n\n" +
		"晚些时间会推送交流群信息，欢迎进群交流。\n---"

	result := stripNotificationFromContent(clientVersion, notification)
	if strings.Contains(result, "网站通知") {
		t.Errorf("客户端重新格式化后，strip 未能移除通知！\n原始通知长度: %d\n客户端版本: %s\nstrip结果: %s",
			len(notification), clientVersion, result)
	}
}

// TestStripNotification_WhitespaceVariation 空格数量变化导致匹配失败
func TestStripNotification_WhitespaceVariation(t *testing.T) {
	notification := ">            \n### 📣 网站通知"

	// 客户端把 ">            " 变成 "> "
	clientContent := "回答\n\n---\n> \n### 📣 网站通知\n---"

	result := stripNotificationFromContent(clientContent, notification)
	if strings.Contains(result, "网站通知") {
		t.Errorf("空格变化后 strip 失败: %s", result)
	}
}

// TestShouldInjectNotification_SecondRequest 第二次请求时历史消息包含被改格式的通知
func TestShouldInjectNotification_SecondRequest(t *testing.T) {
	// 设置全局通知配置
	notificationMutex.Lock()
	notificationConfig = NotificationConfig{
		Enabled: true,
		Message: ">            \n### 📣 网站通知\n>            API-KEY: `123456`",
	}
	notificationMutex.Unlock()

	// 模拟第二次请求：历史消息中 assistant 的内容已被客户端重新格式化
	messages := []map[string]any{
		{"role": "user", "content": "你好"},
		{"role": "assistant", "content": "你好！\n\n---\n\n### 📣 网站通知\nAPI-KEY: `123456`\n---"},
		{"role": "user", "content": "再问一次"},
	}

	// 如果 shouldInjectNotification 返回 true，说明它没识别出历史中已有通知
	// 这会导致重复注入，AI 看到通知后认为是提示注入攻击
	result := shouldInjectNotification(messages)
	if result {
		t.Errorf("历史消息中已有通知（格式被客户端修改），但 shouldInjectNotification 仍返回 true，会导致重复注入")
	}
}
