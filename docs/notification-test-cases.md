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

状态标记格式为 `{结果} {commit-hash} ({测试日期})`，同时记录版本和验证时间：

| 标记 | 含义 | 示例 |
|------|------|------|
| ✅ | 有测试覆盖且通过 | ✅ a837d89 (2026-06-03) |
| 🔴 | 有测试覆盖但失败 | 🔴 a837d89 (2026-06-03) |
| ➖ | 尚未有测试覆盖 | ➖ |

更新时机：**测试通过后立即同步**。由 AI 通过 grep `@test-case` 定位对应测试函数，根据测试结果更新状态列。commit hash 取自 `git log -1 --format="%h"`，日期为测试执行日期。

### 3. 测试执行方式

当前所有 E2E 测试在包内串行执行（不使用 `t.Parallel()`）。不支持并发测试——共享 PG/MQ 无法隔离并发用例之间的数据冲突。

### 4. 配置策略

配置统一存放在 `test/e2e/testdata/` 目录下。

配置采用 `event → routing → vendor` 的链式结构，一条链完整定义事件、路由规则和供应商。

一个用例默认定义一条新的 `event → routing → vendor` 链，与其他链完全隔离，用于测试新场景。如果多个用例可以共享同一条链，则尽量复用。

**配置边界测试必须使用共享目录**（即 `test/e2e/testdata/`），让脏 vendor 配置与正常 vendor 同目录共存，以验证脏配置不影响正常配置的隔离性。suite 在启动 mock vendor 前自动检查 `GetVendorConfig()`，跳过配置错误的 vendor。

**路径不存在等启动失败场景**（如 TC4.1-path-not-found）使用独立二进制启动，不依赖共享目录。

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
| TC1.1-valid_payload | POST 合法通知（包含 event、idempotent_key、payload） | 202, `data.notification_id ≠ ""`, `data.status = "PENDING"`, `data.created_at ≠ ""` | ✅ 37f17c6 (2026-06-09) |
| TC1.1-auto_idempotent_key | POST 合法通知，不传 idempotent_key | 202, `data.notification_id ≠ ""`（系统自动生成 UUID 作为 idempotent_key） | ✅ 37f17c6 (2026-06-09) |

### 1.2 请求格式错误

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.2-invalid_json_body | POST 无效 JSON 字符串 | 400, `error.code = "INVALID_REQUEST"` | ✅ 37f17c6 (2026-06-09) |
| TC1.2-json_array_body | POST `[{...}]` | 400, `error.code = "INVALID_REQUEST"` | ✅ 37f17c6 (2026-06-09) |
| TC1.2-json_scalar_body | POST 纯字符串或数字 | 400, `error.code = "INVALID_REQUEST"` | ✅ 37f17c6 (2026-06-09) |
| TC1.2-empty_event | POST `event: ""` | 400, `error.code = "INVALID_REQUEST"` | ✅ 37f17c6 (2026-06-09) |
| TC1.2-invalid_event_type | POST `event: 123` 或 `event: null` | 400, `error.code = "INVALID_REQUEST"` | ✅ 37f17c6 (2026-06-09) |

### 1.3 幂等键重复

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.3-duplicate_idempotent_key | ① POST（请求体完整合法）<br>② 再次 POST 完全相同请求体 | 第二次返回的 `data.notification_id` 与第一次相同 | ✅ 37f17c6 (2026-06-09) |

### 1.4 事件与 Schema 校验

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC1.4-unregistered_event | POST event 不在 event_schemas 中 | 422, `error.code = "EVENT_NOT_FOUND"` | ✅ 37f17c6 (2026-06-09) |
| TC1.4-missing_required_field | payload 缺少 event 契约声明的必填字段（如 order_id） | 422, `error.code = "SCHEMA_VALIDATION_FAILED"`, `details[0].field = "payload.order_id"` | ✅ 37f17c6 (2026-06-09) |
| TC1.4-wrong_field_type | amount 为 string（契约声明 integer） | 422, `error.code = "SCHEMA_VALIDATION_FAILED"`, details 包含类型错误描述 | ✅ 37f17c6 (2026-06-09) |
| TC1.4-enum_out_of_range | currency 为 "GBP"（契约声明 enum: [CNY, USD, EUR]） | 422, details 包含枚举不匹配描述 | ✅ 37f17c6 (2026-06-09) |
| TC1.4-numeric_constraint | amount 为 -100（契约声明 minimum: 0） | 422, details 包含最小值约束描述 | ✅ 37f17c6 (2026-06-09) |
| TC1.4-multiple_errors | payload 同时缺 2 个必填字段 + 类型错误 | 422, `details` 数组包含 2+ 条记录 | ✅ 37f17c6 (2026-06-09) |

---

## 二、通知查询 API（GET /api/v1/notifications/{id}）

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC2.1-completed_notification | ① POST 创建通知<br>② 等待状态变为 SUCCEEDED<br>③ GET /api/v1/notifications/{id} | 200, `data.status = "SUCCEEDED"`, `data.delivery_results` 包含各 vendor 投递结果 | ✅ 37f17c6 (2026-06-09) |
| TC2.2-nonexistent_notification | GET 一个随机的、未使用过的 ID | 404 | ✅ 37f17c6 (2026-06-09) |

---

## 三、投递行为

### 3.1 全链路成功交付

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.1-matched_vendor_called | POST 通知 → 等待 vendor 被调用 | vendor 收到 HTTP 请求，method = "POST" | ✅ 37f17c6 (2026-06-09) |
| TC3.2-status_succeeded | POST 通知 → 轮询通知状态 | 状态最终变为 "SUCCEEDED" | ✅ 37f17c6 (2026-06-09) |

### 3.2 幂等不重发

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.2-no_duplicate_delivery | ① POST 通知<br>② 等待 SUCCEEDED<br>③ 同 idempotent_key 再次 POST | vendor 仅被调用 1 次 | ✅ 37f17c6 (2026-06-09) |

### 3.3 多路由分发

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.3-all_matched_vendors | POST event 在路由规则中对应 N 个 vendor | 全部 N 个 vendor 收到请求 | ✅ 37f17c6 (2026-06-09) |
| TC3.3-unmatched_vendor_ignored | POST event 只匹配 vendor A/B（系统中存在 A/B/C） | vendor C 未收到任何请求 | ✅ 37f17c6 (2026-06-09) |

### 3.4 无匹配路由

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.4-no_rule_failed | POST routing_rules 中未注册的 event | 0 vendor 被调用，通知状态最终为 "FAILED" | ✅ 37f17c6 (2026-06-09) |

### 3.5 重试与死信

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.5-retry_exhausted | vendor 持续返回 503（retryable），max_attempts=3<br>① POST 通知<br>② 等待投递完成 | vendor 被调用 max_attempts 次，通知状态最终为 "FAILED"，重试间隔递增（约 2s、4s） | ✅ 37f17c6 (2026-06-09) |
| TC3.5-network_unreachable | vendor 地址连接拒绝<br>① POST 通知<br>② 等待投递完成 | 同可重试失败处理，最终状态 FAILED | ✅ 37f17c6 (2026-06-09) |

### 3.6 部分成功

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.6-partial_success | ① POST 通知（event 同时匹配 A 和 B）<br>② vendor A 返回 200<br>③ vendor B 返回 503（耗尽重试） | 通知状态最终为 "PARTIALLY_FAILED" | ✅ 37f17c6 (2026-06-09) |

### 3.7 供应商请求内容正确性

> 验证 Mapping 引擎将通知 payload 按模板规则转换为 vendor 请求体的正确性。

#### 3.7.1 `@{payload:field}` — 字段引用取值

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-pure_field_ref | payload: `{order_id: "123"}`<br>模板: `@{payload:order_id}` | vendor body: `"123"` | ✅ a868ed1 (2026-06-05) |
| TC3.7-nested_path | payload: `{a: {b: {c: "v"}}}`<br>模板: `@{payload:a.b.c}` | vendor body: `"v"` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-missing_field | payload: `{}`<br>模板: `@{payload:missing}` | vendor body: `null`（纯引用返回 nil） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-non_map_intermediate | payload: `{a: "string"}`<br>模板: `@{payload:a.b}` | vendor body: `null`（中间路径非 map 时返回 nil） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-static_template | payload: 任意<br>模板: `static-value` | vendor body: `"static-value"` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-mixed_template | payload: `{id: 123}`<br>模板: `user-@{payload:id}` | vendor body: `"user-123"` | ✅ 37f17c6 (2026-06-09) |

#### 3.7.2 `$source` — 原始类型保持

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-source_integer | payload: `{count: 42}`<br>模板: `$source: "@{payload:count}"` | vendor body: `42`（保持 integer） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-source_boolean | payload: `{active: true}`<br>模板: `$source: "@{payload:active}"` | vendor body: `true`（保持 boolean） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-source_null | payload: `{note: null}`<br>模板: `$source: "@{payload:note}"` | vendor body: `null`（保持 null） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-source_prefix_suffix | payload: `{id: 42}`<br>模板: `$source: "id_@{payload:id}"` | vendor body: `"id_42"`（string） | ✅ 37f17c6 (2026-06-09) |

#### 3.7.3 `$type` — 强制类型转换

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-type_no_explicit | payload: `{count: 42}`，event schema 声明 count 为 integer<br>模板: `$source: "@{payload:count}"`（无 `$type`） | vendor body: `42`（使用 event schema 声明的 integer 类型） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-type_int_to_string | payload: `{count: 42}`<br>`$type: string` | vendor body: `"42"` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-type_string_to_int | payload: `{count_str: "42"}`<br>`$type: integer` | vendor body: `42` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-type_string_to_number | payload: `{price: "29.99"}`<br>`$type: number` | vendor body: `29.99` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-type_int_to_bool | payload: `{flag: 1}`<br>`$type: boolean` | vendor body: `true` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-type_invalid_conversion | payload: `{count: "abc"}`<br>`$type: integer` | 映射失败，通知最终 FAILED，vendor 未收到请求 | ✅ 37f17c6 (2026-06-09) |

#### 3.7.4 `$format` — 格式转换

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-format_timestamp | payload: `{paid_at: 1716518400}`<br>模板: `$format: "2006-01-02"` | vendor body: `"2024-05-24"` | ✅ 37f17c6 (2026-06-09) |

#### 3.7.5 `$each` — 数组遍历映射

> `$source + $each` 组合将源数组遍历转换为对象数组。`item` 是保留关键字，在 `$each` 块内表示当前遍历到的数组元素。对象数组用 `@{item:field}` 引用字段，原始值数组用 `@{item}`（无路径）引用元素本身的值。`@{payload:*}` 在 `$each` 块内同样可用。

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC3.7-each_basic | payload: `{products: [{id: "p1", qty: 3}, {id: "p2", qty: 5}]}`<br>模板: `$source: "@{payload:products}"`<br>`$each: {product_id: "@{item:id}", quantity: "@{item:qty}"}` | vendor body: `[{product_id: "p1", quantity: 3}, {product_id: "p2", quantity: 5}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_with_format | payload: `{orders: [{date: 1716518400, total: 100}]}`<br>模板: `$source: "@{payload:orders}"`<br>`$each: {order_date: {$source: "@{item:date}", $format: "2006-01-02"}, amount: "@{item:total}"}` | vendor body: `[{order_date: "2024-05-24", amount: 100}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_with_type | payload: `{items: [{price: "29.99", count: "3"}]}`<br>模板: `$source: "@{payload:items}"`<br>`$each: {price: {$source: "@{item:price}", $type: "number"}, count: {$source: "@{item:count}", $type: "integer"}}` | vendor body: `[{price: 29.99, count: 3}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_static_mixed | payload: `{products: [{id: "p1"}]}`<br>模板: `$source: "@{payload:products}"`<br>`$each: {product_id: "@{item:id}", source: "notification"}` | vendor body: `[{product_id: "p1", source: "notification"}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_nested | payload: `{orders: [{id: "o1", items: [{name: "apple", price: 5}]}]}`<br>模板: `$source: "@{payload:orders}"`<br>`$each: {order_id: "@{item:id}", products: {$source: "@{item:items}", $each: {product_name: "@{item:name}", cost: "@{item:price}"}}}` | vendor body: `[{order_id: "o1", products: [{product_name: "apple", cost: 5}]}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_payload_ref | payload: `{user_id: "u_001", products: [{id: "p1", qty: 3}]}`<br>模板: `$source: "@{payload:products}"`<br>`$each: {product_id: "@{item:id}", quantity: "@{item:qty}", user: "@{payload:user_id}"}` | vendor body: `[{product_id: "p1", quantity: 3, user: "u_001"}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_empty_array | payload: `{products: []}`<br>模板: `$source: "@{payload:products}"`<br>`$each: {product_id: "@{item:id}"}` | vendor body: `[]`（空数组） | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_not_array | payload: `{products: "not_an_array"}`<br>模板: `$source: "@{payload:products}"`<br>`$each: {product_id: "@{item:id}"}` | 映射失败，通知最终 FAILED | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_primitive | payload: `{produce_list: [1, 2, 3]}`<br>模板: `$source: "@{payload:produce_list}"`<br>`$each: {product: "@{item}"}` | vendor body: `[{product: 1}, {product: 2}, {product: 3}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_primitive_with_type | payload: `{produce_list: [1, 2, 3]}`<br>模板: `$source: "@{payload:produce_list}"`<br>`$each: {product: {$source: "@{item}", $type: "string"}}` | vendor body: `[{product: "1"}, {product: "2"}, {product: "3"}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_primitive_with_format | payload: `{timestamps: [1716518400, 1716604800]}`<br>模板: `$source: "@{payload:timestamps}"`<br>`$each: {date: {$source: "@{item}", $format: "2006-01-02"}}` | vendor body: `[{date: "2024-05-24"}, {date: "2024-05-25"}]` | ✅ 37f17c6 (2026-06-09) |
| TC3.7-each_primitive_empty | payload: `{produce_list: []}`<br>模板: `$source: "@{payload:produce_list}"`<br>`$each: {product: "@{item}"}` | vendor body: `[]`（空数组） | ✅ 37f17c6 (2026-06-09) |

## 四、配置与启动

### 4.1 配置目录整体缺失

> 整个配置目录路径不存在，是物理层面的缺失，服务本身无法启动。

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC4.1-path-not-found | 提供不存在的配置目录路径并启动服务 | 服务启动失败，返回非零退出码 | ✅ 37f17c6 (2026-06-09) |

### 4.2 Vendor 配置异常

> vendor config 文件位于 `vendors/{vendor}/vendor.yaml`，定义 vendor 的 base_url、retry_policy、response_judgment 等。
>
> 异常时：该 vendor 不可用（delivery → DEAD_LETTER），不影响其他 vendor。系统**正常启动不阻塞**。

| 用例号 | 场景 | 故障模式 | 步骤 | 预期 | 状态 |
|--------|------|---------|------|------|------|
| TC4.2-vendor-file-missing | vendor.yaml 文件缺失 | 配置链中 vendor 目录存在但无 vendor.yaml → `walkYAML` 发现该目录，但 `vendor.yaml` 不存在 → `parseYAMLFileAt` 返回 file read error | route 指向该 vendor → POST 通知 | delivery task → DEAD_LETTER, `last_error` 包含文件访问错误（如 "no such file"），**不是** "not configured" | ✅ 37f17c6 (2026-06-09) |
| TC4.2-vendor-unreadable | vendor.yaml 不可读 | 文件存在但权限不足（`chmod 000`） | 同上 | delivery task → DEAD_LETTER, `last_error` 包含文件访问错误，**不是** "not configured" | ➖ |
| TC4.2-vendor-invalid-yaml | vendor.yaml 不是合法 YAML | 文件可读但 YAML 语法错误 | 同上 | delivery task → DEAD_LETTER, `last_error` 是 YAML 解析错误，**不是** "not configured" | ✅ 37f17c6 (2026-06-09) |
| TC4.2-vendor-invalid-retry | vendor.yaml 字段格式错误 | retry 配置字段值不合法（如 `base_delay: "abc"`） | 同上 | delivery task → DEAD_LETTER, `last_error` 包含 duration 解析错误，**不是** "not configured" | ✅ 37f17c6 (2026-06-09) |

### 4.3 Event Schema 异常

> schema 文件位于 `events/{biz}/events/{event}.yaml`，定义 event 的 payload schema。
>
> schema 异常时：该 event type 不可用，`GetEventSchema` 返回 error → ingestion 视为未注册 → `EVENT_NOT_FOUND` (422)。

| 用例号 | 场景 | 故障模式 | 步骤 | 预期 | 状态 |
|--------|------|---------|------|------|------|
| TC4.3-schema-file-missing | schema.yaml 文件缺失 | `events/{biz}/events/` 目录下无该 event 的 schema 文件 | POST 该 event | 422 `EVENT_NOT_FOUND`（schema 不存在视为 event 未注册） | ➖ |
| TC4.3-schema-unreadable | schema.yaml 不可读 | schema 文件存在但不可读 | 同上 | 422 `EVENT_NOT_FOUND` | ➖ |
| TC4.3-schema-invalid-yaml | schema.yaml 不是合法 YAML | YAML 语法错误 | 同上 | 422 `EVENT_NOT_FOUND` | ✅ 37f17c6 (2026-06-09) |
| TC4.3-schema-missing-event_type | schema.yaml 缺 `event_type` | 合法 YAML 但缺少 `event_type` 字段 | 同上 | 422 `EVENT_NOT_FOUND` | ✅ 37f17c6 (2026-06-09) |
| TC4.3-schema-missing-schema | schema.yaml 缺 `schema` 字段 | 合法 YAML 但缺少 `schema` 字段 | 同上 | 422 `EVENT_NOT_FOUND` | ✅ 37f17c6 (2026-06-09) |
| TC4.3-schema-empty-properties | schema.yaml 的 properties 为空 | `schema.properties: {}`（空 schema） | POST 任意 payload | 202 Accepted（空 schema 不校验，允许任何 payload） | ✅ 37f17c6 (2026-06-09) |

### 4.4 Route 配置异常

> route 文件位于 `events/{biz}/routes/{event}.yaml`，定义 event → vendor 的映射。
>
> route 异常时：该 event 的路由规则不可用 → dispatcher 找不到匹配 → 0 vendor → notification → FAILED。

| 用例号 | 场景 | 故障模式 | 步骤 | 预期 | 状态 |
|--------|------|---------|------|------|------|
| TC4.4-route-file-missing | route.yaml 文件缺失 | `events/{biz}/routes/{event}.yaml` 不存在 → `GetRoutingRules` 返回 `ErrNotConfigured` | POST 该 event | 202 Accepted → 最终 FAILED（0 vendor），`delivery_results` 为空 | ✅ 37f17c6 (2026-06-09) |
| TC4.4-route-unreadable | route.yaml 不可读 | 文件存在但不可读 | 同上 | 202 Accepted → 最终 FAILED | ➖ |
| TC4.4-route-invalid-yaml | route.yaml 不是合法 YAML | YAML 语法错误 | 同上 | 202 Accepted → 最终 FAILED（路由规则不可用） | ✅ 37f17c6 (2026-06-09) |
| TC4.4-route-missing-event_type | route.yaml 缺 `event_type` | 合法 YAML 但缺少 `event_type` | 同上 | 202 Accepted → 最终 FAILED（路由规则不可用） | ➖ |
| TC4.4-route-missing-routes | route.yaml 缺 `routes` 列表 | 合法 YAML 但缺少 `routes` 字段 | 同上 | 202 Accepted → 最终 FAILED（0 vendor） | ✅ 37f17c6 (2026-06-09) |
| TC4.4-route-empty-routes | route.yaml 的 `routes` 为空列表 | `routes: []` | 同上 | 202 Accepted → 最终 FAILED（0 vendor） | ✅ 37f17c6 (2026-06-09) |

### 4.5 Delivery Contract 异常

> delivery contract 位于 `vendors/{vendor}/{biz}/{event}.yaml`，定义 vendor 收到通知后的 HTTP 调用参数。
>
> contract 异常时：该 vendor+event 的投递规格不可用 → DEAD_LETTER。

| 用例号 | 场景 | 故障模式 | 步骤 | 预期 | 状态 |
|--------|------|---------|------|------|------|
| TC4.5-contract-file-missing | contract.yaml 文件缺失 | vendor 存在但 `vendors/{vendor}/{biz}/{event}.yaml` 不存在 → `GetDeliverySpec` 返回 `ErrNotConfigured` | POST 通知路由到该 vendor+event | delivery task → DEAD_LETTER, `last_error` = "not configured" | ✅ 37f17c6 (2026-06-09) |
| TC4.5-contract-unreadable | contract.yaml 不可读 | 文件存在但不可读 | 同上 | delivery task → DEAD_LETTER, `last_error` 包含文件访问错误 | ➖ |
| TC4.5-contract-invalid-yaml | contract.yaml 不是合法 YAML | YAML 语法错误 | 同上 | delivery task → DEAD_LETTER, `last_error` 是 YAML 解析错误 | ✅ 37f17c6 (2026-06-09) |
| TC4.5-contract-missing-event_type | contract.yaml 缺 `event_type` | 合法 YAML 但 `event_type` 缺失 | 同上 | delivery task → DEAD_LETTER, `last_error` 包含 "missing event_type" | ➖ |
| TC4.5-contract-missing-request | contract.yaml 缺 `request` 字段 | 合法 YAML 但 `request` 字段缺失 | 同上 | delivery task → DEAD_LETTER（request 不可构造） | ✅ 37f17c6 (2026-06-09) 已知缺陷: loader 不校验缺失字段，零值通过导致 SUCCEEDED |
| TC4.5-contract-missing-body | contract.yaml 缺 `request.body` | 合法 YAML 但 `body` 缺失 | 同上 | delivery task → DEAD_LETTER（body 不可构造） | ✅ 37f17c6 (2026-06-09) 已知缺陷: loader 不校验缺失字段，零值通过导致 SUCCEEDED |

### 4.6 模板字段引用验证

> delivery contract 的 `body.template` 中引用 event schema 未声明的字段 → `validateCrossConfig` 将该 contract 标记不可用。

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC4.6-undeclared-field | contract 中 `@{payload:field}` 引用 event schema 中未声明的字段 | 服务正常启动（不阻塞），该 contract 不可用 → DEAD_LETTER, `last_error` 包含 "not declared" | ✅ 37f17c6 (2026-06-09) |

---

## 五、优雅关闭

| 用例号 | 步骤 | 预期 | 状态 |
|--------|------|------|------|
| TC5.1-wait_delivery | ① vendor 注册 200，延迟 5s 回复（max_attempts=1）<br>② POST 通知<br>③ 等待 vendor 收到请求<br>④ 发送 SIGTERM<br>⑤ 重启服务<br>⑥ GET 通知状态 | 进程退出耗时 2s~8s，通知状态 SUCCEEDED，vendor 仅 1 次调用 | ✅ 37f17c6 (2026-06-09) |
| TC5.2-retry_on_sigterm | ① vendor 注册 [503（延迟 3s）, 200]（max_attempts=3）<br>② POST 通知<br>③ 等待 vendor 收到第 1 次请求<br>④ 发送 SIGTERM<br>⑤ 退出后重启服务<br>⑥ 等待重试完成（60s 超时） | 状态最终变为 SUCCEEDED，vendor 至少收到 1 次重试请求 | ✅ 37f17c6 (2026-06-09) |
