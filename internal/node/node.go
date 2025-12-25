package node

import (
	"bufio"
	"distributed-chat/internal/auth"
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/protocol"
	"distributed-chat/internal/storage"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
)

type ClientRegistration struct {
	Conn net.Conn
	User string
}

type Node struct {
	port        int
	clients     map[net.Conn]string    // map[conn]username
	remoteUsers map[string]bool        // Set of usernames on other nodes
	peers       map[net.Conn]time.Time // Track last seen time
	peerIDs     map[net.Conn]int       // Add tracking of Peer Ports (IDs)
	storage     *storage.Storage
	auth        *auth.Authenticator // Auth module
	broadcast   chan protocol.Message
	register    chan ClientRegistration
	unregister  chan net.Conn
	mu          sync.Mutex

	// Election State
	isLeader bool
	leaderID int

	// E2EE
	sshKey string
}

const (
	HeartbeatInterval = 5 * time.Second
	PeerTimeout       = 15 * time.Second
)

func NewNode(port int) *Node {
	store := storage.NewStorage(1024)
	snapshotFile := fmt.Sprintf("node-%d.snapshot", port)

	// Load existing snapshot if available
	if err := store.LoadSnapshot(snapshotFile); err != nil {
		log.Printf("Warning: Failed to load snapshot: %v", err)
	}

	// Start auto-snapshotting (every 10 seconds for standard durability)
	store.StartSnapshotter(10*time.Second, snapshotFile)

	return &Node{
		port:        port,
		clients:     make(map[net.Conn]string),
		remoteUsers: make(map[string]bool),
		peers:       make(map[net.Conn]time.Time),
		peerIDs:     make(map[net.Conn]int),
		storage:     store,
		auth:        auth.NewAuthenticator(),
		broadcast:   make(chan protocol.Message),
		register:    make(chan ClientRegistration),
		unregister:  make(chan net.Conn),
		isLeader:    false,
		leaderID:    0,
	}
}

func (n *Node) SetSSHKey(key string) {
	n.sshKey = key
}

func (n *Node) Join(peerAddrs []string) {
	for _, addr := range peerAddrs {
		go func(address string) {
			conn, err := net.Dial("tcp", address)
			if err != nil {
				log.Printf("Failed to connect to peer %s: %v", address, err)
				return
			}
			log.Printf("Connected to peer: %s", address)
			// Send Handshake with our ID (Port) in Content
			handshake := protocol.Message{
				Type:    protocol.MsgTypePeerHandshake,
				Sender:  "Node",
				Content: fmt.Sprintf("%d", n.port), // Send our Port as ID
			}
			if err := protocol.SendMessage(conn, handshake); err != nil {
				log.Printf("Failed to send handshake to %s: %v", address, err)
				conn.Close()
				return
			}

			n.mu.Lock()
			n.peers[conn] = time.Now()
			// content of handshake on connect? No, wait for their handshake?
			// Actually we need to wait for their handshake to know THEIR ID.
			// This logic handles SENDING.
			// Receiving logic tracks `n.peerIDs`.
			n.mu.Unlock()
			// We need to pass the decoder to handlePeerMessage
			decoder := protocol.NewDecoder(conn)
			go n.handlePeerMessage(conn, decoder)
		}(addr)
	}
}

func (n *Node) Run() {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", n.port))
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", n.port, err)
	}
	defer listener.Close()

	log.Printf("Chat Node listening on :%d", n.port)

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

// JoinSSH handles a raw SSH session, translating text to/from Protocol Messages
func (n *Node) JoinSSH(s ssh.Session) {
	adapter := NewSSHAdapter(s, n.sshKey)

	// Start a routine to feed the adapter's Pipe from the session input (Text -> JSON)
	go func() {
		scanner := bufio.NewScanner(s)
		for scanner.Scan() {
			text := scanner.Text()

			finalContent := text
			if n.sshKey != "" {
				encrypted, err := crypto.Encrypt(text, n.sshKey)
				if err != nil {
					log.Printf("SSH Encrypt error: %v", err)
					// Maybe warn user?
				} else {
					finalContent = encrypted
				}
			}

			// Create JSON message
			msg := protocol.Message{
				Type:      protocol.MsgTypeChat,
				Sender:    s.User(),
				Content:   finalContent,
				Timestamp: time.Now(),
			}
			jsonBytes, _ := json.Marshal(msg)

			// Write JSON to the PipeReader (which Node will read from)
			// effectively pretending the SSH user sent JSON
			adapter.pw.Write(jsonBytes)
		}
		// If scan ends, close adapter
		adapter.Close()
		n.unregister <- adapter
	}()

	// Register the adapter as a regular connection
	// Since SSH is already authenticated by the Server's PasswordHandler,
	// we bypass the TCP Login Handshake and register directly.

	n.register <- ClientRegistration{Conn: adapter, User: s.User()}

	// Write a welcome message to the user?
	adapter.Session.Write([]byte("Welcome to Distributed Chat! (SSH Mode)\r\nType your messages below.\r\n"))

	// Enter the client loop (blocks until session ends)
	// SSH Adapter is a ReadWriter, so we can make a decoder for it.
	decoder := protocol.NewDecoder(adapter)
	n.handleClientLoop(adapter, decoder)
}

func NewSSHAdapter(s ssh.Session, key string) *SSHAdapterPipe {
	r, w := io.Pipe()
	return &SSHAdapterPipe{
		Session: s,
		pr:      r,
		pw:      w,
		key:     key,
	}
}

type SSHAdapterPipe struct {
	ssh.Session
	pr  *io.PipeReader
	pw  *io.PipeWriter
	key string
}

func (a *SSHAdapterPipe) Read(b []byte) (int, error) {
	return a.pr.Read(b)
}

func (a *SSHAdapterPipe) Write(b []byte) (int, error) {
	// Received JSON bytes from Node
	var msg protocol.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		return 0, err
	}

	// Format for SSH Terminal (Skip internal messages if needed)
	if msg.Type != protocol.MsgTypeChat && msg.Type != protocol.MsgTypeImage && msg.Type != protocol.MsgTypeJoin && msg.Type != protocol.MsgTypeLeave {
		return len(b), nil // Swallow non-chat/image/join/leave messages for SSH users
	}

	content := msg.Content

	// Handle Image Placeholder for SSH
	if msg.Type == protocol.MsgTypeImage {
		content = "[Image Attachment]"
	} else if a.key != "" && (msg.Type == protocol.MsgTypeChat || msg.Type == protocol.MsgTypeJoin || msg.Type == protocol.MsgTypeLeave) { // Only decrypt chat/join/leave messages
		decrypted, err := crypto.Decrypt(msg.Content, a.key)
		if err == nil {
			content = decrypted + " [E2EE]"
		} else {
			content = fmt.Sprintf("[Encrypted blob: %s...]", msg.Content[:10])
		}
	}

	formatted := fmt.Sprintf("[%s] %s: %s\r\n", msg.Timestamp.Format(time.Kitchen), msg.Sender, content)

	// Write raw string to SSH session
	_, err := a.Session.Write([]byte(formatted))
	return len(b), err
}

func (a *SSHAdapterPipe) Close() error {
	a.pw.Close()
	a.pr.Close()
	return a.Session.Close()
}

func (a *SSHAdapterPipe) LocalAddr() net.Addr                { return a.Session.LocalAddr() }
func (a *SSHAdapterPipe) RemoteAddr() net.Addr               { return a.Session.RemoteAddr() }
func (a *SSHAdapterPipe) SetDeadline(t time.Time) error      { return nil }
func (a *SSHAdapterPipe) SetReadDeadline(t time.Time) error  { return nil }
func (a *SSHAdapterPipe) SetWriteDeadline(t time.Time) error { return nil }

// Broadcast message to clients (and peers if origin is local)
// This implementation assumes the standard broadcast channel is for LOCAL client messages that need global separate.
// But wait, the previous `handleMessages` used `n.broadcast`.
// Let's change the pattern:
// 1. Client msg -> `n.broadcast` -> `handleMessages` -> sends to clients AND peers.
// 2. Peer msg -> `n.broadcastLocal` (new channel?) or just direct loop?
// Let's refactor handleMessages to distinguish.

// actually, let's keep it simple.
// We need two broadcast functions or flags
func (n *Node) broadcastToClients(msg protocol.Message) {
	n.mu.Lock()
	conns := make([]net.Conn, 0, len(n.clients))
	for conn := range n.clients {
		conns = append(conns, conn)
	}
	n.mu.Unlock()

	for _, conn := range conns {
		go func(c net.Conn, m protocol.Message) {
			if err := protocol.SendMessage(c, m); err != nil {
				log.Printf("Error sending to client: %v", err)
			}
		}(conn, msg)
	}
}

func (n *Node) broadcastToPeers(msg protocol.Message) {
	n.mu.Lock()
	conns := make([]net.Conn, 0, len(n.peers))
	for conn := range n.peers {
		conns = append(conns, conn)
	}
	n.mu.Unlock()

	for _, conn := range conns {
		go func(c net.Conn, m protocol.Message) {
			if err := protocol.SendMessage(c, m); err != nil {
				log.Printf("Error sending to peer: %v", err)
			}
		}(conn, msg)
	}
}

func (n *Node) startHeartbeat() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for range ticker.C {
		heartbeat := protocol.Message{Type: protocol.MsgTypeHeartbeat, Sender: "Node", Timestamp: time.Now()}
		n.broadcastToPeers(heartbeat)
	}
}

func (n *Node) startMonitor() {
	ticker := time.NewTicker(2 * time.Second) // Check frequently
	defer ticker.Stop()
	for range ticker.C {
		n.mu.Lock()
		now := time.Now()
		for conn, lastSeen := range n.peers {
			if now.Sub(lastSeen) > PeerTimeout {
				log.Printf("Peer timeout: %s", conn.RemoteAddr())
				conn.Close()
				delete(n.peers, conn)
				delete(n.peerIDs, conn) // Remove ID tracking
			}
		}
		n.mu.Unlock()
	}
}

func (n *Node) startElectionLoop() {
	// Simple Bully-like periodic check
	// If my ID > all peers, I declare myself leader.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		n.mu.Lock()
		highest := true
		for _, id := range n.peerIDs {
			if id > n.port {
				highest = false
				break
			}
		}

		if highest && !n.isLeader {
			log.Printf("I am the new Leader (ID: %d)!", n.port)
			n.isLeader = true
			n.leaderID = n.port
			// Broadcast Coordinator message
			msg := protocol.Message{
				Type:    protocol.MsgTypeCoordinator,
				Sender:  "Node",
				Content: fmt.Sprintf("%d", n.port),
			}
			n.mu.Unlock() // Unlock before broadcast to avoid deadlocks
			n.broadcastToPeers(msg)
		} else if !highest && n.isLeader {
			// Degrade if we thought we were leader but found someone higher?
			// Usually handled by receiving Coordinator message.
			// But if a higher node connects, we should yeild.
			n.isLeader = false
			n.mu.Unlock()
		} else {
			n.mu.Unlock()
		}
	}
}

func (n *Node) handleMessages() {
	for {
		select {
		case reg := <-n.register:
			n.mu.Lock()
			n.clients[reg.Conn] = reg.User
			n.mu.Unlock()
			log.Printf("Active Member Joined: %s (%s)", reg.User, reg.Conn.RemoteAddr())

			// Broadcast Join Message
			joinMsg := protocol.Message{
				Type:      protocol.MsgTypeJoin,
				Sender:    "Server",
				Content:   fmt.Sprintf("*** %s has joined the chat ***\a", reg.User), // \a for Bell
				Timestamp: time.Now(),
			}
			go func() { n.broadcast <- joinMsg }()

		case conn := <-n.unregister:
			n.mu.Lock()
			if user, ok := n.clients[conn]; ok {
				delete(n.clients, conn)
				conn.Close()
				log.Printf("Client disconnected: %s (%s)", conn.RemoteAddr(), user)

				// Broadcast Leave
				if user != "" && user != "Connecting..." {
					leaveMsg := protocol.Message{
						Type:      protocol.MsgTypeLeave,
						Sender:    "Server",
						Content:   fmt.Sprintf("*** %s has left the chat ***\a", user), // \a for Bell
						Timestamp: time.Now(),
					}
					// Only broadcast to active clients?
					// Use a separate goroutine to avoid deadlock?
					// Or just put in broadcast channel?
					// Putting in broadcast channel is safe if it doesn't block.
					go func() { n.broadcast <- leaveMsg }()
				}
			}
			n.mu.Unlock()

		case msg := <-n.broadcast:
			// Save message to disk (persistence) across all nodes that route it
			if msg.Type == protocol.MsgTypeChat {
				if err := n.storage.Save(msg); err != nil {
					log.Printf("Failed to persist message: %v", err)
				}
			}

			// Origin is local client. Broadcast to everyone.
			n.broadcastToClients(msg)
			n.broadcastToPeers(msg)
		}
	}
}

func (n *Node) handlePeerMessage(conn net.Conn, decoder *protocol.Decoder) {
	defer conn.Close()
	for {
		msg, err := decoder.Decode()
		if err != nil {
			log.Printf("Peer disconnected: %v", err)
			n.mu.Lock()
			delete(n.peers, conn)
			delete(n.peerIDs, conn) // Ensure peerID is also removed
			n.mu.Unlock()
			return
		}

		// Update last seen
		n.mu.Lock()
		n.peers[conn] = time.Now()
		n.mu.Unlock()

		if msg.Type == protocol.MsgTypeHeartbeat {
			continue
		}

		if msg.Type == protocol.MsgTypeCoordinator {
			var newLeaderID int
			fmt.Sscanf(msg.Content, "%d", &newLeaderID)
			n.mu.Lock()
			if newLeaderID > n.port {
				log.Printf("Acknowledging new Leader: %d", newLeaderID)
				n.isLeader = false
				n.leaderID = newLeaderID
			} else if newLeaderID < n.port {
				log.Printf("Ignoring subordinate leader claim from %d", newLeaderID)
				// We will re-assert leadership in next loop tick
			}
			n.mu.Unlock()
			continue
		}

		if msg.Type == protocol.MsgTypeSyncRequest {
			log.Printf("Received Sync Request from peer. Since: %v", msg.Timestamp)
			// Retrieve messages after timestamp
			history, err := n.storage.GetMessagesAfter(msg.Timestamp)
			if err != nil {
				log.Printf("Failed to read history: %v", err)
				continue
			}
			// Send them back as regular chat messages
			// Note: This might cause re-broadcast if we are not careful.
			// Currently `handlePeerMessage` broadcasts to Local Clients.
			// It assumes they are NEW messages.
			// If we send them as MsgTypeChat, the receiver will persist them and show them to clients.
			// This is what we want! (Syncing state).
			// However, we must ensure we don't start an infinite loop of re-syncing.
			// But SyncRequest is one-off.
			for _, hMsg := range history {
				if err := protocol.SendMessage(conn, hMsg); err != nil {
					log.Printf("Failed to send history msg: %v", err)
					break
				}
			}
			continue
		}

		if msg.Type == protocol.MsgTypeUserSync {
			var receivedUsers []string
			if err := json.Unmarshal([]byte(msg.Content), &receivedUsers); err == nil {
				n.mu.Lock()
				for _, u := range receivedUsers {
					n.remoteUsers[u] = true
				}
				n.mu.Unlock()
				log.Printf("Synced %d remote users from peer", len(receivedUsers))
			}
			continue
		}

		// Persist peer messages too
		if msg.Type == protocol.MsgTypeChat || msg.Type == protocol.MsgTypeJoin || msg.Type == protocol.MsgTypeLeave {

			// Update Remote Users based on Join/Leave
			if msg.Type == protocol.MsgTypeJoin {
				// Parse username from content?
				// Content is "*** Alice has joined... ***"
				// This is parsing heavy.
				// Ideally we should have put username in Sender?
				// Join message sender is "Server".
				// We should have put the user in Content or Sender.
				// Let's rely on parsing for now?
				// No, let's fix the Join message generation to be cleaner?
				// Wait, the Join message is generated by the node that has the client.
				// It puts "Server" as sender.
				// Content: "*** Alice has joined... "

				// Let's hack parsing for now:
				// "*** %s has joined the chat ***"
				parts := strings.Split(msg.Content, " ")
				if len(parts) >= 2 {
					user := parts[1] // "***", "Alice", "has", ...
					n.mu.Lock()
					n.remoteUsers[user] = true
					n.mu.Unlock()
				}
			} else if msg.Type == protocol.MsgTypeLeave {
				// "*** Alice has left... ***"
				parts := strings.Split(msg.Content, " ")
				if len(parts) >= 2 {
					user := parts[1]
					n.mu.Lock()
					delete(n.remoteUsers, user)
					n.mu.Unlock()
				}
			}

			if err := n.storage.Save(*msg); err != nil {
				log.Printf("Failed to persist peer message: %v", err)
			}
		}

		// Message from peer: Only broadcast to local clients (Full Mesh assumption)
		n.broadcastToClients(*msg)
	}
}

func (n *Node) handleConnection(conn net.Conn) {
	// Not deferring Close here because handleClientLoop/handlePeerMessage takes ownership

	decoder := protocol.NewDecoder(conn)

	// Read first message to identify (Handshake or Client Login)
	msg, err := decoder.Decode()
	if err != nil {
		log.Printf("Failed to read handshake: %v", err)
		conn.Close()
		return
	}

	go func() {
		if msg.Type == protocol.MsgTypePeerHandshake {
			// Register as peer
			var peerPort int
			fmt.Sscanf(msg.Content, "%d", &peerPort)

			log.Printf("Accepted peer connection from port %d (%s)", peerPort, conn.RemoteAddr())
			n.mu.Lock()
			n.peers[conn] = time.Now()
			n.peerIDs[conn] = peerPort
			n.mu.Unlock()

			// Trigger History Sync
			// ... (existing sync req) ...

			// Send User Sync (My local clients)
			n.mu.Lock()
			var localUsers []string
			for _, u := range n.clients {
				localUsers = append(localUsers, u)
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
		} else if msg.Type == protocol.MsgTypeLogin {
			// Client Login Attempt
			user, pass := auth.ParseCredentials(msg.Content)
			if n.auth.Check(user, pass) {
				log.Printf("Client authenticated: %s (%s)", user, conn.RemoteAddr())

				// Send Auth Result OK
				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "OK",
				})

				n.register <- ClientRegistration{Conn: conn, User: user}

				// Send History (Last 50 messages)
				history, err := n.storage.GetRecentMessages(50)
				if err == nil {
					for _, hMsg := range history {
						// Only send Chat/Image messages
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
		} else if msg.Type == protocol.MsgTypeRegister {
			// Registration Attempt
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
			conn.Close() // Close connection after registration attempt
		} else {
			// If not Handshake AND not Login, we reject.
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

func (n *Node) CheckAuth(user, pass string) bool {
	return n.auth.Check(user, pass)
}

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

		// Check for commands
		if strings.TrimSpace(msg.Content) == "/active" || strings.TrimSpace(msg.Content) == "/users" {
			n.mu.Lock()
			var users []string
			for _, u := range n.clients {
				users = append(users, u)
			}
			n.mu.Unlock()

			response := fmt.Sprintf("Active Users (%d): %s", len(users), strings.Join(users, ", "))
			protocol.SendMessage(conn, protocol.Message{
				Type:      protocol.MsgTypeChat,
				Sender:    "Server",
				Content:   response,
				Timestamp: time.Now(),
			})
			continue
		}

		n.broadcast <- *msg
	}
}
