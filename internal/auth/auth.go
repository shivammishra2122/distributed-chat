package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"distributed-chat/internal/fileutil"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// UserProfile contains extended user information
type UserProfile struct {
	PasswordHash string    `json:"password_hash"`
	Role         string    `json:"role"`                   // admin, member
	DisplayName  string    `json:"display_name,omitempty"`
	Status       string    `json:"status,omitempty"`       // online, away, dnd, idle, offline, muted, banned
	LastSeen     time.Time `json:"last_seen,omitempty"`
}

// Authenticator handles user validation
type Authenticator struct {
	mu    sync.RWMutex
	users map[string]*UserProfile
	file  string
	dirty bool // track if save is needed
}

// Username validation: 2-32 chars, alphanumeric + underscore/dash
var validUsername = regexp.MustCompile(`^[a-zA-Z0-9_-]{2,32}$`)

// NewAuthenticator creates a new instance
func NewAuthenticator() *Authenticator {
	a := &Authenticator{
		users: make(map[string]*UserProfile),
		file:  "users.json",
	}

	if err := a.load(); err != nil {
		log.Printf("No existing users.json found, starting fresh. First registered user gets admin role.")
		// No hardcoded default credentials — first user to register gets admin
	}
	return a
}

// ValidateUsername checks if a username meets requirements
func ValidateUsername(username string) error {
	if !validUsername.MatchString(username) {
		return fmt.Errorf("username must be 2-32 characters, alphanumeric/underscore/dash only")
	}
	return nil
}

// Check validates credentials against stored hash.
func (a *Authenticator) Check(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	profile, ok := a.users[username]
	if !ok {
		// Constant-time comparison even for non-existent users to prevent timing attacks
		HashPassword("dummy-password-for-timing")
		return false
	}

	// Check if banned
	if profile.Status == "banned" {
		return false
	}

	match, err := VerifyPassword(password, profile.PasswordHash)
	if err != nil {
		return false
	}

	if match {
		profile.LastSeen = time.Now()
		profile.Status = "online"
		a.dirty = true
		a.saveIfDirty()
	}
	return match
}

// Register adds a new user with hashed password. Returns error on failure.
func (a *Authenticator) Register(username, password string) bool {
	if err := ValidateUsername(username); err != nil {
		return false
	}
	if len(password) < 4 {
		return false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.users[username]; exists {
		return false
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false
	}

	// First user gets admin role
	role := "member"
	if len(a.users) == 0 {
		role = "admin"
	}

	a.users[username] = &UserProfile{
		PasswordHash: hash,
		Role:         role,
		DisplayName:  username,
		Status:       "offline",
		LastSeen:     time.Now(),
	}
	a.dirty = true
	a.saveIfDirty()
	return true
}

// SetRole sets the role for a user
func (a *Authenticator) SetRole(username, role string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if profile, ok := a.users[username]; ok {
		profile.Role = role
		a.dirty = true
		a.saveIfDirty()
	}
}

// GetRole returns the role for a user
func (a *Authenticator) GetRole(username string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if profile, ok := a.users[username]; ok {
		return profile.Role
	}
	return "member"
}

// SetStatus sets the status for a user
func (a *Authenticator) SetStatus(username, status string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if profile, ok := a.users[username]; ok {
		profile.Status = status
		profile.LastSeen = time.Now()
		a.dirty = true
		a.saveIfDirty()
	}
}

// GetStatus returns the status for a user
func (a *Authenticator) GetStatus(username string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if profile, ok := a.users[username]; ok {
		return profile.Status
	}
	return ""
}

// GetAllProfiles returns a copy of all user profiles (without passwords)
func (a *Authenticator) GetAllProfiles() map[string]map[string]interface{} {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := make(map[string]map[string]interface{})
	for name, profile := range a.users {
		result[name] = map[string]interface{}{
			"role":         profile.Role,
			"display_name": profile.DisplayName,
			"status":       profile.Status,
			"last_seen":    profile.LastSeen,
		}
	}
	return result
}

// UserExists checks if a username is registered
func (a *Authenticator) UserExists(username string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.users[username]
	return ok
}

func (a *Authenticator) saveIfDirty() {
	if !a.dirty {
		return
	}
	data, err := json.MarshalIndent(a.users, "", "  ")
	if err != nil {
		log.Printf("Error marshaling users: %v", err)
		return
	}
	if err := fileutil.AtomicWriteFile(a.file, data, 0600); err != nil {
		log.Printf("Error saving users: %v", err)
		return
	}
	a.dirty = false
}

func (a *Authenticator) load() error {
	data, err := os.ReadFile(a.file)
	if err != nil {
		return err
	}

	// Try new format first (UserProfile)
	var profiles map[string]*UserProfile
	if err := json.Unmarshal(data, &profiles); err == nil {
		for _, v := range profiles {
			if v.PasswordHash != "" {
				a.users = profiles
				return nil
			}
			break
		}
	}

	// Fallback to old format (username -> hash string)
	var oldFormat map[string]string
	if err := json.Unmarshal(data, &oldFormat); err != nil {
		return err
	}

	// Migrate old format to new
	for name, hash := range oldFormat {
		role := "member"
		if name == "admin" {
			role = "admin"
		}
		a.users[name] = &UserProfile{
			PasswordHash: hash,
			Role:         role,
			DisplayName:  name,
			Status:       "offline",
			LastSeen:     time.Now(),
		}
	}
	a.dirty = true
	a.saveIfDirty()
	return nil
}

// ParseCredentials parses "user:pass" string
func ParseCredentials(cred string) (string, string) {
	parts := strings.SplitN(cred, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// --- Argon2id Helpers ---

const (
	ArgonTime    = 1
	ArgonMemory  = 64 * 1024
	ArgonThreads = 4
	ArgonKeyLen  = 32
	SaltLen      = 16
)

// HashPassword generates an Argon2id hash of the password
func HashPassword(password string) (string, error) {
	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, ArgonTime, ArgonMemory, ArgonThreads, ArgonKeyLen)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encodedHash := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, ArgonMemory, ArgonTime, ArgonThreads, b64Salt, b64Hash)

	return encodedHash, nil
}

// VerifyPassword compares a plaintext password with a stored hash
func VerifyPassword(password, encodedHash string) (bool, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, fmt.Errorf("invalid hash format")
	}

	if parts[1] != "argon2id" {
		return false, fmt.Errorf("incompatible variant")
	}

	var version int
	_, err := fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, fmt.Errorf("incompatible version")
	}

	var memory uint32
	var time uint32
	var threads uint8
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads)
	if err != nil {
		return false, err
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}

	decodedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}

	keyLength := uint32(len(decodedHash))

	comparisonHash := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLength)

	return subtle.ConstantTimeCompare(decodedHash, comparisonHash) == 1, nil
}
