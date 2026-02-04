# Changelog

## [Unreleased] - 2024-02-04

### Added
- 实现动态模型列表获取功能，通过调用 Kiro API `/ListAvailableModels` 获取账号实际可用的模型
- 添加模型列表缓存机制（1小时TTL），减少API调用次数
- 在模型验证失败时返回账号可用的模型列表，提供更好的错误提示

### Changed
- 重构 `AuthManager.GetAvailableModels()` 方法：
  - 优先从缓存读取模型列表
  - 缓存失效时调用 `ListAvailableModels()` API
  - API 失败时降级到预定义的 `AvailableModels` 列表
  - 确保返回值永远不为 nil，提高健壮性
- 删除 `GetAvailableModelsForProvider()` 函数（不再根据 Provider 推测模型权限）

### Fixed
- 修复模型验证错误时 `valid_models` 字段返回 null 的问题
- 修复测试文件中重复定义 `TestIsValidModel` 的问题

### Technical Details

#### 新增 API 调用
```go
// ListAvailableModels 调用 Kiro API 获取账号可用的模型列表
func (m *AuthManager) ListAvailableModels() ([]Model, error)
```

**API Endpoint**: `POST https://q.{region}.amazonaws.com/ListAvailableModels?origin=AI_EDITOR`

**请求体**:
```json
{
  "origin": "AI_EDITOR"
}
```

**响应体**:
```json
{
  "models": [
    {
      "id": "claude-sonnet-4.5",
      "name": "Claude Sonnet 4.5",
      "description": "The latest Claude Sonnet model",
      "credit": 1.3
    }
  ]
}
```

#### 缓存机制
- 缓存时长：1小时
- 缓存字段：`AuthManager.cachedModels` 和 `AuthManager.modelsLoadedAt`
- 缓存策略：首次调用或缓存过期时调用 API，成功后更新缓存

#### 降级策略
1. 优先使用缓存（1小时内有效）
2. 缓存失效时调用 API
3. API 失败或返回空列表时，使用预定义的 `AvailableModels`
4. 确保返回值永远不为 nil（创建切片副本）

### Testing
- ✅ 所有单元测试通过（17个测试用例）
- ✅ 模型验证测试通过
- ✅ 无效模型返回账号可用模型测试通过
- ✅ API 集成测试通过

### Migration Guide

**无需迁移**，API 完全向后兼容。

原有代码：
```go
models := client.Auth.GetAvailableModels()
```

新行为：
- 首次调用会尝试从 Kiro API 获取真实的模型列表
- API 失败时自动降级到预定义列表
- 后续调用使用缓存（1小时内）

### Benefits

1. **动态适配**：自动获取账号实际可用的模型，无需手动维护
2. **未来兼容**：新模型（如 Opus 5）上线后自动支持，无需修改代码
3. **性能优化**：缓存机制减少 API 调用次数
4. **健壮性**：API 失败时自动降级，确保服务可用
5. **用户体验**：错误提示中包含可用模型列表，方便用户选择

### Known Issues

无

### Next Steps

- [ ] 考虑添加模型列表刷新接口（手动刷新缓存）
- [ ] 考虑添加模型能力查询接口（查询特定模型的详细信息）
- [ ] 考虑添加模型使用统计接口（查询模型消耗的 credit）
