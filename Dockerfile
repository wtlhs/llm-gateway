# ===== 构建阶段 =====
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 静态编译(无 CGO), 适配 alpine/distroless
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w -X main.version=docker" \
    -o /llm-gateway ./cmd/gateway

# ===== 运行阶段(alpine, 带 shell 便于排查; 生产可换 distroless) =====
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app

COPY --from=builder /llm-gateway /usr/local/bin/llm-gateway
COPY --from=builder /src/internal/db/migrations /app/migrations

USER app
WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/llm-gateway"]
