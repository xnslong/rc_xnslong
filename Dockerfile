FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /app/notification-server ./cmd/notification-server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/notification-server /app/notification-server

EXPOSE 8080
ENTRYPOINT ["/app/notification-server"]
