package ssh

import (
	"distributed-chat/internal/node"
	"fmt"
	"log"

	"github.com/gliderlabs/ssh"
)

// StartServer starts an SSH server on the given port
func StartServer(port int, n *node.Node) {
	ssh.Handle(func(s ssh.Session) {
		user := s.User()
		log.Printf("New SSH connection from %s (%s)", s.RemoteAddr(), user)

		// Join the node using the Translation Adapter in Node
		n.JoinSSH(s)
	})

	log.Printf("SSH Server listening on port %d", port)

	// Configure Password Handler
	pwdHandler := func(ctx ssh.Context, password string) bool {
		return n.CheckAuth(ctx.User(), password)
	}

	// Configure Options
	opts := []ssh.Option{
		ssh.PasswordAuth(pwdHandler),
	}

	hostKey := GetHostKeyPath()
	if hostKey != "" {
		opts = append(opts, ssh.HostKeyFile(hostKey))
	}

	// Start server with options
	log.Fatal(ssh.ListenAndServe(fmt.Sprintf(":%d", port), nil, opts...))
}

// GetHostKeyPath returns path to a host key (simplified or auto-gen)
// For dev, gliderlabs/ssh auto-generates if not provided?
// It says "If no HostKey is set... it will generate one".
// So we can pass nil options or default?
// ListenAndServe with nil handler uses default?
// We need to pass the handler.
// ssh.ListenAndServe(addr, handler)

func GetHostKeyPath() string {
	// In a real app we'd load from disk.
	return ""
}

// SSHConn wraps ssh.Session to implement net.Conn
// It acts as a protocol translator between Raw Text (SSH) and JSON (Node).
type SSHConn struct {
	ssh.Session
	// Buffers?
}

func (c *SSHConn) Read(b []byte) (n int, err error) {
	// This is TRICKY.
	// Node calls `protocol.ReadMessage` which calls `json.NewDecoder(conn).Decode()`.
	// The decoder pulls from this Read().
	// We need to supply JSON.

	// BUT the user types "Hello\n".
	// We need to catch that "Hello\n", wrap it in `{"type":0, "content":"Hello"}`
	// and feed THAT to the buffer.

	// Implementing a full `Read` translator is hard.
	// Better approach:
	// Don't pass `SSHConn` to `Node.handleConnection` directly if `handleConnection` assumes raw JSON stream.
	// Instead, create a specialized `HandleSSHClient` on Node.
	return 0, fmt.Errorf("Not implemented")
}

func (c *SSHConn) Write(b []byte) (n int, err error) {
	// Node writes JSON here.
	// We need to parse JSON -> Format pretty string -> Write to session.
	// Again, better to handle logic separate.
	return 0, fmt.Errorf("Not implemented")
}

func (c *SSHConn) Close() error {
	return c.Session.Close()
}
