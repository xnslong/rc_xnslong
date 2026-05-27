CREATE TABLE notifications (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    shard_id        INT             NOT NULL DEFAULT 0,
    caller_id       VARCHAR(64)     NOT NULL,
    idempotent_key  VARCHAR(128)    NOT NULL,
    event_type      VARCHAR(128)    NOT NULL,
    payload         JSONB           NOT NULL,
    status          VARCHAR(20)     NOT NULL DEFAULT 'PENDING',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_notifications_caller_idempotent UNIQUE (caller_id, idempotent_key),
    CONSTRAINT chk_notifications_status CHECK (status IN (
        'PENDING', 'DELIVERING', 'SUCCEEDED', 'PARTIALLY_FAILED', 'FAILED'
    ))
);

CREATE TABLE delivery_tasks (
    id                UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    shard_id          INT             NOT NULL DEFAULT 0,
    notification_id   UUID            NOT NULL,
    vendor_id         VARCHAR(64)     NOT NULL,
    event_type        VARCHAR(128)    NOT NULL,
    status            VARCHAR(20)     NOT NULL DEFAULT 'PENDING',
    retry_count       INT             NOT NULL DEFAULT 0,
    max_retries       INT             NOT NULL DEFAULT 5,
    next_retry_at     TIMESTAMPTZ,
    last_error        TEXT,
    created_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_delivery_tasks_status CHECK (status IN (
        'PENDING', 'DELIVERING', 'SUCCEEDED', 'FAILED', 'DEAD_LETTER'
    )),
    CONSTRAINT fk_delivery_tasks_notification FOREIGN KEY (notification_id)
        REFERENCES notifications (id)
);

CREATE INDEX idx_delivery_tasks_notification_id
    ON delivery_tasks (notification_id);
