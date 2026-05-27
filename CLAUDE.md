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

# development guide

Use Outside-In TDD in development flow on development activity. 
follow the following workflow. Please refer to the [development-guide](/development-guide.md) for more details.
