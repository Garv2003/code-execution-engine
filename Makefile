# Variables
BINARY_NAME=server
BUILD_DIR=bin
CMD_DIR=./cmd/server

.PHONY: all build run playground backend worker both test clean lint redis-start redis-stop help verify

all: build

## build: Compile the binary
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@go build -o $(BUILD_DIR)/verify ./cmd/verify

## verify: Run health checks for all configured docker images
verify:
	@echo "Verifying runtimes..."
	@go run ./cmd/verify

## run: Run the server locally
run:
	@echo "Running $(BINARY_NAME)..."
	@go run $(CMD_DIR)

## playground: Start API+worker and open playground
playground:
	@./scripts/start-playground.sh

## backend: Run API server only
backend:
	@./scripts/run-backend.sh

## worker: Run worker only
worker:
	@./scripts/run-worker.sh

## both: Run API and worker in one process
both:
	@./scripts/run-both.sh

## test: Run unit tests
test:
	@echo "Running tests..."
	@go test -v -race ./...

## lint: Run static analysis / go vet
lint:
	@echo "Running go vet..."
	@go vet ./...

## clean: Remove build artifacts
clean:
	@echo "Cleaning build directory..."
	@rm -rf $(BUILD_DIR)
	@go clean

## redis-start: Start local Redis container for development
redis-start:
	@echo "Starting local Redis container..."
	@docker run --name cee-redis -p 6379:6379 -d redis:alpine || docker start cee-redis

## redis-stop: Stop and remove local Redis container
redis-stop:
	@echo "Stopping local Redis container..."
	@docker stop cee-redis || true
	@docker rm cee-redis || true

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
