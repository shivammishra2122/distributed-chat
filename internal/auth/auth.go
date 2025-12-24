package auth

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// Authenticator handles user validation
type Authenticator struct {
	mu    sync.Mutex
	users map[string]string // username -> password
	file  string
}

// NewAuthenticator creates a new instance with a default set of users
func NewAuthenticator() *Authenticator {
	a := &Authenticator{
		users: make(map[string]string),
		file:  "users.json",
	}

	// Try to load
	if err := a.load(); err != nil {
		// If load fails (e.g. no file), init defaults
		a.users["admin"] = "admin123"
		a.users["Alice"] = "secret"
		a.users["Bob"] = "secret"
		a.save()
	}
	return a
}

// Check validates credentials.
func (a *Authenticator) Check(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	expected, ok := a.users[username]
	if !ok {
		return false
	}
	return expected == password
}

// Register adds a new user. Returns false if user already exists.
func (a *Authenticator) Register(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.users[username]; exists {
		return false
	}

	a.users[username] = password
	a.save()
	return true
}

func (a *Authenticator) save() error {
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.file, data, 0644)
}

func (a *Authenticator) load() error {
	data, err := os.ReadFile(a.file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &a.users)
}

// ParseCredentials parses "user:pass" string
func ParseCredentials(cred string) (string, string) {
	parts := strings.SplitN(cred, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
