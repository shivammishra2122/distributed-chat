package channel

import (
	"distributed-chat/internal/fileutil"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"
	"time"
)

// Role constants
const (
	RoleAdmin     = "admin"
	RoleModerator = "moderator"
	RoleMember    = "member"
)

// Channel name validation: 2-32 chars, lowercase alphanumeric + underscore/dash
var validChannelName = regexp.MustCompile(`^[a-z0-9_-]{2,32}$`)

// ValidateChannelName checks if a channel name is valid
func ValidateChannelName(name string) error {
	if !validChannelName.MatchString(name) {
		return fmt.Errorf("channel name must be 2-32 lowercase characters, alphanumeric/underscore/dash only")
	}
	return nil
}

// Channel represents a chat room
type Channel struct {
	Name        string            `json:"name"`
	Topic       string            `json:"topic,omitempty"`
	Description string            `json:"description,omitempty"`
	IsPrivate   bool              `json:"is_private"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   time.Time         `json:"created_at"`
	Members     map[string]string `json:"members"` // username -> role
}

// Manager manages all channels
type Manager struct {
	mu       sync.RWMutex
	channels map[string]*Channel
	file     string
}

// NewManager creates and loads a channel manager
func NewManager() *Manager {
	m := &Manager{
		channels: make(map[string]*Channel),
		file:     "channels.json",
	}

	if err := m.load(); err != nil {
		// Create default "general" channel
		m.channels["general"] = &Channel{
			Name:      "general",
			Topic:     "General discussion",
			CreatedBy: "system",
			CreatedAt: time.Now(),
			Members:   make(map[string]string),
		}
		m.save()
	}
	return m
}

// Create creates a new channel. Returns error string if failed.
func (m *Manager) Create(name, createdBy string, isPrivate bool) bool {
	if err := ValidateChannelName(name); err != nil {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.channels[name]; exists {
		return false
	}

	m.channels[name] = &Channel{
		Name:      name,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		IsPrivate: isPrivate,
		Members: map[string]string{
			createdBy: RoleAdmin,
		},
	}
	m.save()
	return true
}

// Join adds a user to a channel. Returns false if channel not found.
func (m *Manager) Join(channelName, user string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return false
	}

	if _, already := ch.Members[user]; !already {
		ch.Members[user] = RoleMember
		m.save()
	}
	return true
}

// Leave removes a user from a channel.
func (m *Manager) Leave(channelName, user string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return false
	}

	delete(ch.Members, user)
	m.save()
	return true
}

// IsMember checks if a user is in a channel
func (m *Manager) IsMember(channelName, user string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return false
	}
	_, isMember := ch.Members[user]
	return isMember
}

// GetRole returns the role of a user in a channel
func (m *Manager) GetRole(channelName, user string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return ""
	}
	return ch.Members[user]
}

// SetTopic updates the topic for a channel
func (m *Manager) SetTopic(channelName, topic string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return false
	}
	ch.Topic = topic
	m.save()
	return true
}

// SetDescription updates the description for a channel
func (m *Manager) SetDescription(channelName, desc string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return false
	}
	ch.Description = desc
	m.save()
	return true
}

// List returns all public channels (or all if includePrivate)
func (m *Manager) List(includePrivate bool) []Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []Channel
	for _, ch := range m.channels {
		if !ch.IsPrivate || includePrivate {
			result = append(result, *ch)
		}
	}
	return result
}

// ListUserChannels returns all channels a user is a member of
func (m *Manager) ListUserChannels(user string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []string
	for name, ch := range m.channels {
		if _, ok := ch.Members[user]; ok {
			result = append(result, name)
		}
	}
	return result
}

// Exists checks if a channel exists
func (m *Manager) Exists(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.channels[name]
	return exists
}

// GetMembers returns members of a channel
func (m *Manager) GetMembers(channelName string) map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ch, exists := m.channels[channelName]
	if !exists {
		return nil
	}
	result := make(map[string]string)
	for k, v := range ch.Members {
		result[k] = v
	}
	return result
}

// Get returns a copy of a channel
func (m *Manager) Get(name string) *Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ch, exists := m.channels[name]
	if !exists {
		return nil
	}
	cp := *ch
	return &cp
}

func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.channels, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(m.file, data, 0644)
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.file)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &m.channels); err != nil {
		log.Printf("Warning: failed to parse channels.json: %v", err)
		return err
	}
	return nil
}
