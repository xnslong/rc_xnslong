// Package rabbitmq 声明 MQ 拓扑元素。
//
// MVP 拓扑结构（DD §6.1）:
//
//	notification.trigger (direct) ──→ notification.trigger.q
//	notification.delivery (direct) ──→ notification.delivery.q
//
//	失败重试通过多延迟队列实现（DD §6.4 队列级 TTL）:
//
//	Notification.retry (direct) ──→ notification.retry.2s.queue (x-message-ttl=2000)
//	                              ──→ notification.retry.5s.queue (x-message-ttl=5000)
//	                              ──→ ...
//	各延迟队列死信回 notification.delivery exchange
//
//	无 DLX exchange（DD v0.2 已将 DLX+per-message TTL 改为队列级 TTL + 预定义时间槽）
package rabbitmq

// 交换机名称
const (
	TriggerExchange  = "notification.trigger"
	DeliveryExchange = "notification.delivery"
	RetryExchange    = "notification.retry"
)

// 队列名称
const (
	TriggerQueue  = "notification.trigger.q"
	DeliveryQueue = "notification.delivery.q"
	RetryQueue    = "notification.retry.q" // 向后兼容名称，实际使用时间槽命名
)

// RetrySlots 定义 MVP 阶段的重试时间槽（DD §6.4）。
// 每个时间槽对应一个固定 TTL 的延迟队列，Worker 按 backoff 计算结果
// 向上取整匹配最近的时间槽，选择对应 routing key 发布。
// 这是运行时配置，可按需增删。
var RetrySlots = []RetrySlot{
	{Key: "retry.2s", QueueName: "notification.retry.2s.queue", TTLMs: 2000},
	{Key: "retry.5s", QueueName: "notification.retry.5s.queue", TTLMs: 5000},
	{Key: "retry.10s", QueueName: "notification.retry.10s.queue", TTLMs: 10000},
	{Key: "retry.30s", QueueName: "notification.retry.30s.queue", TTLMs: 30000},
}

// RetrySlot 描述一个重试时间槽。
type RetrySlot struct {
	Key       string // routing key, e.g. "retry.2s"
	QueueName string // queue name, e.g. "notification.retry.2s.queue"
	TTLMs     int    // x-message-ttl in milliseconds
}

// ExchangeType 是交换机类型的别名。
type ExchangeType string

const (
	ExchangeFanout ExchangeType = "fanout"
	ExchangeDirect ExchangeType = "direct"
)

// Topology 描述完整的 MQ 拓扑结构。
type Topology struct {
	Exchanges []ExchangeDecl
	Queues    []QueueDecl
	Bindings  []BindingDecl
}

// ExchangeDecl 描述一个交换机声明。
type ExchangeDecl struct {
	Name       string
	Type       ExchangeType
	Durable    bool
	AutoDelete bool
}

// QueueDecl 描述一个队列声明。
type QueueDecl struct {
	Name       string
	Durable    bool
	AutoDelete bool
	Args       map[string]any
}

// BindingDecl 描述一个绑定关系。
type BindingDecl struct {
	Queue    string
	Exchange string
	Key      string
}

// DefaultTopology 返回 MVP 默认拓扑结构（DD v0.2）。
func DefaultTopology() Topology {
	t := Topology{
		Exchanges: []ExchangeDecl{
			{Name: TriggerExchange, Type: ExchangeDirect, Durable: true},
			{Name: DeliveryExchange, Type: ExchangeDirect, Durable: true},
			{Name: RetryExchange, Type: ExchangeDirect, Durable: true},
		},
		Queues: []QueueDecl{
			{Name: TriggerQueue, Durable: true},
			{Name: DeliveryQueue, Durable: true},
		},
		Bindings: []BindingDecl{
			{Queue: TriggerQueue, Exchange: TriggerExchange, Key: "#"},
			{Queue: DeliveryQueue, Exchange: DeliveryExchange, Key: "#"},
		},
	}

	// Add retry slot queues
	for _, slot := range RetrySlots {
		t.Queues = append(t.Queues, QueueDecl{
			Name: slot.QueueName,
			Args: map[string]any{
				"x-message-ttl":             slot.TTLMs,
				"x-dead-letter-exchange":    DeliveryExchange,
				"x-dead-letter-routing-key": "", // 使用原始 routing key（投递到 # 通配 binding）
			},
		})
		t.Bindings = append(t.Bindings, BindingDecl{
			Queue:    slot.QueueName,
			Exchange: RetryExchange,
			Key:      slot.Key,
		})
	}

	return t
}
