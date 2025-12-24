.PHONY: all build run-server run-client clean

APP_NAME=distributed-chat
SERVER_BIN=./bin/server
CLIENT_BIN=./bin/client

all: build

build:
	@echo "Building..."
	go build -o $(SERVER_BIN) ./cmd/server
	go build -o $(CLIENT_BIN) ./cmd/client

run-server: build
	$(SERVER_BIN) -port 8080

run-client: build
	$(CLIENT_BIN)

clean:
	rm -rf bin
