package storage

import (
	"bufio"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type Storage struct {
	messages map[string][]protocol.Message // channel -> messages
	allMsgs  []protocol.Message           // flat list for global search
	capacity int
	mu       sync.RWMutex
	aofFile  *os.File
	encoder  *json.Encoder
}

// NewStorage creates a new in-memory storage backed by an Append-Only File.
func NewStorage(capacity int, dataDir string, port int) *Storage {
	filename := fmt.Sprintf("%s/node-%d.aof", dataDir, port)

	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open AOF file %s: %v", filename, err)
	}

	return &Storage{
		messages: make(map[string][]protocol.Message),
		allMsgs:  make([]protocol.Message, 0, capacity),
		capacity: capacity,
		aofFile:  file,
		encoder:  json.NewEncoder(file),
	}
}

func (s *Storage) normalizeChannel(ch string) string {
	if ch == "" {
		return "general"
	}
	return ch
}

func (s *Storage) Save(msg protocol.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := s.normalizeChannel(msg.Channel)
	msg.Channel = ch

	if msg.ID == "" {
		msg.ID = protocol.GenerateID()
	}

	// Enforce max content size
	if len(msg.Content) > protocol.MaxMessageSize {
		return fmt.Errorf("message content exceeds maximum size of %d bytes", protocol.MaxMessageSize)
	}

	// 1. Append to channel index
	s.messages[ch] = append(s.messages[ch], msg)
	if len(s.messages[ch]) > s.capacity {
		s.messages[ch] = s.messages[ch][1:]
	}

	// 2. Append to flat list
	s.allMsgs = append(s.allMsgs, msg)
	if len(s.allMsgs) > s.capacity {
		s.allMsgs = s.allMsgs[1:]
	}

	// 3. Append to Disk (AOF)
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
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.allMsgs) == 0 {
		return time.Time{}, nil
	}
	return s.allMsgs[len(s.allMsgs)-1].Timestamp, nil
}

func (s *Storage) GetMessagesAfter(t time.Time) ([]protocol.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []protocol.Message
	for _, msg := range s.allMsgs {
		if msg.Timestamp.After(t) {
			results = append(results, msg)
		}
	}
	return results, nil
}

// GetRecentMessages returns the last `count` messages across all channels.
func (s *Storage) GetRecentMessages(count int) ([]protocol.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.allMsgs)
	if total == 0 {
		return nil, nil
	}

	if count > total {
		count = total
	}
	// Return a copy to prevent data races
	result := make([]protocol.Message, count)
	copy(result, s.allMsgs[total-count:])
	return result, nil
}

// GetChannelMessages returns paginated messages for a given channel.
func (s *Storage) GetChannelMessages(channel string, limit, offset int) ([]protocol.Message, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ch := s.normalizeChannel(channel)
	msgs := s.messages[ch]
	total := len(msgs)
	if total == 0 {
		return nil, 0
	}

	start := total - offset - limit
	end := total - offset
	if start < 0 {
		start = 0
	}
	if end < 0 {
		return nil, total
	}
	if end > total {
		end = total
	}
	// Return copy
	result := make([]protocol.Message, end-start)
	copy(result, msgs[start:end])
	return result, total
}

// SearchMessages returns messages whose Content contains the query string.
func (s *Storage) SearchMessages(query string) []protocol.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	var results []protocol.Message
	for _, msg := range s.allMsgs {
		if strings.Contains(strings.ToLower(msg.Content), query) {
			results = append(results, msg)
		}
	}
	if len(results) > 100 {
		results = results[len(results)-100:]
	}
	return results
}

// GetMessageByID finds a single message by its ID.
func (s *Storage) GetMessageByID(id string) *protocol.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := len(s.allMsgs) - 1; i >= 0; i-- {
		if s.allMsgs[i].ID == id {
			msg := s.allMsgs[i]
			return &msg
		}
	}
	return nil
}

// UpdateMessage modifies the content of a message by ID and marks it as edited.
func (s *Storage) UpdateMessage(id, newContent string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.allMsgs {
		if s.allMsgs[i].ID == id {
			s.allMsgs[i].Content = newContent
			s.allMsgs[i].Edited = true
			ch := s.normalizeChannel(s.allMsgs[i].Channel)
			for j := range s.messages[ch] {
				if s.messages[ch][j].ID == id {
					s.messages[ch][j].Content = newContent
					s.messages[ch][j].Edited = true
					break
				}
			}
			return true
		}
	}
	return false
}

// DeleteMessage removes a message by ID from memory. Returns true if found.
// Uses order-preserving deletion to maintain chronological ordering.
func (s *Storage) DeleteMessage(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	// Remove from flat list (order-preserving)
	for i := range s.allMsgs {
		if s.allMsgs[i].ID == id {
			ch := s.normalizeChannel(s.allMsgs[i].Channel)
			s.allMsgs = append(s.allMsgs[:i], s.allMsgs[i+1:]...)
			// Remove from channel index (order-preserving)
			for j := range s.messages[ch] {
				if s.messages[ch][j].ID == id {
					s.messages[ch] = append(s.messages[ch][:j], s.messages[ch][j+1:]...)
					break
				}
			}
			found = true
			break
		}
	}
	return found
}

// AddReaction adds a reaction emoji from a user to a message.
func (s *Storage) AddReaction(id, emoji, user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.allMsgs {
		if s.allMsgs[i].ID == id {
			if s.allMsgs[i].Reactions == nil {
				s.allMsgs[i].Reactions = make(map[string][]string)
			}
			// Prevent duplicate reactions from same user
			for _, existing := range s.allMsgs[i].Reactions[emoji] {
				if existing == user {
					return true // Already reacted
				}
			}
			s.allMsgs[i].Reactions[emoji] = append(s.allMsgs[i].Reactions[emoji], user)
			// Also update channel index
			ch := s.normalizeChannel(s.allMsgs[i].Channel)
			for j := range s.messages[ch] {
				if s.messages[ch][j].ID == id {
					if s.messages[ch][j].Reactions == nil {
						s.messages[ch][j].Reactions = make(map[string][]string)
					}
					s.messages[ch][j].Reactions[emoji] = s.allMsgs[i].Reactions[emoji]
					break
				}
			}
			return true
		}
	}
	return false
}

// CompactAOF rewrites the AOF file with only current in-memory messages.
// This should be called periodically to prevent unbounded disk growth.
func (s *Storage) CompactAOF(dataDir string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filename := fmt.Sprintf("%s/node-%d.aof", dataDir, port)
	tmpFile := filename + ".compact"

	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create compact file: %w", err)
	}

	encoder := json.NewEncoder(f)
	for _, msg := range s.allMsgs {
		if err := encoder.Encode(msg); err != nil {
			f.Close()
			os.Remove(tmpFile)
			return fmt.Errorf("failed to write during compaction: %w", err)
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpFile)
		return err
	}
	f.Close()

	// Close current AOF and replace
	if s.aofFile != nil {
		s.aofFile.Close()
	}

	if err := os.Rename(tmpFile, filename); err != nil {
		return fmt.Errorf("failed to rename compacted AOF: %w", err)
	}

	// Reopen for appending
	newFile, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to reopen AOF: %w", err)
	}
	s.aofFile = newFile
	s.encoder = json.NewEncoder(newFile)

	log.Printf("AOF compacted: %d messages written", len(s.allMsgs))
	return nil
}

// LoadAOF reads the Append-Only File line by line to rebuild memory state
func (s *Storage) LoadAOF(dataDir string, port int) error {
	filename := fmt.Sprintf("%s/node-%d.aof", dataDir, port)

	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Increase scanner buffer for large messages
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize+4096)
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
	if len(loadedMessages) > s.capacity {
		loadedMessages = loadedMessages[len(loadedMessages)-s.capacity:]
	}
	s.allMsgs = loadedMessages
	s.messages = make(map[string][]protocol.Message)
	for _, msg := range loadedMessages {
		ch := s.normalizeChannel(msg.Channel)
		s.messages[ch] = append(s.messages[ch], msg)
	}
	s.mu.Unlock()

	log.Printf("Restored %d messages from AOF", len(s.allMsgs))
	return scanner.Err()
}
