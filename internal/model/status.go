package model

// Notification lifecycle statuses.
const (
	NotificationStatusPending         = "PENDING"
	NotificationStatusDelivering      = "DELIVERING"
	NotificationStatusSucceeded       = "SUCCEEDED"
	NotificationStatusFailed          = "FAILED"
	NotificationStatusPartiallyFailed = "PARTIALLY_FAILED"
)

// Delivery task lifecycle statuses.
const (
	DeliveryTaskStatusPending    = "PENDING"
	DeliveryTaskStatusDelivering = "DELIVERING"
	DeliveryTaskStatusSucceeded  = "SUCCEEDED"
	DeliveryTaskStatusDeadLetter = "DEAD_LETTER"
)

// JSON field names shared across delivery and MQ packages.
const (
	FieldDeliveryTaskID = "delivery_task_id"
	FieldVendorID       = "vendor_id"
)
