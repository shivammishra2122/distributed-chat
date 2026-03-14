package node

import (
	"context"
	"crypto/tls"
	"distributed-chat/internal/auth"
	"distributed-chat/internal/channel"
	"distributed-chat/internal/metrics"
	"distributed-chat/internal/protocol"
	"distributed-chat/internal/ratelimit"
	"distributed-chat/internal/storage"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// ClientRegistration represents a connected client
type ClientRegistration struct {
	Conn    net.Conn
	User    string
	Send    chan protocol.Message // Outbound channel for this client
	Channel string               // Current active channel
}

// Node is the core chat server node
type Node struct {
	port        int
	clients     map[net.Conn]ClientRegistration
	remoteUsers map[string]bool
	peers       map[net.Conn]time.Time
	peerIDs     map[net.Conn]int
	storage     *storage.Storage
	auth        *auth.Authenticator
	channels    *channel.Manager
	limiter     *ratelimit.Limiter
	broadcast   chan protocol.Message
	register    chan ClientRegistration
	unregister  chan net.Conn
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc

	// Election State
	isLeader bool
	leaderID int

	// E2EE
	sshKey string

	// TLS
	tlsConfig *tls.Config
	listener  net.Listener

	// Uptime
	startTime time.Time
}

const (
	HeartbeatInterval = 5 * time.Second
	PeerTimeout       = 15 * time.Second
	MaxClients        = 500
	MaxPeers          = 50
)

// NewNode creates and initializes a new chat node
func NewNode(port int, tlsConfig *tls.Config) *Node {
	store := storage.NewStorage(1024, ".", port)

	if err := store.LoadAOF(".", port); err != nil {
		log.Printf("Warning: Failed to load AOF: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	n := &Node{
		port:        port,
		clients:     make(map[net.Conn]ClientRegistration),
		remoteUsers: make(map[string]bool),
		peers:       make(map[net.Conn]time.Time),
		peerIDs:     make(map[net.Conn]int),
		storage:     store,
		auth:        auth.NewAuthenticator(),
		channels:    channel.NewManager(),
		limiter:     ratelimit.NewLimiter(10, 20), // 10 msg/sec, burst 20
		broadcast:   make(chan protocol.Message),
		register:    make(chan ClientRegistration),
		unregister:  make(chan net.Conn),
		isLeader:    false,
		leaderID:    0,
		tlsConfig:   tlsConfig,
		startTime:   time.Now(),
		ctx:         ctx,
		cancel:      cancel,
	}

	// Periodic AOF compaction (every 30 minutes)
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := store.CompactAOF(".", port); err != nil {
					log.Printf("AOF compaction failed: %v", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Periodic rate limiter cleanup (every 5 minutes)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				n.limiter.Cleanup(10 * time.Minute)
			case <-ctx.Done():
				return
			}
		}
	}()

	return n
}

// SetSSHKey sets the E2EE key for SSH sessions
func (n *Node) SetSSHKey(key string) {
	n.sshKey = key
}

// GetStorage exposes the storage for external use (API server)
func (n *Node) GetStorage() *storage.Storage {
	return n.storage
}

// GetChannelManager exposes the channel manager for external use
func (n *Node) GetChannelManager() *channel.Manager {
	return n.channels
}

// GetAuth exposes the authenticator for external use
func (n *Node) GetAuth() *auth.Authenticator {
	return n.auth
}

// GetNodeInfo returns info about this node for health checks
func (n *Node) GetNodeInfo() map[string]interface{} {
	n.mu.RLock()
	defer n.mu.RUnlock()

	peerList := make([]int, 0, len(n.peerIDs))
	for _, id := range n.peerIDs {
		peerList = append(peerList, id)
	}

	return map[string]interface{}{
		"port":        n.port,
		"is_leader":   n.isLeader,
		"leader_id":   n.leaderID,
		"clients":     len(n.clients),
		"max_clients": MaxClients,
		"peers":       len(n.peers),
		"max_peers":   MaxPeers,
		"peer_ids":    peerList,
		"uptime_sec":  time.Since(n.startTime).Seconds(),
	}
}

// InjectMessage lets external systems (API) push a message into the broadcast loop
func (n *Node) InjectMessage(msg protocol.Message) {
	if msg.ID == "" {
		msg.ID = protocol.GenerateID()
	}
	if msg.Channel == "" {
		msg.Channel = "general"
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	n.broadcast <- msg
}

// GetOnlineUsers returns a list of currently connected usernames
func (n *Node) GetOnlineUsers() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	var users []string
	for _, c := range n.clients {
		users = append(users, c.User)
	}
	return users
}

// Run starts the TLS listener and main event loops
func (n *Node) Run() {
	listener, err := tls.Listen("tcp", fmt.Sprintf(":%d", n.port), n.tlsConfig)
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", n.port, err)
	}
	n.listener = listener
	defer n.listener.Close()

	log.Printf("Chat Node listening on :%d (TLS Enabled)", n.port)

	go n.handleMessages()
	go n.startHeartbeat()
	go n.startMonitor()
	go n.startElectionLoop()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		go n.handleConnection(conn)
	}
}

// handleMessages is the central event loop for register/unregister/broadcast
func (n *Node) handleMessages() {
	for {
		select {
		case reg := <-n.register:
			n.mu.Lock()
			n.clients[reg.Conn] = reg
			n.mu.Unlock()
			metrics.ConnectedClients.Inc()
			log.Printf("Active Member Joined: %s (%s)", reg.User, reg.Conn.RemoteAddr())

			joinMsg := protocol.Message{
				Type:      protocol.MsgTypeJoin,
				ID:        protocol.GenerateID(),
				Sender:    "Server",
				Content:   fmt.Sprintf("*** %s has joined the chat ***\a", reg.User),
				Channel:   "general",
				Timestamp: time.Now(),
			}
			go func() { n.broadcast <- joinMsg }()

		case conn := <-n.unregister:
			n.mu.Lock()
			if client, ok := n.clients[conn]; ok {
				delete(n.clients, conn)
				close(client.Send)
				conn.Close()
				metrics.ConnectedClients.Dec()
				log.Printf("Client disconnected: %s (%s)", conn.RemoteAddr(), client.User)

				if client.User != "" && client.User != "Connecting..." {
					leaveMsg := protocol.Message{
						Type:      protocol.MsgTypeLeave,
						ID:        protocol.GenerateID(),
						Sender:    "Server",
						Content:   fmt.Sprintf("*** %s has left the chat ***\a", client.User),
						Channel:   "general",
						Timestamp: time.Now(),
					}
					go func() { n.broadcast <- leaveMsg }()
				}
			}
			n.mu.Unlock()

		case msg := <-n.broadcast:
			metrics.MessagesTotal.Inc()
			if msg.Type == protocol.MsgTypeChat || msg.Type == protocol.MsgTypeDirectMessage || msg.Type == protocol.MsgTypeImage {
				if err := n.storage.Save(msg); err != nil {
					log.Printf("Failed to persist message: %v", err)
				}
			}

			n.broadcastToClients(msg)
			// Only relay to peers if not a DM (DMs stay local for now)
			if msg.Type != protocol.MsgTypeDirectMessage && msg.Type != protocol.MsgTypeTyping {
				n.broadcastToPeers(msg)
			}
		}
	}
}

// Shutdown gracefully stops the node
func (n *Node) Shutdown() {
	log.Println("Shutting down node...")

	// Cancel context to stop all background goroutines
	n.cancel()

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.listener != nil {
		n.listener.Close()
	}

	// Compact AOF before shutdown
	if err := n.storage.CompactAOF(".", n.port); err != nil {
		log.Printf("Final AOF compaction failed: %v", err)
	}

	if err := n.storage.Close(); err != nil {
		log.Printf("Error closing storage: %v", err)
	}

	for conn := range n.clients {
		conn.Close()
	}
	for conn := range n.peers {
		conn.Close()
	}
}
