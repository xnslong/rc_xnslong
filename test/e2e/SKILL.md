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

以下类型测试可以使用独立的配置目录（不在统一 testdata 中），或通过 `e2e.ExcludeVendors` / `e2e.IncludeVendors` 精确控制 vendor mock：

1. **配置边界测试** — 脏 vendor 直接存放在共享的 `test/e2e/testdata/vendors/` 下，与正常 vendor 共存。mock 仍然启动（见 §Mock Vendor 必须启动原则）。测试通过共享套件的 HTTP API 观察不同 vendor 的投递结果，验证脏配置的隔离性。
2. **网络不可达测试** — 通过 `e2e.SetupSuite(e2e.ExcludeVendors("unreachable_vendor"))` 排除该 vendor 的 mock，让端口上没有任何服务监听。
3. **Vendor 崩溃恢复测试** — 通过 `e2e.StopVendor(id)` / `e2e.StartVendor(id)` 在用例中模拟 vendor 挂掉和恢复。`e2e.TearDown()` 自动恢复到初始状态。

## 测试编写模式

### 幂等键（idempotent_key）约定

每个用例的幂等键必须是**新鲜且唯一**的，避免直接 `go test` 重跑时因 PG/MQ 残留数据撞到幂等去重：

- 格式：`e2e.NewTestID("TCX.Y-{slug}")`，其中 `TCX.Y-{slug}` 是 `@test-case` 标注的用例 ID
- `e2e.NewTestID()` 自动追加纳秒级时间戳保证唯一性
- 幂等去重测试（TC1.3）是例外：生成一次 key，两次 POST 用同一个
- 不传 idempotent_key 的测试（TC1.1-auto_idempotent_key）不受此约束

```go
// 标准用法
body := fmt.Sprintf(`{
    "event": "order.paid",
    "idempotent_key": "%s",
    "payload": {...}
}`, e2e.NewTestID("TC3.1-matched_vendor_called"))

// 幂等去重测试 — 生成一次 key，整个用例复用
dedupKey := e2e.NewTestID("TC1.3-duplicate_idempotent_key")
body := fmt.Sprintf(`{..., "idempotent_key": "%s", ...}`, dedupKey)
// 第一次 POST 用 body（含 dedupKey）
// 第二次 POST 用同一个 body（同一个 dedupKey）
```

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
    status, err := e2e.WaitForNotificationStatus(notifID, []string{e2e.StatusSucceeded}, 10*time.Second)
    require.NoError(t, err)
    assert.Equal(t, e2e.StatusSucceeded, status)
}
```

### 黑盒原则

- **不直接访问 DB** — 不查询数据库表来验证状态。所有状态通过 HTTP API（`GET /api/v1/notifications/{id}`）验证。
- **不直接操作 MQ** — 不连接 RabbitMQ 来检查队列或投递消息（`mq_debug_test.go` 这种基础设施验证除外）。
- **不声明 MQ 拓扑** — MQ 的 exchange/queue/binding 由被测服务在启动时自己声明。
- **不清理 DB/MQ 数据** — 每次运行使用全新 Docker 容器，测试无需自行清理。

### Mock Vendor 必须启动原则

E2E 测试是黑盒测试，**不基于对系统内部实现方式的假设做任何决策**。测试只模拟端点行为，通过端点行为来验证系统行为是否正确。

因此：

- **凡是 route 文件中引用的 vendor，其 mock 必须启动。** 不论该 vendor 的配置（vendor.yaml、delivery contract）是否合法。
- **即使预期系统不会向某个 vendor 发请求，其 mock 也必须启动并监听对应端口。** 这是唯一能区分"系统正确不发请求"和"系统有 bug 但 mock 没启动所以检测不到"的方式。
- **唯一的例外是网络不可达测试**（如 TC3.5），通过 `ExcludeVendors` 排除 mock 启动，在该端口上确实没有任何服务监听。

```go
// 正确：即使预期 bad_vendor 收不到请求，mock 也必须启动
// 然后通过 .Requests() 断言为 0 来验证系统确实没发请求
assert.Empty(t, e2e.Vendor("bad_vendor_18001").Requests(),
    "bad_vendor_18001 should receive 0 requests due to config error")

// 错误：不启动 mock → 测试无法区分"正确没发"和"系统错误但没被发现"
```

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
e2e.IncludeVendors(specs ...VendorSpec) Option  // 为配置损坏的 vendor 声明端口并启动 mock
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

Config 边界测试验证系统在配置文件中存在脏数据时的容错行为。关键在于**测试必须使用与正常测试相同的共享配置目录**，否则无法验证"脏配置与正常配置共存时的隔离性"。脏 vendor 直接存放在 `test/e2e/testdata/vendors/` 下，与正常 vendor 共存。所有 route 中引用的 vendor（包括脏 vendor）的 mock 都会启动（见 §Mock Vendor 必须启动原则），保证测试能通过请求计数来验证系统行为。

在 `main_test.go` 中通过 `IncludeVendors` 为配置损坏的 vendor 声明端口：

```go
func TestMain(m *testing.M) {
    os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
    SetupSuite(
        ExcludeVendors("unreachable_vendor_19999"),
        IncludeVendors(
            VendorSpec{ID: "bad_vendor_18001", Port: 18001},
            VendorSpec{ID: "invalid_retry_vendor_18002", Port: 18002},
            VendorSpec{ID: "missing_yaml_vendor_18003", Port: 18003},
        ),
    )
    defer TearDownSuite()
    // ...
}
```

`IncludeVendors` 补充那些 vendor.yaml 损坏（语法错误、文件缺失、retry 配置无效）的 vendor，让测试框架能直接启动 mock，不依赖系统对 vendor.yaml 的解析能力。端口号是测试编写者在创建 vendor.yaml 时就约定好的。

然后测试通过断言 mock 请求计数来验证系统行为：

```go
func TestConfig_PartialAvailability(t *testing.T) {
    t.Run("TC4.2-vendor-invalid-yaml", func(t *testing.T) {
        e2e.Setup()
        defer e2e.TearDown()

        // POST 通知到共享 server，路由到 mapping_vendor（好）和 bad_vendor（配置损坏）
        // → 共享 server 使用与正常测试相同的 testdata 目录
        notifID := postNotification(t, e2e.ServerURL(), body)
        status, results := waitForDelivery(t, e2e.ServerURL(), notifID, 15*time.Second)

        // mapping_vendor: SUCCEEDED
        // bad_vendor_18001:  DEAD_LETTER → last_error 是 YAML 解析错误，不是 "not configured"

        // 即使预期不会收到请求，mock 必须启动才能验证
        assert.Empty(t, e2e.Vendor("bad_vendor_18001").Requests(),
            "bad_vendor_18001 should receive 0 requests (DEAD_LETTER)")
    })
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
| `config_test.go` | 配置边界测试（非法 YAML、路径不存在）——通过独立 server + HTTP API 验证 |
| `config_validation_test.go` | 模板字段校验测试 |
| `mq_debug_test.go` | MQ 连通性调试（基础设施验证，非业务测试） |
