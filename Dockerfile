# ===== 构建阶段 =====
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o /llm-gateway ./cmd/gateway

# ===== 运行阶段 =====
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /llm-gateway /llm-gateway
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

EXPOSE 8080
ENTRYPOINT ["/llm-gateway"]
