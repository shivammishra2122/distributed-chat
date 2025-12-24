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
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
)

type Node struct {
	port       int
	clients    map[net.Conn]bool
	peers      map[net.Conn]time.Time // Track last seen time
	peerIDs    map[net.Conn]int       // Add tracking of Peer Ports (IDs)
	storage    *storage.Storage
	auth       *auth.Authenticator // Auth module
	broadcast  chan protocol.Message
	register   chan net.Conn
	unregister chan net.Conn
	mu         sync.Mutex

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
	store, err := storage.NewStorage(port)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	return &Node{
		port:       port,
		clients:    make(map[net.Conn]bool),
		peers:      make(map[net.Conn]time.Time),
		peerIDs:    make(map[net.Conn]int),
		storage:    store,
		auth:       auth.NewAuthenticator(),
		broadcast:  make(chan protocol.Message),
		register:   make(chan net.Conn),
		unregister: make(chan net.Conn),
		isLeader:   false,
		leaderID:   0,
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
			go n.handlePeerMessage(conn)
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

	n.register <- adapter

	// Write a welcome message to the user?
	adapter.Session.Write([]byte("Welcome to Distributed Chat! (SSH Mode)\r\nType your messages below.\r\n"))

	// Enter the client loop (blocks until session ends)
	n.handleClientLoop(adapter)
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
	if msg.Type != protocol.MsgTypeChat && msg.Type != protocol.MsgTypeImage {
		return len(b), nil // Swallow non-chat messages for SSH users
	}

	content := msg.Content

	// Handle Image Placeholder for SSH
	if msg.Type == protocol.MsgTypeImage {
		content = "[Image]" // Basic placeholder
		// If we want to show filename or something, we'd need to parse the content (which might be encrypted blob)
		// For E2EE, it's just a blob.
		content = "[Image Attachment]"
	} else if a.key != "" {
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
	defer n.mu.Unlock()
	for conn := range n.clients {
		go func(c net.Conn, m protocol.Message) {
			if err := protocol.SendMessage(c, m); err != nil {
				log.Printf("Error sending to client: %v", err)
			}
		}(conn, msg)
	}
}

func (n *Node) broadcastToPeers(msg protocol.Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for conn := range n.peers {
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
		case conn := <-n.register:
			n.mu.Lock()
			n.clients[conn] = true
			n.mu.Unlock()
			log.Printf("New client connected: %s", conn.RemoteAddr())

		case conn := <-n.unregister:
			n.mu.Lock()
			if _, ok := n.clients[conn]; ok {
				delete(n.clients, conn)
				conn.Close()
				log.Printf("Client disconnected: %s", conn.RemoteAddr())
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

func (n *Node) handlePeerMessage(conn net.Conn) {
	defer conn.Close()
	for {
		msg, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Printf("Peer disconnected: %v", err)
			n.mu.Lock()
			delete(n.peers, conn)
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

		// Persist peer messages too
		if msg.Type == protocol.MsgTypeChat {
			if err := n.storage.Save(*msg); err != nil {
				log.Printf("Failed to persist peer message: %v", err)
			}
		}

		// Message from peer: Only broadcast to local clients (Full Mesh assumption)
		n.broadcastToClients(*msg)
	}
}

func (n *Node) handleConnection(conn net.Conn) {
	// Peek or read the first message to determine type
	// For simplicity, we just read it. If it's not handshake, we assume it's a client join/chat and process it.
	// But `ReadMessage` consumes it. We might need to handle it.

	// Issue: If it's a client, the first message might be "Chat" or nothing yet? Clients usually just connect.
	// So we need clients to send a "Join" or "Handshake" too?
	// Or we assume clients are passive until they send "Chat".
	// But Peers MUST send Handshake immediately.

	// Let's rely on a timeout? No.
	// Let's assume clients send NOTHING on connect, or we wait for first message.
	// If first message is Handshake -> Peer.
	// If first message is Chat/Join -> Client.

	// Actually, `handleConnection` blocks reading the first message.
	// If a client connects and waits for user input, this blocks.
	// This is fine, we don't treat them as registered until they send something?
	// No, we want to register clients immediately for broadcasting?
	// But without knowing if it's a peer, we can't puts it in `n.peers` vs `n.clients`.

	// Better approach:
	// Clients enter `n.clients` by default?
	// If we receive Handshake, we move it to `n.peers`?
	// Complicated state transition.

	// Simplest: Clients ALSO send a Join/Handshake message.
	// Current client code:
	// Connects.
	// Starts reading goroutine.
	// Waits for user input.
	// User types "Hello". Sends MsgTypeChat.

	// So if we block on `ReadMessage`, the client won't see any messages until they type something.
	// That's bad.

	// Solution:
	// Start a goroutine that reads.
	// Checks type.
	// If Handshake -> Register as Peer.
	// Else -> Register as Client and process that message.

	go func() {
		msg, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Printf("Connection error during handshake: %v", err)
			conn.Close()
			return
		}

		if msg.Type == protocol.MsgTypePeerHandshake {
			log.Printf("Accepted peer connection: %s", conn.RemoteAddr())

			// Parse ID from content
			var peerID int
			fmt.Sscanf(msg.Content, "%d", &peerID)
			log.Printf("Peer identified as ID: %d", peerID)

			n.mu.Lock()
			n.peers[conn] = time.Now()
			n.peerIDs[conn] = peerID
			n.mu.Unlock()

			// Trigger History Sync
			// Get my last timestamp
			lastTime, err := n.storage.GetLastTimestamp()
			if err != nil {
				log.Printf("Error getting last timestamp: %v", err)
			} else {
				// Send Sync Request
				req := protocol.Message{
					Type:      protocol.MsgTypeSyncRequest,
					Sender:    "Node",
					Timestamp: lastTime, // This is the 'since' time
				}
				protocol.SendMessage(conn, req)
			}

			n.handlePeerMessage(conn)
			n.handlePeerMessage(conn)
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

				n.register <- conn
				n.handleClientLoop(conn)
			} else {
				log.Printf("Client authentication failed: %s", conn.RemoteAddr())
				protocol.SendMessage(conn, protocol.Message{
					Type:    protocol.MsgTypeAuthResult,
					Sender:  "Server",
					Content: "FAIL",
				})
				conn.Close()
			}
		} else {
			// If not Handshake AND not Login, we reject.
			// Currently we treated it as "Client Connection" indiscriminately.
			// Now we enforce Login.

			// Backward compatibility: If we just dump them in `handleClientLoop`,
			// they will be unauthenticated.
			// Let's REJECT.
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

func (n *Node) handleClientLoop(conn net.Conn) {
	defer func() {
		n.unregister <- conn
	}()

	for {
		msg, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Printf("Error reading from client %s: %v", conn.RemoteAddr(), err)
			break
		}
		n.broadcast <- *msg
	}
}
