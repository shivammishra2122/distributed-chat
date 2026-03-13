package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// MessageType defines the type of message being sent.
// Values are explicit to avoid breaking the wire protocol when new types are added.
type MessageType int

const (
	MsgTypeChat          MessageType = 0
	MsgTypeJoin          MessageType = 1
	MsgTypeLeave         MessageType = 2
	MsgTypePeerHandshake MessageType = 3
	MsgTypeHeartbeat     MessageType = 4
	MsgTypeElection      MessageType = 5
	MsgTypeCoordinator   MessageType = 6
	MsgTypeSyncRequest   MessageType = 7
	MsgTypeLogin         MessageType = 8
	MsgTypeAuthResult    MessageType = 9
	MsgTypeImage         MessageType = 10
	MsgTypeRegister      MessageType = 11
	MsgTypeUserSync      MessageType = 12
	MsgTypeDirectMessage MessageType = 13
	MsgTypeEditMessage   MessageType = 14
	MsgTypeDeleteMessage MessageType = 15
	MsgTypeReaction      MessageType = 16
	MsgTypeTyping        MessageType = 17
	MsgTypeStatusChange  MessageType = 18
	MsgTypeChannelCreate MessageType = 19
	MsgTypeChannelJoin   MessageType = 20
	MsgTypeChannelLeave  MessageType = 21
	MsgTypeChannelList   MessageType = 22
	MsgTypeAdminAction   MessageType = 23
	MsgTypeHealthCheck   MessageType = 24
	MsgTypeHistory       MessageType = 25
	MsgTypeSearch        MessageType = 26
)

// MaxMessageSize is the maximum allowed message content size in bytes (1MB).
const MaxMessageSize = 1 * 1024 * 1024

// Message represents the data structure exchanged between clients and nodes
type Message struct {
	Type      MessageType         `json:"type"`
	ID        string              `json:"id,omitempty"`
	Sender    string              `json:"sender"`
	Content   string              `json:"content"`
	Channel   string              `json:"channel,omitempty"`
	Recipient string              `json:"recipient,omitempty"`
	ReplyTo   string              `json:"reply_to,omitempty"`
	Edited    bool                `json:"edited,omitempty"`
	Reactions map[string][]string `json:"reactions,omitempty"`
	Metadata  map[string]string   `json:"metadata,omitempty"`
	Timestamp time.Time           `json:"timestamp"`
}

// GenerateID creates a random 8-byte hex message ID.
// Falls back to timestamp-based ID if crypto/rand fails.
func GenerateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use nanosecond timestamp
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// SendMessage sends a JSON-encoded message to the writer
func SendMessage(w io.Writer, msg Message) error {
	encoder := json.NewEncoder(w)
	return encoder.Encode(msg)
}

// Decoder wraps json.Decoder to preserve buffering and enforce size limits
type Decoder struct {
	dec *json.Decoder
}

func NewDecoder(r io.Reader) *Decoder {
	// Limit reads to prevent memory exhaustion from oversized payloads
	limited := io.LimitReader(r, MaxMessageSize+4096) // buffer for JSON overhead
	return &Decoder{dec: json.NewDecoder(limited)}
}

func (d *Decoder) Decode() (*Message, error) {
	var msg Message
	if err := d.dec.Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
