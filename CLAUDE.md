# CLAUDE.md 

this is a notification system to push messages for critical events to external system vendors.

# directories

```
.
├── cmd/notification-server    # main entry point
├── config/                    # runtime config (vendors, mappings, schemas, routing)
├── doc/                       # design documents
├── internal/
│   ├── api/                   # HTTP API handlers
│   ├── config/                # config loader
│   ├── db/                    # database layer (PostgreSQL)
│   ├── delivery/              # delivery worker (HTTP calls, retry, dead letter)
│   ├── ingestion/             # notification ingestion
│   ├── logger/                # structured logging
│   ├── mapping/               # payload → vendor body mapping engine
│   ├── model/                 # domain models
│   ├── mq/                    # message queue (RabbitMQ)
│   ├── port/                  # port interfaces
│   └── routing/               # event routing and dispatcher
├── migrations/                # DB schema migrations
├── output/                    # build output (binary)
└── test/
    └── e2e/                   # end-to-end tests
```


# commands

```bash
go build .                       # build the CLI binary
go vet ./...                     # what CI runs
go test ./...                    # full test suite (CI-equivalent)
go test -race ./...              # race detector

# Cross-build matrix (CI runs this on every push):
GOOS=windows go build ./...
GOOS=darwin  go build ./...
GOOS=linux   go build ./...
```

# restriction

* Requires go 1.19+
* assertion with github.com/stretchr/testify in testing cases.
* use RabbitMQ, PostgreSQL
* run go-vet command before each commission.
* draw diagrams with mermaid.

# conventions

Config directory structure follows DD §4.1:

```
config/
├── events/{biz}/                    # event definitions, grouped by business domain
│   ├── route.yaml                   #   routing rules (event_type → vendor_id)
│   └── events/{event}.yaml          #   event schema (JSON Schema Draft-07)
└── vendors/{vendor}/                # vendor configurations
    ├── vendor.yaml                  #   base config (method, url, headers, retry, judgment)
    └── {biz}/{event}.yaml           #   delivery contract (body.template + optional field overrides)
```

- **Event schemas** are the authoritative data contract. The data producer MUST declare every payload field the event carries — type, nested structure, and constraints — regardless of which vendors consume it. Vendors discover available fields from the schema alone.
- **Delivery contracts** define how the unified payload maps to a vendor's API format. `body.template` MUST only reference fields declared in the event schema (`@{payload:...}`). References to undeclared fields are invalid — the schema is the single source of truth, not runtime payload inspection.
- **Route files** live alongside their event schemas under `events/{biz}/route.yaml`, keeping routing decisions near the events they govern.

# development guide

Use Outside-In TDD in development flow on development activity. 
follow the following workflow. Please refer to the [development-guide](/development-guide.md) for more details.
