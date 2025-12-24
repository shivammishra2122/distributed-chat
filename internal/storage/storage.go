package storage

import (
	"bufio"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type Storage struct {
	filename    string
	mu          sync.Mutex
	file        *os.File
	writer      *bufio.Writer
	flushTicker *time.Ticker
	stopFlush   chan struct{}
}

func NewStorage(nodePort int) (*Storage, error) {
	filename := fmt.Sprintf("node-%d.log", nodePort)
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	s := &Storage{
		filename:    filename,
		file:        file,
		writer:      bufio.NewWriterSize(file, 4096), // 4KB Buffer
		flushTicker: time.NewTicker(1 * time.Second), // Flush every second
		stopFlush:   make(chan struct{}),
	}

	// Start background flusher
	go s.runFlusher()

	return s, nil
}

func (s *Storage) runFlusher() {
	for {
		select {
		case <-s.flushTicker.C:
			s.mu.Lock()
			s.writer.Flush()
			s.mu.Unlock()
		case <-s.stopFlush:
			s.flushTicker.Stop()
			return
		}
	}
}

func (s *Storage) Save(msg protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if _, err := s.writer.Write(data); err != nil {
		return err
	}
	if _, err := s.writer.WriteString("\n"); err != nil {
		return err
	}
	// Note: We rely on ticker or explicit Flush for persistence now
	return nil
}

func (s *Storage) Close() error {
	close(s.stopFlush)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writer.Flush()
	return s.file.Close()
}

func (s *Storage) GetLastTimestamp() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Naive implementation: Read the whole file to find the last valid message
	// In production, we'd maintain an index or read from end.
	// Since we are creating a new reader, we need the filename, not the file handle (which is open for writing)

	// Re-do with os.Open
	f, err := os.Open(s.filename)
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	var msg protocol.Message
	var lastTime time.Time

	for {
		if err := decoder.Decode(&msg); err != nil {
			break
		}
		if msg.Timestamp.After(lastTime) {
			lastTime = msg.Timestamp
		}
	}
	return lastTime, nil
}

func (s *Storage) GetMessagesAfter(t time.Time) ([]protocol.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.filename)
	if err != nil {
		return nil, nil // No file
	}
	defer f.Close()

	var messages []protocol.Message
	decoder := json.NewDecoder(f)
	var msg protocol.Message

	for {
		if err := decoder.Decode(&msg); err != nil {
			break
		}
		if msg.Timestamp.After(t) {
			messages = append(messages, msg)
		}
	}
	return messages, nil
}

func (s *Storage) GetRecentMessages(count int) ([]protocol.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.filename)
	if err != nil {
		return nil, nil // No file
	}
	defer f.Close()

	var messages []protocol.Message
	decoder := json.NewDecoder(f)
	var msg protocol.Message

	// Read all (naive but simple for now)
	for {
		if err := decoder.Decode(&msg); err != nil {
			break
		}
		messages = append(messages, msg)
	}

	total := len(messages)
	if total == 0 {
		return nil, nil // Return empty list, not nil error
	}

	if count > total {
		count = total
	}

	return messages[total-count:], nil
}
