APP_NAME=alert-bot
BIN_DIR=bin

.PHONY: run build lint tidy deps clean

run:
	go run ./cmd/bot

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(APP_NAME) ./cmd/bot

lint:
	go vet ./...
	go fmt ./...

tidy:
	go mod tidy

deps:
	go get github.com/joho/godotenv
	go get github.com/sirupsen/logrus
	go get github.com/go-telegram-bot-api/telegram-bot-api/v5
	go get github.com/google/uuid
clean:
	rm -rf $(BIN_DIR)


