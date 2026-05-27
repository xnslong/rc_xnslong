# API 通知系统 — 测试用例

> 本文件定义系统的**黑盒行为契约**。所有用例均从系统外部观察者视角出发，
> 通过 HTTP API 响应或 Mock Vendor 记录来验证。

---

## 文档规约

### 1. 用例 ID 格式

`TC{节号}-{语义slug}`，使用 kebab-case 描述场景本质，不绑定实现细节。

```
TC1.2-invalid_json_body    ✅ 好：描述场景
TC1.2-test1                ❌ 坏：无意义
```

新增用例直接在表中给新 slug，不破坏已有编号。

### 2. 状态标记

| 标记 | 含义 |
|------|------|
| ✅ | 有测试覆盖且通过 |
| 🔴 | 有测试覆盖但失败 |
| ➖ | 尚未有测试覆盖 |

更新时机：**测试通过后立即同步**。由 AI 通过 grep `@test-case` 定位对应测试函数，根据测试结果更新状态列。

### 3. 测试执行方式

当前所有 E2E 测试在包内串行执行（不使用 `t.Parallel()`）。不支持并发测试——共享 PG/MQ 无法隔离并发用例之间的数据冲突。

### 4. 配置策略

配置统一存放在 `test/e2e/testdata/` 目录下。

配置采用 `event → routing → vendor` 的链式结构，一条链完整定义事件、路由规则和供应商。

一个用例默认定义一条新的 `event → routing → vendor` 链，与其他链完全隔离，用于测试新场景。如果多个用例可以共享同一条链，则尽量复用。

如果用例涉及服务启动失败等场景（如非法配置），则需要使用完全独立的配置目录，让程序以该目录启动，避免影响其他用例的正常测试。

### 5. 新增用例流程

1. 在文档对应节插入一行，标注状态 ➖
2. 判断用例能否复用已有的 `event → routing → vendor` 链（见 §4 配置策略），能则复用，否则按 §4 的策略定义新链
3. 编写测试函数，添加 `// @test-case TC{X.Y}-{slug}` 注释
4. 测试通过后改状态为 ✅

---

## 一、Ingestion API（POST /api/v1/notifications）

### 1.1 有效提交

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.1-valid_payload | POST 合法通知（包含 event、idempotent_key、payload） | 202, `data.notification_id ≠ ""`, `data.status = "PENDING"`, `data.created_at ≠ ""` | ✅ |
| TC1.1-auto_idempotent_key | POST 合法通知，不传 idempotent_key | 202, `data.notification_id ≠ ""`（系统自动生成 UUID 作为 idempotent_key） | ✅ |

### 1.2 请求格式错误

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.2-invalid_json_body | POST 无效 JSON 字符串 | 400, `error.code = "INVALID_REQUEST"` | ✅ |
| TC1.2-json_array_body | POST `[{...}]` | 400, `error.code = "INVALID_REQUEST"` | ✅ |
| TC1.2-json_scalar_body | POST 纯字符串或数字 | 400, `error.code = "INVALID_REQUEST"` | ✅ |
| TC1.2-empty_event | POST `event: ""` | 400, `error.code = "INVALID_REQUEST"` | ✅ |
| TC1.2-invalid_event_type | POST `event: 123` 或 `event: null` | 400, `error.code = "INVALID_REQUEST"` | ✅ |

### 1.3 幂等键重复

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.3-duplicate_idempotent_key | ① POST（请求体完整合法）<br>② 再次 POST 完全相同请求体 | 第二次返回的 `data.notification_id` 与第一次相同 | ✅ |

### 1.4 事件与 Schema 校验

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.4-unregistered_event | POST event 不在 event_schemas 中 | 422, `error.code = "EVENT_NOT_FOUND"` | ✅ |
| TC1.4-missing_required_field | payload 缺少 event 契约声明的必填字段（如 order_id） | 422, `error.code = "SCHEMA_VALIDATION_FAILED"`, `details[0].field = "payload.order_id"` | ✅ |
| TC1.4-wrong_field_type | amount 为 string（契约声明 integer） | 422, `error.code = "SCHEMA_VALIDATION_FAILED"`, details 包含类型错误描述 | ✅ |
| TC1.4-enum_out_of_range | currency 为 "GBP"（契约声明 enum: [CNY, USD, EUR]） | 422, details 包含枚举不匹配描述 | ✅ |
| TC1.4-numeric_constraint | amount 为 -100（契约声明 minimum: 0） | 422, details 包含最小值约束描述 | ✅ |
| TC1.4-multiple_errors | payload 同时缺 2 个必填字段 + 类型错误 | 422, `details` 数组包含 2+ 条记录 | ✅ |

---

## 二、通知查询 API（GET /api/v1/notifications/{id}）

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC2.1-completed_notification | ① POST 创建通知<br>② 等待状态变为 SUCCEEDED<br>③ GET /api/v1/notifications/{id} | 200, `data.status = "SUCCEEDED"`, `data.delivery_results` 包含各 vendor 投递结果 | ✅ |
| TC2.2-nonexistent_notification | GET 一个随机的、未使用过的 ID | 404 | ✅ |

---

## 三、投递行为

### 3.1 全链路成功交付

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.1-matched_vendor_called | POST 通知 → 等待 vendor 被调用 | vendor 收到 HTTP 请求，method = "POST" | ✅ |
| TC3.2-status_succeeded | POST 通知 → 轮询通知状态 | 状态最终变为 "SUCCEEDED" | ✅ |

### 3.2 幂等不重发

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.2-no_duplicate_delivery | ① POST 通知<br>② 等待 SUCCEEDED<br>③ 同 idempotent_key 再次 POST | vendor 仅被调用 1 次 | ✅ |

### 3.3 多路由分发

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.3-all_matched_vendors | POST event 在路由规则中对应 N 个 vendor | 全部 N 个 vendor 收到请求 | ✅ |
| TC3.3-unmatched_vendor_ignored | POST event 只匹配 vendor A/B（系统中存在 A/B/C） | vendor C 未收到任何请求 | ✅ |

### 3.4 无匹配路由

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.4-no_rule_failed | POST routing_rules 中未注册的 event | 0 vendor 被调用，通知状态最终为 "FAILED" | ✅ |

### 3.5 重试与死信

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.5-retry_exhausted | vendor 持续返回 503（retryable），max_attempts=3<br>① POST 通知<br>② 等待投递完成 | vendor 被调用 max_attempts 次，通知状态最终为 "FAILED"，重试间隔递增（约 2s、4s） | ✅ |
| TC3.5-network_unreachable | vendor 地址连接拒绝<br>① POST 通知<br>② 等待投递完成 | 同可重试失败处理，最终状态 FAILED | ✅ |

### 3.6 部分成功

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.6-partial_success | ① POST 通知（event 同时匹配 A 和 B）<br>② vendor A 返回 200<br>③ vendor B 返回 503（耗尽重试） | 通知状态最终为 "PARTIALLY_FAILED" | ✅ |

### 3.7 供应商请求内容正确性

> 验证 Mapping 引擎将通知 payload 按模板规则转换为 vendor 请求体的正确性。

#### 3.7.1 `@{payload.field}` — 字段引用取值

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-pure_field_ref | payload: `{order_id: "123"}`<br>模板: `@{payload.order_id}` | vendor body: `"123"` | ✅ |
| TC3.7-nested_path | payload: `{a: {b: {c: "v"}}}`<br>模板: `@{payload.a.b.c}` | vendor body: `"v"` | ✅ |
| TC3.7-missing_field | payload: `{}`<br>模板: `@{payload.missing}` | vendor body: `""`（空字符串） | ✅ |
| TC3.7-non_map_intermediate | payload: `{a: "string"}`<br>模板: `@{payload.a.b}` | vendor body: `""`（中间路径非 map 时返回空） | ✅ |
| TC3.7-static_template | payload: 任意<br>模板: `static-value` | vendor body: `"static-value"` | ✅ |
| TC3.7-mixed_template | payload: `{id: 123}`<br>模板: `user-@{payload.id}` | vendor body: `"user-123"` | ✅ |

#### 3.7.2 `$source` — 原始类型保持

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-source_integer | payload: `{count: 42}`<br>模板: `$source: "@{payload.count}"` | vendor body: `42`（保持 integer） | ✅ |
| TC3.7-source_boolean | payload: `{active: true}`<br>模板: `$source: "@{payload.active}"` | vendor body: `true`（保持 boolean） | ✅ |
| TC3.7-source_null | payload: `{note: null}`<br>模板: `$source: "@{payload.note}"` | vendor body: `null`（保持 null） | ✅ |
| TC3.7-source_prefix_suffix | payload: `{id: 42}`<br>模板: `$source: "id_@{payload.id}"` | vendor body: `"id_42"`（string） | ✅ |

#### 3.7.3 `$type` — 强制类型转换

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-type_no_explicit | payload: `{count: 42}`，event schema 声明 count 为 integer<br>模板: `$source: "@{payload.count}"`（无 `$type`） | vendor body: `42`（使用 event schema 声明的 integer 类型） | ✅ |
| TC3.7-type_int_to_string | payload: `{count: 42}`<br>`$type: string` | vendor body: `"42"` | ✅ |
| TC3.7-type_string_to_int | payload: `{count_str: "42"}`<br>`$type: integer` | vendor body: `42` | ✅ |
| TC3.7-type_string_to_number | payload: `{price: "29.99"}`<br>`$type: number` | vendor body: `29.99` | ✅ |
| TC3.7-type_int_to_bool | payload: `{flag: 1}`<br>`$type: boolean` | vendor body: `true` | ✅ |
| TC3.7-type_invalid_conversion | payload: `{count: "abc"}`<br>`$type: integer` | 映射失败，通知最终 FAILED，vendor 未收到请求 | ✅ |

#### 3.7.4 `$format` — 格式转换

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-format_timestamp | payload: `{paid_at: 1716518400}`<br>模板: `$format: "2006-01-02"` | vendor body: `"2024-05-24"` | ✅ |

---

## 四、配置与启动

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC4.1-invalid_config | 提供格式错误的 YAML 配置文件作为配置目录并启动服务 | 服务启动失败，返回非零退出码 | ✅ |

---

## 五、优雅关闭

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC5.1-wait_delivery | ① vendor 注册 200，延迟 5s 回复（max_attempts=1）<br>② POST 通知<br>③ 等待 vendor 收到请求<br>④ 发送 SIGTERM<br>⑤ 重启服务<br>⑥ GET 通知状态 | 进程退出耗时 2s~8s，通知状态 SUCCEEDED，vendor 仅 1 次调用 | ✅ |
| TC5.2-retry_on_sigterm | ① vendor 注册 [503（延迟 3s）, 200]（max_attempts=3）<br>② POST 通知<br>③ 等待 vendor 收到第 1 次请求<br>④ 发送 SIGTERM<br>⑤ 退出后重启服务<br>⑥ 等待重试完成（60s 超时） | 状态最终变为 SUCCEEDED，vendor 至少收到 1 次重试请求 | ✅ |
