package model

import "time"

type DeliveryTask struct {
	ID             string
	ShardID        int
	NotificationID string
	EventType      string
	VendorID       string
	Status         string
	RetryCount     int
	MaxRetries     int
	NextRetryAt    *time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
