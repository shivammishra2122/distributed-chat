package storage

import (
	"distributed-chat/internal/protocol"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

type Storage struct {
	messages []protocol.Message
	capacity int
	mu       sync.Mutex
}

// NewStorage creates a new in-memory storage with a fixed capacity (Ring Buffer).
// It no longer requires a port since it doesn't create file-based logs.
func NewStorage(capacity int) *Storage {
	return &Storage{
		messages: make([]protocol.Message, 0, capacity),
		capacity: capacity,
	}
}

func (s *Storage) Save(msg protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Append new message
	s.messages = append(s.messages, msg)

	// Ring Buffer Logic: Drop oldest if over capacity
	if len(s.messages) > s.capacity {
		// Optimization: Re-slice to remove the first element
		// For very large buffers, a real Ring/Circular buffer is better (index wrapping),
		// but for chat history (e.g. 1000 items), re-slicing is perfectly fine and simple.
		s.messages = s.messages[1:]
	}

	return nil
}

func (s *Storage) Close() error {
	// No file to close
	return nil
}

// GetLastTimestamp returns the timestamp of the last message in memory
func (s *Storage) GetLastTimestamp() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.messages) == 0 {
		return time.Time{}, nil
	}
	return s.messages[len(s.messages)-1].Timestamp, nil
}

func (s *Storage) GetMessagesAfter(t time.Time) ([]protocol.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var results []protocol.Message
	// Iterate to find messages after t
	// Optimization: Could binary search since timestamps are sorted, but linear scan is fast enough for <10k items.
	for _, msg := range s.messages {
		if msg.Timestamp.After(t) {
			results = append(results, msg)
		}
	}
	return results, nil
}

func (s *Storage) GetRecentMessages(count int) ([]protocol.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	total := len(s.messages)
	if total == 0 {
		return nil, nil
	}

	if count > total {
		count = total
	}

	// Return the last 'count' messages
	return s.messages[total-count:], nil
}

// SaveSnapshot dumps the current in-memory messages to a JSON file
func (s *Storage) SaveSnapshot(filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(s.messages)
	if err != nil {
		return err
	}

	return os.WriteFile(filename, data, 0644)
}

// LoadSnapshot loads messages from a JSON file into memory
func (s *Storage) LoadSnapshot(filename string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No snapshot exists, start empty
		}
		return err
	}

	return json.Unmarshal(data, &s.messages)
}

// StartSnapshotter starts a background ticker to save snapshots periodically
func (s *Storage) StartSnapshotter(interval time.Duration, filename string) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			if err := s.SaveSnapshot(filename); err != nil {
				log.Printf("Failed to save snapshot to %s: %v", filename, err)
			}
		}
	}()
}
