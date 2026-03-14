package tui

import "distributed-chat/internal/protocol"

// BubbleTea message types for the event loop

// ServerMsg wraps a protocol message received from the server
type ServerMsg struct {
	Msg protocol.Message
}

// ConnectedMsg signals successful connection and auth
type ConnectedMsg struct {
	Username string
}

// ErrorMsg signals a connection or protocol error
type ErrorMsg struct {
	Err error
}

// TickMsg is used for periodic updates (typing indicators, etc)
type TickMsg struct{}
