#!/bin/bash

# Kiro API Client 功能测试脚本

set -e

echo "========================================="
echo "  Kiro API Client 功能测试"
echo "========================================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数
TOTAL=0
PASSED=0
FAILED=0

# 测试函数
test_command() {
    TOTAL=$((TOTAL + 1))
    echo -e "${YELLOW}[测试 $TOTAL]${NC} $1"
    
    if eval "$2" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ 通过${NC}"
        PASSED=$((PASSED + 1))
        return 0
    else
        echo -e "${RED}❌ 失败${NC}"
        FAILED=$((FAILED + 1))
        return 1
    fi
}

# 1. 编译测试
echo "1. 编译测试"
echo "-------------------"
test_command "编译命令行工具" "go build -o kiroclient ./cmd/main.go"
test_command "编译 HTTP 服务器" "go build -o kiro-proxy ./server/main.go"
echo ""

# 2. 代码质量检查
echo "2. 代码质量检查"
echo "-------------------"
test_command "Go fmt 检查" "test -z \$(gofmt -l .)"
test_command "Go vet 检查（主代码）" "go vet *.go"
test_command "Go vet 检查（cmd）" "go vet ./cmd/..."
test_command "Go vet 检查（server）" "go vet ./server/..."
echo ""

# 3. 功能测试（需要有效的 Token）
echo "3. 功能测试"
echo "-------------------"

# 检查 Token 文件是否存在
if [ -f ~/.aws/sso/cache/kiro-auth-token.json ]; then
    echo -e "${GREEN}✓${NC} Token 文件存在"
    
    # 测试工具列表
    test_command "获取工具列表" "./kiroclient -cmd=tools"
    
    # 测试搜索（如果工具列表成功）
    if [ $? -eq 0 ]; then
        test_command "Web Search 测试" "./kiroclient -cmd=search -q='Golang'"
    fi
    
    # 测试聊天（简短问题避免超时）
    test_command "Chat 测试" "timeout 30s ./kiroclient -cmd=chat -p='hi'"
else
    echo -e "${RED}✗${NC} Token 文件不存在，跳过功能测试"
    echo "  请先登录 Kiro IDE 生成 Token"
fi
echo ""

# 4. 示例代码编译测试
echo "4. 示例代码编译测试"
echo "-------------------"
test_command "编译 chat_example" "go build -o /dev/null ./examples/chat_example.go"
test_command "编译 search_example" "go build -o /dev/null ./examples/search_example.go"
test_command "编译 mcp_example" "go build -o /dev/null ./examples/mcp_example.go"
test_command "编译 token_example" "go build -o /dev/null ./examples/token_example.go"
echo ""

# 5. 文档检查
echo "5. 文档检查"
echo "-------------------"
test_command "README.md 存在" "test -f README.md"
test_command "USAGE.md 存在" "test -f USAGE.md"
test_command "ALIGNMENT.md 存在" "test -f ALIGNMENT.md"
test_command "examples/ 目录存在" "test -d examples"
echo ""

# 测试结果汇总
echo "========================================="
echo "  测试结果汇总"
echo "========================================="
echo -e "总计: $TOTAL"
echo -e "${GREEN}通过: $PASSED${NC}"
echo -e "${RED}失败: $FAILED${NC}"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}🎉 所有测试通过！${NC}"
    exit 0
else
    echo -e "${RED}⚠️  有 $FAILED 个测试失败${NC}"
    exit 1
fi
