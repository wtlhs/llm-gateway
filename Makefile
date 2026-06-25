APP        := llm-gateway
PKG        := github.com/company/llm-gateway
BIN        := $(APP)

.PHONY: all build run test tidy vet fmt migrate-up migrate-down sqlc docker clean help

all: build

## build: 编译单二进制
build:
	go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/gateway

## run: 本地运行(读 .env)
run: build
	./$(BIN)

## test: 跑单元测试
test:
	go test ./... -race -count=1

## vet/fmt: 静态检查与格式化
vet:
	go vet ./...
fmt:
	gofmt -s -w .

## tidy: 整理依赖
tidy:
	go mod tidy

## migrate-up: 应用迁移(goose)
migrate-up:
	goose -dir internal/db/migrations postgres "$(CONTEXT_DB_URL)" up

## migrate-down: 回滚最后一次迁移
migrate-down:
	goose -dir internal/db/migrations postgres "$(CONTEXT_DB_URL)" down

## sqlc: 由 queries/*.sql 生成类型安全代码到 gen/
sqlc:
	sqlc generate

## docker: 构建镜像
docker:
	docker build -t $(APP):latest .

## clean: 清理构建产物
clean:
	rm -f $(BIN)

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'
