package model

import "time"

type Notification struct {
	ID             string
	ShardID        int
	CallerID       string
	IdempotentKey  string
	EventType      string
	Payload        map[string]any
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UpsertParams struct {
	CallerID      string
	EventType     string
	IdempotentKey string
	Payload       map[string]any
}
