package protocol

import (
	"encoding/json"
	"io"
	"time"
)

// MessageType defines the type of message being sent
type MessageType int

const (
	MsgTypeChat MessageType = iota
	MsgTypeJoin
	MsgTypeLeave
	MsgTypePeerHandshake
	MsgTypeHeartbeat
	MsgTypeElection
	MsgTypeCoordinator
	MsgTypeSyncRequest
	MsgTypeLogin
	MsgTypeAuthResult
	MsgTypeImage
	MsgTypeRegister // New: Registration
	MsgTypeUserSync // New: Exchange active user lists
)

// Message represents the data structure exchanged between clients and nodes
type Message struct {
	Type      MessageType `json:"type"`
	Sender    string      `json:"sender"`
	Content   string      `json:"content"`
	Timestamp time.Time   `json:"timestamp"`
}

// SendMessage sends a JSON-encoded message to the writer
func SendMessage(w io.Writer, msg Message) error {
	encoder := json.NewEncoder(w)
	return encoder.Encode(msg)
}

// Decoder wraps json.Decoder to preserve buffering
type Decoder struct {
	dec *json.Decoder
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: json.NewDecoder(r)}
}

func (d *Decoder) Decode() (*Message, error) {
	var msg Message
	if err := d.dec.Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// Deprecated: Use NewDecoder instead to avoid buffer loss
func ReadMessage(r io.Reader) (*Message, error) {
	var msg Message
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
