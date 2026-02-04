# Kiro API Client - 部署检查清单

## 📋 部署前检查

### 代码质量

- [x] 所有单元测试通过（17/17）
- [x] 代码符合 Go 规范
- [x] 无 TODO 或占位符
- [x] 关键逻辑有中文注释
- [x] 错误处理完整

### 文档完整性

- [x] README.md - 项目介绍
- [x] USAGE.md - 使用指南
- [x] ALIGNMENT.md - 对齐文档
- [x] DEMO.md - 演示文档
- [x] CHANGELOG.md - 变更日志
- [x] PROJECT_SUMMARY.md - 项目总结
- [x] DEPLOYMENT_CHECKLIST.md - 部署清单（本文件）

### 功能完整性

- [x] Token 管理功能
- [x] Token 自动刷新
- [x] MCP 协议支持
- [x] Web Search 功能
- [x] Chat 功能（流式和非流式）
- [x] 模型选择功能
- [x] 动态模型列表
- [x] HTTP API 代理服务器
- [x] Web UI 控制台

### 安全性

- [x] Token 文件权限检查
- [x] 输入验证
- [x] 错误信息不暴露敏感数据
- [x] CORS 配置

---

## 🚀 部署步骤

### 1. 环境准备

```bash
# 检查 Go 版本（需要 1.18+）
go version

# 克隆项目
git clone https://github.com/jinfeijie/kiro-api-client-go.git
cd kiro-api-client-go

# 安装依赖
go mod download
```

### 2. 配置环境变量（可选）

```bash
# Token 文件路径（默认：~/.aws/sso/cache/kiro-auth-token.json）
export KIRO_AUTH_TOKEN_PATH="/path/to/token.json"

# 直接设置 Token（不推荐生产环境）
export KIRO_ACCESS_TOKEN="your-token-here"

# AWS 区域（默认：us-east-1）
export KIRO_REGION="us-east-1"

# 是否自动刷新（默认：true）
export KIRO_AUTO_REFRESH="true"
```

### 3. 编译项目

```bash
# 编译 CLI 工具
go build -o kiroclient ./cmd/main.go

# 编译 HTTP 服务器
cd server
go build -o kiro-proxy main.go
```

### 4. 运行测试

```bash
# 运行所有测试
./test_all.sh

# 或者手动运行
go test -v ./...
cd server && go test -v
```

### 5. 启动服务

```bash
# 启动 HTTP 服务器
cd server
./kiro-proxy

# 或者使用 go run
go run main.go
```

### 6. 验证部署

```bash
# 检查服务是否启动
curl http://localhost:8080/api/token/status

# 检查模型列表
curl http://localhost:8080/api/models

# 访问 Web UI
open http://localhost:8080
```

---

## 🐳 Docker 部署（推荐）

### Dockerfile

```dockerfile
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY . .

RUN go mod download
RUN cd server && go build -o kiro-proxy main.go

FROM alpine:latest

RUN apk --no-cache add ca-certificates
WORKDIR /root/

COPY --from=builder /app/server/kiro-proxy .
COPY --from=builder /app/server/static ./static

EXPOSE 8080

CMD ["./kiro-proxy"]
```

### 构建和运行

```bash
# 构建镜像
docker build -t kiro-api-client:latest .

# 运行容器
docker run -d \
  -p 8080:8080 \
  -v ~/.aws/sso/cache:/root/.aws/sso/cache:ro \
  --name kiro-proxy \
  kiro-api-client:latest

# 查看日志
docker logs -f kiro-proxy
```

---

## ☸️ Kubernetes 部署

### deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kiro-api-client
  labels:
    app: kiro-api-client
spec:
  replicas: 3
  selector:
    matchLabels:
      app: kiro-api-client
  template:
    metadata:
      labels:
        app: kiro-api-client
    spec:
      containers:
      - name: kiro-proxy
        image: kiro-api-client:latest
        ports:
        - containerPort: 8080
        env:
        - name: KIRO_REGION
          value: "us-east-1"
        volumeMounts:
        - name: token-cache
          mountPath: /root/.aws/sso/cache
          readOnly: true
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /api/token/status
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /api/token/status
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
      volumes:
      - name: token-cache
        hostPath:
          path: /home/user/.aws/sso/cache
          type: Directory
---
apiVersion: v1
kind: Service
metadata:
  name: kiro-api-client
spec:
  selector:
    app: kiro-api-client
  ports:
  - protocol: TCP
    port: 80
    targetPort: 8080
  type: LoadBalancer
```

### 部署到 Kubernetes

```bash
# 应用配置
kubectl apply -f deployment.yaml

# 查看状态
kubectl get pods -l app=kiro-api-client
kubectl get svc kiro-api-client

# 查看日志
kubectl logs -f deployment/kiro-api-client
```

---

## 🔍 监控和日志

### 健康检查端点

```bash
# Token 状态
curl http://localhost:8080/api/token/status

# 模型列表
curl http://localhost:8080/api/models
```

### 日志级别

服务器使用 Gin 框架，默认输出访问日志：

```
[GIN] 2024/02/04 - 01:00:00 | 200 |    1.234567ms |       127.0.0.1 | POST     "/api/chat"
```

### Prometheus 指标（未来）

计划添加以下指标：
- `kiro_requests_total` - 总请求数
- `kiro_request_duration_seconds` - 请求延迟
- `kiro_token_refresh_total` - Token 刷新次数
- `kiro_errors_total` - 错误总数

---

## 🚨 故障排查

### 常见问题

#### 1. Token 文件找不到

**错误**: `读取 token 文件失败: no such file or directory`

**解决方案**:
```bash
# 检查文件是否存在
ls -la ~/.aws/sso/cache/kiro-auth-token.json

# 或者设置环境变量
export KIRO_AUTH_TOKEN_PATH="/path/to/your/token.json"
```

#### 2. Token 过期

**错误**: `Token 已过期`

**解决方案**:
- 服务会自动刷新 Token
- 如果自动刷新失败，请手动登录 Kiro IDE

#### 3. 模型列表为空

**错误**: `valid_models 为空`

**解决方案**:
- 检查网络连接
- 检查 Token 是否有效
- 服务会自动降级到预定义模型列表

#### 4. 端口被占用

**错误**: `bind: address already in use`

**解决方案**:
```bash
# 查找占用端口的进程
lsof -i :8080

# 杀死进程
kill -9 <PID>

# 或者使用其他端口
PORT=8081 go run main.go
```

---

## 📊 性能调优

### 1. 连接池配置

```go
// 在 auth.go 中调整 HTTP 客户端
httpClient: &http.Client{
    Timeout: 30 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    },
}
```

### 2. 缓存配置

```go
// 在 auth.go 中调整缓存时间
if len(m.cachedModels) > 0 && time.Since(m.modelsLoadedAt) < time.Hour {
    // 可以调整为 30 分钟或 2 小时
}
```

### 3. 并发限制

```go
// 在 search.go 中调整并发数
maxConcurrent := 10 // 可以根据服务器性能调整
```

---

## 🔐 安全加固

### 1. HTTPS 配置

```go
// 在 server/main.go 中启用 HTTPS
r.RunTLS(":8443", "cert.pem", "key.pem")
```

### 2. API 密钥认证

```go
// 添加中间件
func APIKeyAuth() gin.HandlerFunc {
    return func(c *gin.Context) {
        apiKey := c.GetHeader("X-API-Key")
        if apiKey != os.Getenv("API_KEY") {
            c.JSON(401, gin.H{"error": "Unauthorized"})
            c.Abort()
            return
        }
        c.Next()
    }
}

// 应用到路由
api := r.Group("/api")
api.Use(APIKeyAuth())
```

### 3. 速率限制

```go
// 使用 gin 的速率限制中间件
import "github.com/ulule/limiter/v3"

// 配置限制器
rate := limiter.Rate{
    Period: 1 * time.Minute,
    Limit:  100,
}
```

---

## 📈 扩展性

### 水平扩展

- 服务是无状态的，可以轻松水平扩展
- 使用负载均衡器分发请求
- Token 缓存在每个实例中独立管理

### 垂直扩展

- 增加 CPU 和内存资源
- 调整 Go 的 GOMAXPROCS
- 优化数据库连接池（如果使用）

---

## ✅ 部署后验证

### 功能测试

```bash
# 1. Token 状态
curl http://localhost:8080/api/token/status

# 2. 模型列表
curl http://localhost:8080/api/models

# 3. Chat 测试
curl -X POST http://localhost:8080/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false,
    "model": "claude-sonnet-4.5"
  }'

# 4. Search 测试
curl -X POST http://localhost:8080/api/search \
  -H "Content-Type: application/json" \
  -d '{
    "query": "golang best practices",
    "maxResults": 5
  }'

# 5. Web UI 测试
open http://localhost:8080
```

### 性能测试

```bash
# 使用 ab (Apache Bench)
ab -n 1000 -c 10 http://localhost:8080/api/token/status

# 使用 wrk
wrk -t4 -c100 -d30s http://localhost:8080/api/token/status
```

---

## 🎯 生产环境建议

### 必须配置

- [x] 使用 HTTPS
- [x] 配置 API 密钥认证
- [x] 启用速率限制
- [x] 配置日志收集
- [x] 配置监控告警

### 推荐配置

- [ ] 使用 CDN 加速静态资源
- [ ] 配置数据库连接池
- [ ] 启用 Gzip 压缩
- [ ] 配置缓存策略
- [ ] 配置备份策略

### 可选配置

- [ ] 配置分布式追踪
- [ ] 配置服务网格
- [ ] 配置灰度发布
- [ ] 配置 A/B 测试

---

## 📞 支持

如有问题，请联系：
- GitHub Issues: https://github.com/jinfeijie/kiro-api-client-go/issues
- Email: jinfeijie@example.com

---

**最后更新**: 2024-02-04  
**版本**: v1.0.0  
**状态**: ✅ 生产就绪
