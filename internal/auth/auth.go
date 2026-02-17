package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Authenticator handles user validation
type Authenticator struct {
	mu    sync.Mutex
	users map[string]string // username -> hashed_password
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
		// Note: These are initial defaults. In production, meaningful defaults or empty init is preferred.
		a.Register("admin", "admin123")
		a.Register("Alice", "secret")
		a.Register("Bob", "secret")
		a.save()
	}
	return a
}

// Check validates credentials against stored hash.
func (a *Authenticator) Check(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	storedHash, ok := a.users[username]
	if !ok {
		return false
	}

	match, err := VerifyPassword(password, storedHash)
	if err != nil {
		return false
	}
	return match
}

// Register adds a new user with hashed password. Returns false if user already exists.
func (a *Authenticator) Register(username, password string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.users[username]; exists {
		return false
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false
	}

	a.users[username] = hash
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

// --- Argon2id Helpers ---

const (
	ArgonTime    = 1
	ArgonMemory  = 64 * 1024
	ArgonThreads = 4
	ArgonKeyLen  = 32
	SaltLen      = 16
)

// HashPassword generates an Argon2id hash of the password
// Format: $argon2id$v=19$m=65536,t=1,p=4$salt$hash
func HashPassword(password string) (string, error) {
	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
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
