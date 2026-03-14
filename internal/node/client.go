package node

import (
	"distributed-chat/internal/auth"
	"distributed-chat/internal/metrics"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// broadcastToClients sends to clients who are in the message's channel
func (n *Node) broadcastToClients(msg protocol.Message) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for _, client := range n.clients {
		// For DMs, only deliver to sender + recipient
		if msg.Type == protocol.MsgTypeDirectMessage {
			if client.User != msg.Sender && client.User != msg.Recipient {
				continue
			}
		} else if msg.Channel != "" {
			// For channel messages, only deliver to channel members
			if !n.channels.IsMember(msg.Channel, client.User) {
				// But always deliver system messages & join/leave
				if msg.Type != protocol.MsgTypeJoin && msg.Type != protocol.MsgTypeLeave {
					continue
				}
			}
		}

		select {
		case client.Send <- msg:
		default:
			log.Printf("Client %s buffer full, dropping message", client.User)
		}
	}
}

// writePump sends messages from the send channel to the client connection
func (n *Node) writePump(conn net.Conn, send <-chan protocol.Message) {
	defer conn.Close()
	for msg := range send {
		if err := protocol.SendMessage(conn, msg); err != nil {
			log.Printf("Write error to %s: %v", conn.RemoteAddr(), err)
			return
		}
	}
}

// sendToClient sends a server message directly to a single client
func (n *Node) sendToClient(conn net.Conn, content string) {
	n.mu.RLock()
	client, ok := n.clients[conn]
	n.mu.RUnlock()
	if ok {
		select {
		case client.Send <- protocol.Message{
			Type:      protocol.MsgTypeChat,
			ID:        protocol.GenerateID(),
			Sender:    "Server",
			Content:   content,
			Timestamp: time.Now(),
		}:
		default:
		}
	}
}

// handleConnection processes a new incoming TCP connection
func (n *Node) handleConnection(conn net.Conn) {
	// Connection limit check
	n.mu.RLock()
	clientCount := len(n.clients)
	peerCount := len(n.peers)
	n.mu.RUnlock()
	if clientCount+peerCount >= MaxClients+MaxPeers {
		log.Printf("Connection limit reached, rejecting %s", conn.RemoteAddr())
		conn.Close()
		return
	}

	decoder := protocol.NewDecoder(conn)

	msg, err := decoder.Decode()
	if err != nil {
		log.Printf("Failed to read handshake: %v", err)
		conn.Close()
		return
	}

	go func() {
		switch msg.Type {
		case protocol.MsgTypePeerHandshake:
			var peerPort int
			fmt.Sscanf(msg.Content, "%d", &peerPort)

			log.Printf("Accepted peer connection from port %d (%s)", peerPort, conn.RemoteAddr())
			n.mu.Lock()
			n.peers[conn] = time.Now()
			n.peerIDs[conn] = peerPort
			n.mu.Unlock()
			metrics.ActivePeers.Inc()

			// Send User Sync
			n.mu.Lock()
			var localUsers []string
			for _, c := range n.clients {
				localUsers = append(localUsers, c.User)
			}
			n.mu.Unlock()
			if len(localUsers) > 0 {
				userBytes, _ := json.Marshal(localUsers)
				protocol.SendMessage(conn, protocol.Message{
					Type:      protocol.MsgTypeUserSync,
					Sender:    "Node",
					Content:   string(userBytes),
					Timestamp: time.Now(),
				})
			}

			n.handlePeerMessage(conn, decoder)

		case protocol.MsgTypeLogin:
			user, pass := auth.ParseCredentials(msg.Content)
			if n.auth.Check(user, pass) {
				log.Printf("Client authenticated: %s (%s)", user, conn.RemoteAddr())

				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "OK",
				})

				clientReg := ClientRegistration{
					Conn:    conn,
					User:    user,
					Send:    make(chan protocol.Message, 256),
					Channel: "general",
				}
				n.register <- clientReg

				// Auto-join general channel
				n.channels.Join("general", user)

				go n.writePump(conn, clientReg.Send)

				// Send History (Last 50 messages)
				history, err := n.storage.GetRecentMessages(50)
				if err == nil {
					for _, hMsg := range history {
						if hMsg.Type == protocol.MsgTypeChat || hMsg.Type == protocol.MsgTypeImage || hMsg.Type == protocol.MsgTypeJoin || hMsg.Type == protocol.MsgTypeLeave {
							protocol.SendMessage(conn, hMsg)
						}
					}
				}

				n.handleClientLoop(conn, decoder)
			} else {
				log.Printf("Client authentication failed: %s", conn.RemoteAddr())
				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "FAIL",
				})
				conn.Close()
			}

		case protocol.MsgTypeRegister:
			user, pass := auth.ParseCredentials(msg.Content)
			if n.auth.Register(user, pass) {
				log.Printf("New user registered: %s", user)
				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "OK_REGISTERED",
				})
			} else {
				log.Printf("Registration failed (exists): %s", user)
				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "FAIL_EXISTS",
				})
			}
			conn.Close()

		default:
			log.Printf("Unauthenticated connection attempt: %s", conn.RemoteAddr())
			protocol.SendMessage(conn, protocol.Message{
				Type:    protocol.MsgTypeAuthResult,
				Sender:  "Server",
				Content: "AUTH_REQUIRED",
			})
			conn.Close()
		}
	}()
}

// CheckAuth validates credentials (used by SSH server)
func (n *Node) CheckAuth(user, pass string) bool {
	return n.auth.Check(user, pass)
}

// handleClientLoop is the main read loop for an authenticated client
func (n *Node) handleClientLoop(conn net.Conn, decoder *protocol.Decoder) {
	defer func() {
		n.unregister <- conn
	}()

	for {
		msg, err := decoder.Decode()
		if err != nil {
			log.Printf("Error reading from client %s: %v", conn.RemoteAddr(), err)
			break
		}

		// Assign ID if missing
		if msg.ID == "" {
			msg.ID = protocol.GenerateID()
		}

		content := strings.TrimSpace(msg.Content)

		// ---- Command Dispatch ----
		if strings.HasPrefix(content, "/") {
			n.handleCommand(conn, msg, content)
			continue
		}

		// Rate limiting
		n.mu.RLock()
		client := n.clients[conn]
		n.mu.RUnlock()

		if !n.limiter.Allow(client.User) {
			n.sendToClient(conn, "⚠️ Rate limit exceeded. Slow down.")
			continue
		}

		// Check if user is muted
		if n.auth.GetStatus(client.User) == "muted" {
			n.sendToClient(conn, "🔇 You are muted.")
			continue
		}

		// Override sender with authenticated username (prevent impersonation)
		msg.Sender = client.User

		if msg.Channel == "" {
			msg.Channel = client.Channel
			if msg.Channel == "" {
				msg.Channel = "general"
			}
		}

		n.broadcast <- *msg
	}
}
