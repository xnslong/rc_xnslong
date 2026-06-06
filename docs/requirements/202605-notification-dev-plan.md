# API 通知系统 — 开发计划

> 版本: v0.1
> 日期: 2026-05-25
> 基于: HLD v0.2, DD v0.1

## 开发原则

| 原则 | 说明 |
|------|------|
| **TDD（测试先行）** | 先写测试用例和依赖的接口代码，再实现直至测试全绿 |
| **自顶向下** | 从入口层（Ingestion）逐层向下到 Routing → Delivery |
| **敏捷迭代** | 每层先出 MVP 版本，逐步丰富，每步跑测试快速反馈 |
| **Docker** | PostgreSQL + RabbitMQ 通过 Docker Compose 管理，开发期间常驻运行 |

## 测试分层策略

基于 DD §10 的 Outside-In TDD，E2E 测试对外部行为定义合格线，编排测试覆盖内部决策分支：

| 测试层 | 作用 | 写入时机 | 变绿时机 |
|--------|------|----------|----------|
| **E2E 测试** | 全链路黑盒验收（仅通过 HTTP 公开 API 交互） | Iteration 1 一次写完 | Iteration 4 完成时 |
| **编排测试** | 组件级 mock 测试，覆盖决策分支和异常路径 | 每个 Iteration 的 Step 1 | 同 Iteration |
| **契约测试** | 验证 DB/MQ 适配器与真实中间件交互正确 | 每个 Iteration 的 Step 1 | 同 Iteration |
| **算法测试** | 纯函数边界覆盖（backoff、resolveString） | 对应 Iteration | 同 Iteration |

E2E 测试作为黑盒，不观测和断言任何内部状态（DB、MQ、日志），只通过 HTTP 公开 API 验证系统行为。

---

## 迭代计划

### Iteration 0: 项目骨架 + 基础设施

#### Docker 组件

| 组件 | 镜像 | 端口 | 用途 |
|------|------|------|------|
| PostgreSQL | `postgres:16-alpine` | 5432 | 通知/投递任务/死信记录持久化 |
| RabbitMQ | `rabbitmq:4-management` | 5672(AMQP), 15672(UI) | MQ 触发/投递/延迟重试 |

**使用方式**：通过 `docker-compose.yml` 定义，开发期间常驻运行。各测试层（契约测试、E2E 测试）通过环境变量 `DATABASE_URL` / `MQ_URL` 连接。服务默认连接地址 `localhost:5432` / `localhost:5672`。

| 任务 | 文件 | 测试 |
|------|------|------|
| Docker Compose | `docker-compose.yml` | docker compose up -d |
| 目录结构 | `internal/` 各子包 | go build ./... |
| 领域模型 | `internal/model/notification.go`, `delivery_task.go` | — |
| Port 接口 | `internal/port/`（db.go, mq.go, config.go） | — |
| SQL 迁移 | `migrations/001_init.up.sql` | — |

> 本 Iteration 是项目骨架，无需 TDD。

---

### Iteration 1: E2E 验收测试（定义合格线）

> 本迭代只写 E2E 测试，不实现任何组件。E2E 定义的是从 Iteration 2 到 Iteration 4 全过程的合格线，写入后不再修改，始终保持 RED 直到 Iteration 4 完成。

**文件**：`test/e2e/e2e_test.go`

#### 测试生命周期设计

```
TestMain:
  ├── ① testcontainers 启动 PostgreSQL + RabbitMQ
  ├── ② 运行 SQL 迁移（建表）
  ├── ③ 在临时目录写入配置 YAML（vendors、mappings、routing_rules）
  ├── ④ 启动 mock vendor HTTP 服务
  ├── ⑤ 在进程中启动完整系统（HTTP 服务 + 路由分发器 + Worker 池）
  ├── 运行所有测试用例（支持串行或并行）
  └── ⑥ 清理：关闭系统 → 关闭 mock vendor → 关闭容器
```

#### Mock Vendor 设计

一个可编程的 HTTP 服务，职责只有两个：**按规则响应**、**记录请求**。

**交互方式**：测试用例通过 `X-Test-Case-Id` header 与 mock vendor 协作。

```
                    测试用例                            Mock Vendor
                    ──────────                        ──────────
  ① 行为配置         设定用例 X 的响应序列              按 ID 存储序列
  ② 系统调用         提交通知（payload 含 _test_case_id）
                          → 系统路由到 vendor
                          → vendor 请求携带 X-Test-Case-Id header
                                                    匹配 ID → 从序列取下一个响应返回
                                                    记录本次请求（Method/Path/Header/Body）
  ③ 结果验证         查询用例 X 的请求记录              返回记录的请求列表
```

**三要素**：

| 要素 | 说明 |
|------|------|
| **配置响应序列** | 测试用例指定"用例 X 的响应序列是什么"，如`[503, 503, 200]`表示前两次失败、第三次成功 |
| **记录请求** | mock vendor 自动记录每次收到的 Method、Path、Header、Body，按 X-Test-Case-Id 归类 |
| **查询记录** | 测试用例按用例 ID 查询请求记录（请求列表 + 调用次数） |

**默认行为**：未配置的用例 ID 始终返回 200。

**`_test_case_id` 传递方式**：作为 payload 中的普通字段，通过映射规则的 header 配置传递：

```yaml
# mappings/crm_system/order.paid.yaml
request:
  headers:
    X-Test-Case-Id: "@{payload._test_case_id}"
```

系统将其视为一个普通字段处理，mock vendor 从 header 中提取以区分用例。

#### 配置准备

E2E 测试在 setup 阶段动态生成配置目录，供应商配置中的 `url` 指向 MockVendor.URL：

```
{tmpDir}/config/
├── vendors/
│   └── crm_system.yaml      # url: "{{MOCK_VENDOR_URL}}/api/v3/contacts/@{payload.user_id}"
├── mappings/
│   └── crm_system/
│       └── order.paid.yaml
└── routing_rules.yaml
```

服务启动时通过 `CONFIG_DIR` 环境变量指定配置路径。

#### 用例组织

所有测试用例共享同一个服务实例，通过不同数据隔离：

```go
func TestE2E(t *testing.T) {
    t.Run("happy path: notification delivered to vendor", func(t *testing.T) {
        // POST → vendor 收到调用 → GET 返回 SUCCEEDED
    })
    t.Run("duplicate idempotent key returns existing notification", func(t *testing.T) {
        // 同一幂等键 POST 两次 → 第二次返回相同 ID → vendor 只被调用一次
    })
    t.Run("no matching vendor marks notification as FAILED", func(t *testing.T) {
        // POST 未注册的事件类型 → GET 返回 FAILED（0 供应商匹配）
    })
    t.Run("retry exhausted moves to DEAD_LETTER", func(t *testing.T) {
        // mock vendor 返回 503 → Worker 重试至 max_attempts → GET 返回 FAILED
    })
    t.Run("invalid request body returns 400", func(t *testing.T) {
        // 提交非 JSON 或缺少必填字段 → 400
    })
}
```

每条用例的完整外部验证序列：

```
① POST /api/v1/notifications → 202

② 等待 mock vendor 收到调用（超时 10s，轮询 500ms）

③ 验证 vendor 收到的请求：
   - method: PATCH（匹配供应商配置）
   - path: /api/v3/contacts/{user_id}（URL 模板已解析）
   - header Content-Type: application/json
   - header X-Test-Case-Id: {用例号}
   - body: 符合映射模板的结构和字段值

④ GET /api/v1/notifications/:id → status: SUCCEEDED
```

依赖：testcontainers（PG + RabbitMQ），在进程中启动完整系统。

---

### Iteration 2: 接收层（Ingestion）实现

**Step 1 — 编排测试**

`internal/ingestion/service_test.go` — mock DB/MQ
- 有效请求 → UPSERT + PUBLISH → 返回 ID
- 幂等键重复 → 不重复 PUBLISH
- DB 失败 → 503
- MQ 发布失败 → 日志告警仍返回 202

`internal/api/handler/ingestion_test.go` — mock Service
- 有效请求 → 202
- 缺少必填字段 → 400

`internal/db/postgres/db_test.go` — 契约测试
- UpsertNotification 创建 / 幂等返回
- GetNotification 按 ID 查询

`internal/mq/rabbitmq/mq_test.go` — 契约测试
- PublishTrigger → 队列收到消息

**Step 2 — 实现**：Handler → IngestionService → PostgresDB + RabbitMQ

---

### Iteration 3: 路由层实现

**Step 1 — 编排测试**

`internal/routing/dispatcher_test.go` — mock DB/MQ/Config
- 匹配 N 条规则 → 创建 N 个 task + N 条 MQ 消息
- 匹配 0 条 → notification FAILED
- 已处理通知 → 幂等跳过

`internal/config/loader_test.go`
- 解析 routing_rules.yaml → 获取供应商列表
- 加载 vendors 配置

**Step 2 — 实现**：RoutingDispatcher + ConfigLoader

---

### Iteration 4: 映射与投递层实现

**Step 1 — 算法测试**

`internal/mapping/engine_test.go`
- resolveString: `@{payload.field}` 取值
- resolveString: 嵌套路径 `a.b.c`
- buildBody: mapping 模式 + $source/$format
- getNestedField 边界（字段缺失、路径中途非 map）

`internal/delivery/worker/backoff_test.go`
- 指数退避结果
- 随机抖动范围
- max_delay 上限

**MQ 契约测试**（testcontainers 启动 RabbitMQ，验证 DLX+TTL 拓扑行为）

`internal/mq/rabbitmq/dlx_test.go`
- 声明完整 MQ 拓扑（DELIVERY_EX → DELIVERY_Q、DLX_EX → RETRY_EX → RETRY_Q）
- 发布投递消息到 DELIVERY_Q → 消费成功并 ACK → 消息不重现
- 发布投递消息到 DELIVERY_Q → 消费失败并 NACK（不重新入队）→ 消息进入 DLX → 经 RETRY_Q 等待后死信回 DELIVERY_Q
- RETRY_Q 中的消息携带 `expiration` → TTL 到期后自动重投到 DELIVERY_Q
- 重投后的消息可被重新消费

**Step 2 — 编排测试**

`internal/delivery/worker_test.go` — mock DB/MQ/Engine
- 成功投递 → SUCCEEDED + ACK
- HTTP 失败可重试 → retry_count + 延迟消息
- 重试超限 → 死信

**Step 3 — 实现**：MappingEngine + WorkerPool + DLX+TTL 重试

**此时 E2E 测试变 GREEN** ✅

> **补充：优雅关闭验证**  
> E2E 测试覆盖正常启动流程后，可额外补充优雅关闭测试：在 E2E TestMain 中发送 SIGTERM，验证 Worker 完成当前投递后退出，MQ 消息不丢失。此测试不单独设迭代，在 Iteration 4 完成后按需补充。

---

## 关键文件清单

| 文件 | 用途 |
|------|------|
| `docker-compose.yml` | PostgreSQL + RabbitMQ |
| `internal/model/notification.go` | Notification 领域模型 |
| `internal/model/delivery_task.go` | DeliveryTask 领域模型 |
| `internal/port/db.go` | DB 端口接口 |
| `internal/port/mq.go` | MQ 端口接口 |
| `internal/port/config.go` | 配置端口接口 |
| `internal/ingestion/service.go` | 接收层核心逻辑 |
| `internal/api/handler/ingestion.go` | HTTP handler |
| `internal/routing/dispatcher.go` | 路由分发器 |
| `internal/mapping/engine.go` | 请求拼装引擎 |
| `internal/delivery/worker/worker.go` | Worker 池 + DLX 重试 |
| `internal/db/postgres/db.go` | PostgreSQL 适配器 |
| `internal/mq/rabbitmq/mq.go` | RabbitMQ 适配器 |
| `internal/config/loader.go` | 配置加载器 |
| `test/e2e/e2e_test.go` | E2E 验收测试 |
| `cmd/notification-server/main.go` | 启动入口 |

## 验证方式

每次迭代完成后运行：
```bash
docker compose ps         # 确认外部组件
go test ./internal/... -v # 编排 + 契约 + 算法测试
go vet ./...              # 静态检查
```

E2E 测试 Iteration 1 写入后保持 RED，Iteration 4 实现结束后变 GREEN。
