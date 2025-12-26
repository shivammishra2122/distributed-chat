package storage

import (
	"bufio"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

type Storage struct {
	messages []protocol.Message
	capacity int
	mu       sync.Mutex
	aofFile  *os.File
	encoder  *json.Encoder
}

// NewStorage creates a new in-memory storage backed by an Append-Only File.
func NewStorage(capacity int, dataDir string, port int) *Storage {
	filename := fmt.Sprintf("%s/node-%d.aof", dataDir, port)

	// Open AOF file for appending, create if not exists
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open AOF file %s: %v", filename, err)
	}

	return &Storage{
		messages: make([]protocol.Message, 0, capacity),
		capacity: capacity,
		aofFile:  file,
		encoder:  json.NewEncoder(file),
	}
}

func (s *Storage) Save(msg protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Append to Memory
	s.messages = append(s.messages, msg)
	if len(s.messages) > s.capacity {
		s.messages = s.messages[1:]
	}

	// 2. Append to Disk (AOF)
	if err := s.encoder.Encode(msg); err != nil {
		return fmt.Errorf("failed to write to AOF: %w", err)
	}

	return nil
}

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aofFile != nil {
		return s.aofFile.Close()
	}
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
	return s.messages[total-count:], nil
}

// LoadAOF reads the Append-Only File line by line to rebuild memory state
func (s *Storage) LoadAOF(dataDir string, port int) error {
	filename := fmt.Sprintf("%s/node-%d.aof", dataDir, port)

	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // New node, no history
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var loadedMessages []protocol.Message

	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			log.Printf("Warning: Corrupt line in AOF: %v", err)
			continue
		}
		loadedMessages = append(loadedMessages, msg)
	}

	s.mu.Lock()
	// If loaded history is larger than capacity, only take the tail
	if len(loadedMessages) > s.capacity {
		s.messages = loadedMessages[len(loadedMessages)-s.capacity:]
	} else {
		s.messages = loadedMessages
	}
	s.mu.Unlock()

	log.Printf("Restored %d messages from AOF", len(s.messages))
	return scanner.Err()
}
