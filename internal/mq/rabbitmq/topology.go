// Package rabbitmq 声明 MQ 拓扑元素。
//
// 拓扑结构:
//
//	notification.trigger (fanout) ──→ notification.trigger.q
//	notification.delivery (direct) ──→ notification.delivery.q
//	                                         │
//	                                    x-dead-letter-exchange = notification.dlx
//	                                         ↓
//	notification.dlx (fanout) ──→ notification.retry.q
//	                                    │
//	                               x-dead-letter-exchange = notification.delivery
//	                               (per-message TTL via expiration header)
//
// TODO(Iteration 4): 实现 DeclareAll() 集中声明全部拓扑
package rabbitmq

// 交换机名称
const (
	TriggerExchange  = "notification.trigger"
	DeliveryExchange = "notification.delivery"
	DLXExchange      = "notification.dlx"
)

// 队列名称
const (
	TriggerQueue  = "notification.trigger.q"
	DeliveryQueue = "notification.delivery.q"
	RetryQueue    = "notification.retry.q"
)

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

// DefaultTopology 返回 MVP 默认拓扑结构。
func DefaultTopology() Topology {
	return Topology{
		Exchanges: []ExchangeDecl{
			{Name: TriggerExchange, Type: ExchangeFanout, Durable: true},
			{Name: DeliveryExchange, Type: ExchangeDirect, Durable: true},
			{Name: DLXExchange, Type: ExchangeFanout, Durable: true},
		},
		Queues: []QueueDecl{
			{Name: TriggerQueue, Durable: true},
			{Name: DeliveryQueue, Durable: true, Args: map[string]any{"x-dead-letter-exchange": DLXExchange}},
			{Name: RetryQueue, Durable: true, Args: map[string]any{"x-dead-letter-exchange": DeliveryExchange}},
		},
		Bindings: []BindingDecl{
			{Queue: TriggerQueue, Exchange: TriggerExchange, Key: "#"},
			{Queue: DeliveryQueue, Exchange: DeliveryExchange, Key: "#"},
			{Queue: RetryQueue, Exchange: DLXExchange, Key: "#"},
		},
	}
}

// TODO(Iteration 4): 实现 DeclareAll(ch *amqp.Channel) error — 遍历 DefaultTopology() 执行声明
