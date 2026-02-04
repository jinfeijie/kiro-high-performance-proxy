//go:build ignore
// +build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	// 读取账号配置
	data, err := os.ReadFile("kiro-api-client-go/kiro-accounts.json")
	if err != nil {
		fmt.Printf("读取配置失败: %v\n", err)
		return
	}

	var config struct {
		Accounts []struct {
			Token struct {
				AccessToken string `json:"accessToken"`
				Region      string `json:"region"`
			} `json:"token"`
		} `json:"accounts"`
	}

	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Printf("解析配置失败: %v\n", err)
		return
	}

	if len(config.Accounts) == 0 {
		fmt.Println("没有账号")
		return
	}

	acc := config.Accounts[0]
	region := acc.Token.Region
	if region == "" {
		region = "us-east-1"
	}

	// 尝试调用 OIDC userinfo endpoint
	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/userinfo", region)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+acc.Token.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("OIDC userinfo 状态码: %d\n", resp.StatusCode)
	fmt.Printf("响应:\n%s\n", string(body))
}
