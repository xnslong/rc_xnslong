# E2E 测试编写指南

## 概述

本系统使用黑盒 E2E 测试验证通知系统的行为。测试只通过 HTTP API 与系统交互，通过 MockVendor 观察外部 vendor 的调用情况。不直接访问 DB 或 MQ。

## 生命周期

所有 API 都以 `e2e` 包的包级函数暴露，内部依赖全局 Suite 对象。

```
TestMain (全局一次)
  ├── e2e.SetupSuite()
  │   ├── 加载配置
  │   ├── 排除 e2e.ExcludeVendors(...) 中指定的 vendor
  │   └── 启动其余所有 MockVendors
  │
  ├── TestFoo (每个测试用例)
  │   ├── e2e.Setup()                         ← 恢复基础设施到初始状态
  │   ├── defer e2e.TearDown()                ← 清除这个用例的痕迹
  │   └── <测试逻辑>
  │
  ├── TestBar (同上)
  │   ├── e2e.Setup()
  │   ├── defer e2e.TearDown()
  │   └── <测试逻辑>
  │
  └── e2e.TearDownSuite()
      ├── stop shared server
      ├── stop 所有 MockVendors
      └── cleanups
```

### 规则

- `e2e.SetupSuite()` / `e2e.TearDownSuite()` 只在 `TestMain` 中出现一次，**不允许**在测试函数中调用。
- `e2e.Setup()` / `e2e.TearDown()` 每个 `TestXxx` 函数中调用一次，`defer e2e.TearDown()` 在 `e2e.Setup()` 之后立即声明。
- `e2e.Setup()` 负责恢复基础设施到初始状态（重启被停的 server/vendor、关闭多开的 vendor），保证当前用例的执行前提。
- `e2e.TearDown()` 只负责清除当前用例的痕迹（重置 vendor 请求和行为），不做基础设施恢复。
- Shared server 由 `e2e.Setup()` 的恢复逻辑保障运行，由 `e2e.TearDownSuite()` 最终关闭。
- Shutdown 测试允许调用 `e2e.StopServer()` 停掉 shared server；后续用例的 `e2e.Setup()` 会自动重启。
- Shutdown 测试如果需要验证重启后行为，可显式调用 `e2e.StartServer()` 重启 server。

## 测试数据配置

### 目录结构

所有测试配置统一放在 `test/e2e/testdata/` 目录下，不使用子目录隔离场景。

```
test/e2e/testdata/
├── events/{biz}/                    # event definitions, grouped by business domain
│   ├── events/{event}.yaml          #   event schema (JSON Schema Draft-07)
│   └── routes/{event}.yaml          #   routing rules (event_type → vendor_id)
└── vendors/{vendor}/                # vendor configurations
    ├── vendor.yaml                  #   base config (method, url, headers, retry, judgment)
    └── {biz}/{event}.yaml           #   delivery contract (body.template + optional field overrides)
```

### 事件命名规则

- 事件名在整个系统中**全局唯一**，`{biz}` 目录不提供命名空间隔离。
- 格式：`{biz}.{event}`，例如 `order.paid`、`tc5.wait_delivery`。
- 不同测试场景使用不同事件名，**不要**借用同一个事件名来测试不同行为。
- 每个事件只能有一个 schema 定义。

### 特殊场景例外

以下类型测试可以使用独立的配置目录（不在统一 testdata 中），或通过 `e2e.ExcludeVendors` 排除不需要的 vendor mock：

1. **Config loader 测试** — 使用独立配置目录（放在 `test/e2e/testdata/{场景名}/`），不启动 server 和 vendors。这些测试应最终迁移到 `internal/config/` 下。
2. **网络不可达测试** — 通过 `e2e.SetupSuite(e2e.ExcludeVendors("unreachable_vendor"))` 排除该 vendor 的 mock，让端口上没有任何服务监听。
3. **Vendor 崩溃恢复测试** — 通过 `e2e.StopVendor(id)` / `e2e.StartVendor(id)` 在用例中模拟 vendor 挂掉和恢复。`e2e.TearDown()` 自动恢复到初始状态。

## 测试编写模式

### 标准模式

```go
func TestMyScenario(t *testing.T) {
    e2e.Setup()
    defer e2e.TearDown()

    // 1. 注册 vendor 行为（如需要覆盖默认 200）
    e2e.Vendor("my_vendor").RegisterBehavior([]e2e.MockResponse{
        {StatusCode: 200, Body: "ok"},
    })

    // 2. 发送请求
    body := `{
        "event": "my.event",
        "idempotent_key": "my-key-1",
        "payload": {"field": "value"}
    }`
    resp, err := http.Post(e2e.ServerURL()+"/api/v1/notifications", "application/json", strings.NewReader(body))
    require.NoError(t, err)
    require.Equal(t, http.StatusAccepted, resp.StatusCode)

    // 3. 等待 vendor 收到请求并验证
    vendorReq := e2e.Vendor("my_vendor").WaitRequest(15 * time.Second)
    require.NotNil(t, vendorReq)
    assert.Equal(t, "POST", vendorReq.Method)

    // 4. 等待通知状态并验证
    status, err := e2e.WaitForNotificationStatus(notifID, []string{"SUCCEEDED"}, 10*time.Second)
    require.NoError(t, err)
    assert.Equal(t, "SUCCEEDED", status)
}
```

### 黑盒原则

- **不直接访问 DB** — 不查询数据库表来验证状态。所有状态通过 HTTP API（`GET /api/v1/notifications/{id}`）验证。
- **不直接操作 MQ** — 不连接 RabbitMQ 来检查队列或投递消息（`mq_debug_test.go` 这种基础设施验证除外）。
- **不声明 MQ 拓扑** — MQ 的 exchange/queue/binding 由被测服务在启动时自己声明。
- **不清理 DB/MQ 数据** — 每次运行使用全新 Docker 容器，测试无需自行清理。

### 包级 API 速查

```go
// 生命周期（仅 TestMain 调用）
e2e.SetupSuite(opts ...Option)    // 启动基础设施（vendors、config）
e2e.TearDownSuite()               // 停止所有资源

// 生命周期（每个用例调用）
e2e.Setup()                       // 确保 server 运行 + 重置状态
e2e.TearDown()                    // 恢复初始状态（不关 server）

// Server 控制
e2e.StartServer()                 // 启动 shared server
e2e.StopServer()                  // 停止 shared server（优雅关闭）
e2e.ServerURL()                   // 获取 shared server HTTP 地址

// Vendor 控制
e2e.StartVendor(id)               // 启动指定 vendor 的 mock
e2e.StopVendor(id)                // 停止指定 vendor 的 mock
e2e.Vendor(id) *MockVendor        // 获取 vendor 的 mock 实例（未启动时返回 nil）

// 选项
e2e.ExcludeVendors(ids ...string) Option  // 排除指定 vendor 不启动
```

### MockVendor 用法

- `RegisterBehavior([]MockResponse)` — 设置响应序列。超出注册数量的调用重复最后一个响应。
- `WaitRequest(timeout)` — 阻塞直到收到请求，返回请求内容。超时返回 nil。
- `Requests()` — 返回所有收到的请求（用于验证总调用次数）。
- `LatestRequest()` — 返回最近一次请求。
- 默认行为（未调用 `RegisterBehavior`）返回 `200 OK`。

### Shutdown 测试模式

```go
func TestShutdown_WaitDelivery(t *testing.T) {
    e2e.Setup()
    defer e2e.TearDown()

    // 1. 配置慢响应 vendor
    e2e.Vendor("sd-wait-vendor").RegisterBehavior([]e2e.MockResponse{
        {StatusCode: 200, Body: "ok", Delay: 3 * time.Second},
    })

    // 2. 发送通知
    // 3. 等待 vendor 收到请求

    // 4. 停掉 shared server（模拟优雅关闭）
    e2e.StopServer()

    // 5. 验证 vendor 在旧进程中被调用了一次（无重试）
    require.Equal(t, 1, len(e2e.Vendor("sd-wait-vendor").Requests()))

    // 6. 重启 server 验证结果
    e2e.StartServer()

    // 7. 查询 API 确认 SUCCEEDED
    // 8. 验证 vendor 仍然只被调用了一次（无重启后重新投递）

    // 注意：e2e.TearDownSuite() 不在测试函数中调用。
    // 后续用例的 e2e.Setup() 会确保 server 恢复运行。
}
```

### 重试测试模式

```go
func TestShutdown_RetryOnSigterm(t *testing.T) {
    e2e.Setup()
    defer e2e.TearDown()

    // 注册 vendor 行为：第一次 503（慢响应）
    e2e.Vendor("sd-retry-vendor").RegisterBehavior([]e2e.MockResponse{
        {StatusCode: 503, Body: `{"error":"unavailable"}`, Delay: 3 * time.Second},
    })

    // 发送通知 → 等待 vendor 收到第一次请求

    e2e.StopServer()  // 旧进程未 ACK，消息回到 MQ

    // 启动一个新的 server 进程（独立进程，不干扰 shared server）
    // 旧进程的 retry 消息在 MQ 中，新 server 会消费并重试
    // 注册第二个 vendor 实例来验证重试成功

    // 验证状态最终变为 SUCCEEDED
    // 验证 vendor（新实例）被调用了（重试成功）
}
```

### Config 加载测试模式

```go
func TestConfig_Scenario(t *testing.T) {
    // 使用独立配置目录 + config.NewLoader 直接测试加载逻辑
    // 不启动 server，不启动 vendors（不是端到端测试）
    // 这些测试应放在 internal/config/ 下，而非 test/e2e/
    loader, err := config.NewLoader(testdataDir)
    require.NoError(t, err)
    err = loader.Load(context.Background())
    // ... 验证加载结果
}
```

## 断言规则

- 使用 `github.com/stretchr/testify` 做断言。
- `require.*` 用于前置条件（不满足则中止测试）。
- `assert.*` 用于期望结果（不满足但继续执行）。
- API 响应结构使用 `apiResponse` / `apiErrorResponse` 类型。

## 文件组织

| 文件 | 职责 |
|------|------|
| `main_test.go` | `TestMain` + 全局 suite 生命周期（SetupSuite/TearDownSuite） |
| `suite.go` | Suite 基础设施 + 包级 API（Setup/TearDown/Server/Vendor） |
| `mock_vendor.go` | Mock vendor 实现 |
| `ingestion_test.go` | 通知接收 API 测试 |
| `full_chain_test.go` | 全链路投递测试 |
| `mapping_content_test.go` | 映射引擎内容测试 |
| `shutdown_test.go` | 优雅关闭相关测试 |
| `config_test.go` | 配置加载器测试（FIXME: 应迁移到 internal/config） |
| `config_validation_test.go` | 模板字段校验测试 |
| `mq_debug_test.go` | MQ 连通性调试（基础设施验证，非业务测试） |
