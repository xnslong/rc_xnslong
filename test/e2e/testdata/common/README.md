# common — shared config for default e2e tests

This is a self-contained config root used as `--config-dir` by the notification-server (`SetupSuite()` in `suite.go`).

## Structure (DD §4.1)

```
events/{biz}/                    # event definitions, grouped by business domain
├── route.yaml                   #   routing rules (event_type → vendor_id)
└── events/{event}.yaml          #   event schema (JSON Schema Draft-07)
vendors/{vendor}/                # vendor configurations
├── vendor.yaml                  #   base config (method, url, headers, retry, judgment)
└── {biz}/{event}.yaml           #   delivery contract (body.template + optional field overrides)
```

## Rules

- **Event schemas** are the authoritative data contract. The data producer MUST declare every payload field the event carries — type, nested structure, and constraints — regardless of which vendors consume it. Vendors discover available fields from the schema alone.
- **Delivery contracts** define how the unified payload maps to a vendor's API format. `body.template` MUST only reference fields declared in the event schema (`@{payload:...}`). References to undeclared fields are invalid — the schema is the single source of truth, not runtime payload inspection.
- **Route files** (`events/{biz}/route.yaml`) live alongside their event schemas, keeping routing decisions near the events they govern.
