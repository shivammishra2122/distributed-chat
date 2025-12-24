package auth

import "strings"

// Authenticator handles user validation
type Authenticator struct {
	// Map "username" -> "password"
	users map[string]string
}

// NewAuthenticator creates a new instance with a default set of users
// In production, this would load from a DB or file.
func NewAuthenticator() *Authenticator {
	return &Authenticator{
		users: map[string]string{
			"admin": "admin123",
			"user1": "pass1",
			"user2": "pass2",
			"Alice": "secret",
			"Bob":   "secret",
		},
	}
}

// Check validates credentials.
func (a *Authenticator) Check(username, password string) bool {
	expected, ok := a.users[username]
	if !ok {
		return false
	}
	return expected == password
}

// ParseCredentials parses "user:pass" string
func ParseCredentials(cred string) (string, string) {
	parts := strings.SplitN(cred, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
