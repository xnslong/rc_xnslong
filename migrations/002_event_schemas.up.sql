CREATE TABLE event_schemas (
    id              SERIAL          PRIMARY KEY,
    event_type      VARCHAR(128)    NOT NULL,
    version         INT             NOT NULL DEFAULT 1,
    schema_def      JSONB           NOT NULL,
    description     TEXT            NOT NULL DEFAULT '',
    status          VARCHAR(16)     NOT NULL DEFAULT 'ACTIVE',
    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    CONSTRAINT uq_event_schemas_type_version UNIQUE (event_type, version)
);

COMMENT ON TABLE event_schemas IS '事件类型及其 JSON Schema 定义';
COMMENT ON COLUMN event_schemas.schema_def IS 'JSON Schema (Draft-07) 定义';
