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

	// Start server
	// Note: gliderlabs/ssh auto-generates a host key if none provided
	log.Fatal(ssh.ListenAndServe(fmt.Sprintf(":%d", port), nil, opts...))
}

