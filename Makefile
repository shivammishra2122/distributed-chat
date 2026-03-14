.PHONY: all build build-tui run-server run-client clean

APP_NAME=distributed-chat
SERVER_BIN=./bin/server
CLIENT_BIN=./bin/client
TUI_BIN=./bin/meshchat

all: build

build:
	@echo "Building..."
	go build -o $(SERVER_BIN) ./cmd/server
	go build -o $(CLIENT_BIN) ./cmd/client
	go build -o $(TUI_BIN) ./cmd/tui

build-tui:
	@echo "Building TUI..."
	go build -o $(TUI_BIN) ./cmd/tui

run-server: build
	$(SERVER_BIN) -port 8080

run-client: build
	$(CLIENT_BIN)

run-tui: build-tui
	$(TUI_BIN)

clean:
	rm -rf bin

