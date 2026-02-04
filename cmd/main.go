package main

import (
	"flag"
	"fmt"
	"os"

	kiroclient "github.com/jinfeijie/kiro-api-client-go"
)

func main() {
	cmd := flag.String("cmd", "chat", "命令: search, chat, tools")
	query := flag.String("q", "", "搜索查询")
	prompt := flag.String("p", "", "聊天提示词")
	stream := flag.Bool("stream", false, "流式输出")
	flag.Parse()

	client := kiroclient.NewKiroClient()

	switch *cmd {
	case "chat":
		if *prompt == "" {
			fmt.Fprintln(os.Stderr, "请使用 -p 指定聊天提示词")
			os.Exit(1)
		}

		if *stream {
			err := client.Chat.SimpleChatStream(*prompt, func(content string, done bool) {
				if done {
					fmt.Println()
					return
				}
				fmt.Print(content)
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "聊天失败: %v\n", err)
				os.Exit(1)
			}
		} else {
			response, err := client.Chat.SimpleChat(*prompt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "聊天失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(response)
		}

	case "search":
		if *query == "" {
			fmt.Fprintln(os.Stderr, "请使用 -q 指定搜索查询")
			os.Exit(1)
		}

		results, err := client.Search.Search(*query, 10)
		if err != nil {
			fmt.Fprintf(os.Stderr, "搜索失败: %v\n", err)
			os.Exit(1)
		}

		for _, r := range results {
			fmt.Printf("%s\n%s\n%s\n\n", r.Title, r.URL, r.Snippet)
		}

	case "tools":
		tools, err := client.MCP.ToolsList()
		if err != nil {
			fmt.Fprintf(os.Stderr, "获取工具列表失败: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("可用工具 (%d):\n", len(tools))
		for _, t := range tools {
			fmt.Printf("  - %s: %s\n", t.Name, t.Description)
		}
	}
}
