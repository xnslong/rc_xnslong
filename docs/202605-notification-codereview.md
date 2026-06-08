# Code Review — 按 ai-coding-rule.md 规则扫描

> 扫描时间：2026-05（基于 commit c01909b 后的代码）
> 扫描范围：全部 `.go` 源文件（排除 `vendor`）

---

## 规则 1：函数/方法不超过 60 行

### 超限函数

| 函数 | 行数 | 文件 | 说明 |
|------|------|------|------|
| `main()` | **131** | cmd/notification-server/main.go:25 | 包含配置加载、MQ 客户端创建、路由注册、HTTP 服务器启动和优雅关闭 |
| `validateNode()` | **130** | internal/ingestion/validator.go:73 | 对 JSON Schema 所有类型（object/array/string/integer/number/boolean）的递归验证 |
| `loadDir()` | **120** | internal/config/loader.go:206 | 跨多个配置类型的文件和目录遍历 |
| `SetupSuiteWithConfig()` | **122** | test/e2e/suite.go:71 | E2E 测试套件初始化 |
| `NewClient()` | **75** | internal/mq/rabbitmq/mq.go:27 | AMQP 拓扑声明 + 连接创建 |
| `Ingest()` | **66** | internal/api/handler/ingestion.go:32 | POST 通知处理 |
| `ListNotifications()` | **65** | internal/db/postgres/db.go:194 | 通知列表查询 |
| `ProcessMessage()` | **64** | internal/delivery/worker.go:152 | 消息处理流程 |
| `convertType()` | **61** | internal/mapping/engine.go:388 | 类型转换 |

### 建议

- `main()` 可以拆分为 `setupDB()`, `setupMQ()`, `setupRouter()` 等命名函数
- `validateNode()` 可以将每个 type case 拆为独立函数
- `loadDir()` 可以分离 events 和 vendors 的遍历逻辑
- `NewClient()` 可引入 `defer` 清理模式（详见规则 3）

---

## 规则 2：禁止直接使用字面量（需提取为常量）

### 已完成重构（commit 086e236）

| 文件 | 提取的常量 |
|------|-----------|
| `internal/model/status.go` （新增） | Notification/DeliveryTask 状态常量，`FieldDeliveryTaskID`, `FieldVendorID` |
| `internal/port/config.go` | `DirectiveSource`, `DirectiveEach`, `DirectiveType`, `DirectiveFormat` |
| `handler/ingestion.go` | 错误码 (5), 中文消息 (8), JSON 字段键 (20), 分页参数, Content-Type |
| `delivery/worker.go` | HTTP 状态码范围, 引用 model 状态常量 |
| `config/loader.go` | 配置类型标签 (4), 日志属性, 模板前缀, schema 键 |
| `mapping/engine.go` | Body/Auth 类型, Scope, 类型名, 指令键引用 |
| `mq/rabbitmq/mq.go` | Content-Type, 头部名, routing key |
| `db/postgres/db.go` | 分页参数默认值 |
| `routing/dispatcher.go` | 引用 model 状态常量 |
| `ingestion/validator.go` | `validationRootPath` |
| `cmd/notification-server/main.go` | 路由路径, 消费者标签, 超时/并发设置 |

### 重构要点

- 跨文件共享常量归入 `model/status.go` 和 `port/config.go`
- `"$source"`, `"$each"`, `"$type"`, `"$format"` 等在 port 包统一定义，`mapping/engine.go` 和 `config/loader.go` 共用
- 通知状态字符串（`"PENDING"`, `"FAILED"` 等）在 worker.go、dispatcher.go、db.go 间共享

---

## 规则 3：禁止重复代码（2次以上需抽象）

### 高优先级（4 次以上）

| 重复模式 | 次数 | 位置 | 建议 |
|----------|------|------|------|
| **JSON 响应写入**（Set+WriteHeader+Encode） | 5 | `handler/ingestion.go` | 抽取 `writeJSON()` 辅助函数 |
| **RabbitMQ 清理+错误返回**（ch.Close/conn.Close/return error） | 9 | `mq/rabbitmq/mq.go` | 改用 `defer cleanup` 标志位模式 |
| **DB 行扫描**（DeliveryTask 12字段） | 3 | `db/postgres/db.go` | 考虑 `pgx.RowToStructByName` |
| **Config getter 守卫**（map lookup → ErrNotConfigured） | 3-4 | `config/loader.go` | 泛型 `lookup[T]()` |
| **ValidationError 构造**（append + struct literal） | 12+ | `ingestion/validator.go` | `typeMismatchError()` 辅助函数 |

### 中优先级（2-3 次）

| 重复模式 | 出现次数 | 位置 | 建议 |
|----------|----------|------|------|
| MQ 拓扑声明 | 2 处（整个函数重复） | `mq.go` + `suite.go` | 共享到 `rabbitmq` 包 |
| Retry 时长解析 | 2 处 | `loader.go` | 复用 `convertVendorRetryFile` |
| `recordError` switch | 4 个分支 | `loader.go` | 用 per-map 函数替代类型分发 |

### 低优先级

| 重复模式 | 次数 | 位置 | 建议 |
|----------|------|------|------|
| Auth 配置字段访问 | 3 | `engine.go` | `authConfigString()` |
| `convertType` 子 switch | 3 | `engine.go` | `toFloat64()` 数值辅助 |

### 关于 DB 行扫描的说明

三种可选方案：

| 方式 | 列序耦合 | 需要 tag | 样板代码 | 运行时安全 |
|:----|:--------:|:--------:|:--------:|:---------:|
| 手动 Scan（现状） | 有 | 不需要 | 多 | 列序不对静默错 |
| `pgx.RowToStructByPos` | 有 | 不需要 | 极少 | 同上 |
| `pgx.RowToStructByName` | **无** | 可选（有则按 tag，无则大小写不敏感匹配字段名） | 极少 | 列名不对静默错 |

**推荐 `RowToStructByName`**，列序解耦，性能经过 `sync.Map` 缓存后几乎无差异。详见 [pgx rows.go](https://github.com/jackc/pgx/blob/master/rows.go) 中 `namedStructFieldMap` 缓存。

### 关于 RabbitMQ defer 清理

三种变体及命名偏好：

**变体 1**（命名返回值 + defer 捕获 err）：
```go
func NewClient(url string) (c *Client, err error) {
    // ...
    defer func() {
        if err != nil { ch.Close(); conn.Close() }
    }()
    // ...
}
```

**变体 2**（布尔标志位）：
```go
cleanup := true
defer func() {
    if cleanup { ch.Close(); conn.Close() }
}()
// 失败直接 return；成功后：cleanup = false
```

**变体 3**（函数变量替换）：
```go
postOp := func() { ch.Close(); conn.Close() }
defer func() { postOp() }()
// 失败直接 return；成功后：postOp = func() {}
```

结论：三个变体都可，偏好 1 和 2。若用变体 3，**变量名应反映"最后执行的操作"**（如 `postOp` → `noop`），而非"最初想做的操作"（如 `cancel`）。

---

## 规则 4：单元测试只对"对外可见"单元测试

### 违反

| 测试文件 | 测试的目标 | 问题说明 |
|----------|-----------|----------|
| `internal/mapping/engine_test.go` | `resolveField`, `getNestedField`, `tostring`, `convertType`, `formatValue`, `resolveSourceDirective`, `resolveEachDirective` | **全部是未导出（private）函数**，应通过公共方法 `BuildRequest` 间接测试 |

### 边界情况

| 测试文件 | 说明 |
|----------|------|
| `internal/delivery/backoff_test.go` | 测试了私有函数 `calculateBackoff`，但有明确职责且行为稳定，可保留亦可重构 |
| `internal/config/walk_yaml_test.go` | 测试了导出函数 `walkYAML`，符合规则 ✅ |

---

## 反模式 1：正则表达式编译

**没有违反。** 代码库中唯一的正则编译：

```go
// internal/mapping/engine.go
var resolveRefRe = regexp.MustCompile(...)  // ✅ 包级 var，仅编译一次
```

所有其他字符串匹配都用 `strings.HasPrefix` / `strings.Contains` / `strings.Split` 等零分配方式完成。

---

## 规则 5：性能基准测试

核心路径：**`Ingest → dispatch → BuildRequest → HTTP delivery`**

建议为以下路径添加 `Benchmark*` 函数：
- `internal/mapping/engine.go` 的 `BuildRequest()`
- `internal/delivery/worker.go` 的 HTTP 调用部分
- `internal/routing/dispatcher.go` 的分发逻辑

当前代码未发现 `Benchmark*` 测试。

---

## 🏗 重构项 A：模板编译 — 预编译 `@{payload:...}` 引用为可执行结构

### 现状

运行时每调用一次 `BuildRequest`，engine.go 都需要对每个模板字符串执行：

```
regex.FindStringIndex → regex.FindStringSubmatch → strings.Split(".") → map lookup
```

对于包含 `$each` 嵌套的复杂模板（如订单通知含商品列表），一次请求可能触发十几到几十次 regex 解析。

同时，loader.go 用 `schemaHasPath()` / `validateString()` 在**加载时**做同一种解析，仅用于校验 ref 是否合法；engine.go 在**运行时**又做一遍同样的解析来取值。两套遍历逻辑维护同一套语法（`@{payload:}` / `@{item:}` / `$source` / `$each` / `$type` / `$format`）。

### 目标

```
加载时（一次）                      运行时（每请求）
┌──────────────────┐              ┌──────────────────┐
│ yarnaml 模板      │              │                  │
│ → 编译为          │              │  payload map     │
│   CompiledTemplate              │  + 编译后的树     │
│   ├─ StaticNode  │              │  → 直接取值       │
│   ├─ RefNode     │  ────────→   │    拼结果         │
│   ├─ EachNode    │              │                   │
│   └─ ...         │              │ 零字符串解析      │
└──────────────────┘              │ 零 regex          │
                                  └──────────────────┘
```

- **加载时**：parser 将模板字符串编译为一棵节点树（表达式 AST），同时执行 schema 校验（减少一次重复遍历）
- **运行时**：engine 接收编译好的树和数据，沿树行走直接取值，不做任何字符串解析

### 影响范围

| 组件 | 变化 |
|:----|:----|
| `internal/template/` (新) | 语法解析 + 节点类型定义 + 编译 |
| `config/loader.go` | 加载后调用编译，编译时完成校验，不再需要单独的 `validateCrossConfig` |
| `mapping/engine.go` | `resolveField` / `resolveSourceDirective` / `resolveNode` 改为走编译树，删除 regex |

### 优先级

🔴 **P0** — 既是性能优化（消除运行时 regex），也是架构重构（统一引用遍历职责）。

---

## 优先级汇总

| 优先级 | 问题 | 影响 | 状态 |
|--------|------|------|------|
| 🔴 P0 | `engine_test.go` 测试私有函数 | 违规，重构时测试不可靠 | 待处理 |
| 🔴 P0 | 9 个函数超过 60 行 | 可维护性差 | 待处理 |
| 🔴 P0 | **重构项 A：模板编译** | 运行时性能 + 架构统一 | 待处理 |
| 🟡 P1 | JSON 响应写入重复 5 次 | 违反规则 3，易改 | 待处理 |
| 🟡 P1 | DB 扫描重复 3 次 | 违反规则 3 | 待处理 |
| 🟡 P1 | ValidationError 构造重复 12+ 次 | 违反规则 3 | 待处理 |
| 🟡 P1 | Config getter 守卫重复 | 违反规则 3 | 待处理 |
| 🟢 P2 | MQ 拓扑声明在 e2e 中重复 | 违反规则 3 | 待处理 |
| 🟢 P2 | Auth/MQ cleanup/convertType 等 | 违反规则 3 | 待处理 |
| ✅ | 字面量（规则 2） | 已完成 | 已提交 |
| ✅ | 正则编译（反模式） | 无问题 | — |
| ✅ | 单元测试覆盖 | 基本合规 | — |
