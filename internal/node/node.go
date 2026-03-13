package node

import (
	"bufio"
	"crypto/tls"
	"distributed-chat/internal/auth"
	"distributed-chat/internal/channel"
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/metrics"
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
	Conn    net.Conn
	User    string
	Send    chan protocol.Message // Outbound channel for this client
	Channel string               // Current active channel
}

type Node struct {
	port        int
	clients     map[net.Conn]ClientRegistration
	remoteUsers map[string]bool
	peers       map[net.Conn]time.Time
	peerIDs     map[net.Conn]int
	storage     *storage.Storage
	auth        *auth.Authenticator
	channels    *channel.Manager
	broadcast   chan protocol.Message
	register    chan ClientRegistration
	unregister  chan net.Conn
	mu          sync.Mutex

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
)

func NewNode(port int, tlsConfig *tls.Config) *Node {
	store := storage.NewStorage(1024, ".", port)

	if err := store.LoadAOF(".", port); err != nil {
		log.Printf("Warning: Failed to load AOF: %v", err)
	}

	return &Node{
		port:        port,
		clients:     make(map[net.Conn]ClientRegistration),
		remoteUsers: make(map[string]bool),
		peers:       make(map[net.Conn]time.Time),
		peerIDs:     make(map[net.Conn]int),
		storage:     store,
		auth:        auth.NewAuthenticator(),
		channels:    channel.NewManager(),
		broadcast:   make(chan protocol.Message),
		register:    make(chan ClientRegistration),
		unregister:  make(chan net.Conn),
		isLeader:    false,
		leaderID:    0,
		tlsConfig:   tlsConfig,
		startTime:   time.Now(),
	}
}

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
	n.mu.Lock()
	defer n.mu.Unlock()

	peerList := make([]int, 0, len(n.peerIDs))
	for _, id := range n.peerIDs {
		peerList = append(peerList, id)
	}

	return map[string]interface{}{
		"port":       n.port,
		"is_leader":  n.isLeader,
		"leader_id":  n.leaderID,
		"clients":    len(n.clients),
		"peers":      len(n.peers),
		"peer_ids":   peerList,
		"uptime_sec": time.Since(n.startTime).Seconds(),
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
	n.mu.Lock()
	defer n.mu.Unlock()
	var users []string
	for _, c := range n.clients {
		users = append(users, c.User)
	}
	return users
}

func (n *Node) Join(peerAddrs []string) {
	for _, addr := range peerAddrs {
		go func(address string) {
			clientConfig := &tls.Config{
				RootCAs:    n.tlsConfig.RootCAs,
				ServerName: "localhost",
			}

			conn, err := tls.Dial("tcp", address, clientConfig)
			if err != nil {
				log.Printf("Failed to connect to peer %s: %v", address, err)
				return
			}
			log.Printf("Connected to peer: %s", address)
			handshake := protocol.Message{
				Type:    protocol.MsgTypePeerHandshake,
				Sender:  "Node",
				Content: fmt.Sprintf("%d", n.port),
			}
			if err := protocol.SendMessage(conn, handshake); err != nil {
				log.Printf("Failed to send handshake to %s: %v", address, err)
				conn.Close()
				return
			}

			n.mu.Lock()
			n.peers[conn] = time.Now()
			n.mu.Unlock()
			metrics.ActivePeers.Inc()
			decoder := protocol.NewDecoder(conn)
			go n.handlePeerMessage(conn, decoder)
		}(addr)
	}
}

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

// JoinSSH handles a raw SSH session
func (n *Node) JoinSSH(s ssh.Session) {
	adapter := NewSSHAdapter(s, n.sshKey)

	go func() {
		scanner := bufio.NewScanner(s)
		for scanner.Scan() {
			text := scanner.Text()

			finalContent := text
			if n.sshKey != "" {
				encrypted, err := crypto.Encrypt(text, n.sshKey)
				if err != nil {
					log.Printf("SSH Encrypt error: %v", err)
				} else {
					finalContent = encrypted
				}
			}

			msg := protocol.Message{
				Type:      protocol.MsgTypeChat,
				ID:        protocol.GenerateID(),
				Sender:    s.User(),
				Content:   finalContent,
				Channel:   "general",
				Timestamp: time.Now(),
			}
			jsonBytes, _ := json.Marshal(msg)
			adapter.pw.Write(jsonBytes)
		}
		adapter.Close()
		n.unregister <- adapter
	}()

	clientReg := ClientRegistration{
		Conn:    adapter,
		User:    s.User(),
		Send:    make(chan protocol.Message, 256),
		Channel: "general",
	}

	// Auto-join general channel
	n.channels.Join("general", s.User())

	n.register <- clientReg
	go n.writePump(adapter, clientReg.Send)

	adapter.Session.Write([]byte("Welcome to Distributed Chat! (SSH Mode)\r\nType your messages below.\r\n"))

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
	var msg protocol.Message
	if err := json.Unmarshal(b, &msg); err != nil {
		return 0, err
	}

	if msg.Type != protocol.MsgTypeChat && msg.Type != protocol.MsgTypeImage && msg.Type != protocol.MsgTypeJoin && msg.Type != protocol.MsgTypeLeave {
		return len(b), nil
	}

	content := msg.Content
	if msg.Type == protocol.MsgTypeImage {
		content = "[Image Attachment]"
	} else if a.key != "" && (msg.Type == protocol.MsgTypeChat || msg.Type == protocol.MsgTypeJoin || msg.Type == protocol.MsgTypeLeave) {
		decrypted, err := crypto.Decrypt(msg.Content, a.key)
		if err == nil {
			content = decrypted + " [E2EE]"
		} else {
			content = fmt.Sprintf("[Encrypted blob: %s...]", msg.Content[:10])
		}
	}

	editTag := ""
	if msg.Edited {
		editTag = " (edited)"
	}

	chTag := ""
	if msg.Channel != "" && msg.Channel != "general" {
		chTag = fmt.Sprintf(" #%s", msg.Channel)
	}

	formatted := fmt.Sprintf("[%s]%s %s: %s%s\r\n", msg.Timestamp.Format(time.Kitchen), chTag, msg.Sender, content, editTag)
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

// broadcastToClients sends to clients who are in the message's channel
func (n *Node) broadcastToClients(msg protocol.Message) {
	n.mu.Lock()
	defer n.mu.Unlock()

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

func (n *Node) writePump(conn net.Conn, send <-chan protocol.Message) {
	defer conn.Close()
	for msg := range send {
		if err := protocol.SendMessage(conn, msg); err != nil {
			log.Printf("Write error to %s: %v", conn.RemoteAddr(), err)
			return
		}
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
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		n.mu.Lock()
		now := time.Now()
		for conn, lastSeen := range n.peers {
			if now.Sub(lastSeen) > PeerTimeout {
				log.Printf("Peer timeout: %s", conn.RemoteAddr())
				conn.Close()
				delete(n.peers, conn)
				delete(n.peerIDs, conn)
			}
		}
		n.mu.Unlock()
	}
}

func (n *Node) startElectionLoop() {
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
			msg := protocol.Message{
				Type:    protocol.MsgTypeCoordinator,
				Sender:  "Node",
				Content: fmt.Sprintf("%d", n.port),
			}
			n.mu.Unlock()
			n.broadcastToPeers(msg)
		} else if !highest && n.isLeader {
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

func (n *Node) handlePeerMessage(conn net.Conn, decoder *protocol.Decoder) {
	defer conn.Close()
	defer metrics.ActivePeers.Dec()
	for {
		msg, err := decoder.Decode()
		if err != nil {
			log.Printf("Peer disconnected: %v", err)
			n.mu.Lock()
			delete(n.peers, conn)
			delete(n.peerIDs, conn)
			n.mu.Unlock()
			return
		}

		n.mu.Lock()
		n.peers[conn] = time.Now()
		n.mu.Unlock()

		switch msg.Type {
		case protocol.MsgTypeHeartbeat:
			continue

		case protocol.MsgTypeCoordinator:
			var newLeaderID int
			fmt.Sscanf(msg.Content, "%d", &newLeaderID)
			n.mu.Lock()
			if newLeaderID > n.port {
				log.Printf("Acknowledging new Leader: %d", newLeaderID)
				n.isLeader = false
				n.leaderID = newLeaderID
			} else if newLeaderID < n.port {
				log.Printf("Ignoring subordinate leader claim from %d", newLeaderID)
			}
			n.mu.Unlock()
			continue

		case protocol.MsgTypeSyncRequest:
			log.Printf("Received Sync Request from peer. Since: %v", msg.Timestamp)
			history, err := n.storage.GetMessagesAfter(msg.Timestamp)
			if err != nil {
				log.Printf("Failed to read history: %v", err)
				continue
			}
			for _, hMsg := range history {
				if err := protocol.SendMessage(conn, hMsg); err != nil {
					log.Printf("Failed to send history msg: %v", err)
					break
				}
			}
			continue

		case protocol.MsgTypeUserSync:
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

		case protocol.MsgTypeChat, protocol.MsgTypeJoin, protocol.MsgTypeLeave, protocol.MsgTypeImage:
			if msg.Type == protocol.MsgTypeJoin {
				parts := strings.Split(msg.Content, " ")
				if len(parts) >= 2 {
					user := parts[1]
					n.mu.Lock()
					n.remoteUsers[user] = true
					n.mu.Unlock()
				}
			} else if msg.Type == protocol.MsgTypeLeave {
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

		case protocol.MsgTypeEditMessage:
			n.storage.UpdateMessage(msg.ID, msg.Content)

		case protocol.MsgTypeDeleteMessage:
			n.storage.DeleteMessage(msg.ID)

		case protocol.MsgTypeReaction:
			if msg.Metadata != nil {
				n.storage.AddReaction(msg.ID, msg.Content, msg.Sender)
			}
		}

		n.broadcastToClients(*msg)
	}
}

func (n *Node) handleConnection(conn net.Conn) {
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

func (n *Node) CheckAuth(user, pass string) bool {
	return n.auth.Check(user, pass)
}

// sendToClient sends a server message directly to a single client
func (n *Node) sendToClient(conn net.Conn, content string) {
	n.mu.Lock()
	client, ok := n.clients[conn]
	n.mu.Unlock()
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

		// Set channel from client's active channel
		n.mu.Lock()
		client := n.clients[conn]
		n.mu.Unlock()
		if msg.Channel == "" {
			msg.Channel = client.Channel
			if msg.Channel == "" {
				msg.Channel = "general"
			}
		}

		n.broadcast <- *msg
	}
}

func (n *Node) handleCommand(conn net.Conn, msg *protocol.Message, content string) {
	parts := strings.Fields(content)
	cmd := parts[0]

	n.mu.Lock()
	client := n.clients[conn]
	username := client.User
	n.mu.Unlock()

	switch cmd {

	// ---- Active Users ----
	case "/active", "/users":
		n.mu.Lock()
		var users []string
		for _, c := range n.clients {
			status := n.auth.GetStatus(c.User)
			if status == "" {
				status = "online"
			}
			users = append(users, fmt.Sprintf("%s (%s)", c.User, status))
		}
		n.mu.Unlock()
		response := fmt.Sprintf("Online Users (%d): %s", len(users), strings.Join(users, ", "))
		n.sendToClient(conn, response)

	// ---- Channel Commands ----
	case "/channel":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /channel <create|join|leave|list|topic|info> [args...]")
			return
		}
		subCmd := parts[1]
		switch subCmd {
		case "create":
			if len(parts) < 3 {
				n.sendToClient(conn, "Usage: /channel create <name> [private]")
				return
			}
			chName := parts[2]
			isPrivate := len(parts) >= 4 && parts[3] == "private"
			if n.channels.Create(chName, username, isPrivate) {
				pType := "public"
				if isPrivate {
					pType = "private"
				}
				n.sendToClient(conn, fmt.Sprintf("✅ Channel #%s created (%s)", chName, pType))
			} else {
				n.sendToClient(conn, fmt.Sprintf("❌ Channel #%s already exists", chName))
			}
		case "join":
			if len(parts) < 3 {
				n.sendToClient(conn, "Usage: /channel join <name>")
				return
			}
			chName := parts[2]
			if !n.channels.Exists(chName) {
				n.sendToClient(conn, fmt.Sprintf("❌ Channel #%s does not exist", chName))
				return
			}
			if n.channels.Join(chName, username) {
				// Switch active channel
				n.mu.Lock()
				c := n.clients[conn]
				c.Channel = chName
				n.clients[conn] = c
				n.mu.Unlock()
				n.sendToClient(conn, fmt.Sprintf("✅ Joined #%s (now your active channel)", chName))
			}
		case "leave":
			if len(parts) < 3 {
				n.sendToClient(conn, "Usage: /channel leave <name>")
				return
			}
			chName := parts[2]
			n.channels.Leave(chName, username)
			// If leaving active channel, switch back to general
			n.mu.Lock()
			c := n.clients[conn]
			if c.Channel == chName {
				c.Channel = "general"
				n.clients[conn] = c
			}
			n.mu.Unlock()
			n.sendToClient(conn, fmt.Sprintf("Left #%s. Active channel: general", chName))
		case "switch":
			if len(parts) < 3 {
				n.sendToClient(conn, "Usage: /channel switch <name>")
				return
			}
			chName := parts[2]
			if !n.channels.IsMember(chName, username) {
				n.sendToClient(conn, fmt.Sprintf("❌ You are not a member of #%s. Join first.", chName))
				return
			}
			n.mu.Lock()
			c := n.clients[conn]
			c.Channel = chName
			n.clients[conn] = c
			n.mu.Unlock()
			n.sendToClient(conn, fmt.Sprintf("Switched to #%s", chName))
		case "list":
			channels := n.channels.List(false)
			var lines []string
			for _, ch := range channels {
				pType := "public"
				if ch.IsPrivate {
					pType = "private"
				}
				lines = append(lines, fmt.Sprintf("  #%s (%s) - %s [%d members]", ch.Name, pType, ch.Topic, len(ch.Members)))
			}
			if len(lines) == 0 {
				n.sendToClient(conn, "No channels found.")
			} else {
				n.sendToClient(conn, "📋 Channels:\n"+strings.Join(lines, "\n"))
			}
		case "topic":
			if len(parts) < 4 {
				n.sendToClient(conn, "Usage: /channel topic <name> <topic text>")
				return
			}
			chName := parts[2]
			topic := strings.Join(parts[3:], " ")
			if n.channels.SetTopic(chName, topic) {
				n.sendToClient(conn, fmt.Sprintf("Topic of #%s set to: %s", chName, topic))
			} else {
				n.sendToClient(conn, fmt.Sprintf("❌ Channel #%s not found", chName))
			}
		case "info":
			if len(parts) < 3 {
				n.sendToClient(conn, "Usage: /channel info <name>")
				return
			}
			ch := n.channels.Get(parts[2])
			if ch == nil {
				n.sendToClient(conn, fmt.Sprintf("❌ Channel #%s not found", parts[2]))
				return
			}
			var members []string
			for u, r := range ch.Members {
				members = append(members, fmt.Sprintf("%s(%s)", u, r))
			}
			info := fmt.Sprintf("#%s | Topic: %s | Members: %s | Created by %s", ch.Name, ch.Topic, strings.Join(members, ", "), ch.CreatedBy)
			n.sendToClient(conn, info)
		default:
			n.sendToClient(conn, "Unknown channel command. Use: create, join, leave, switch, list, topic, info")
		}

	// ---- Direct Message ----
	case "/dm":
		if len(parts) < 3 {
			n.sendToClient(conn, "Usage: /dm <username> <message>")
			return
		}
		recipient := parts[1]
		dmContent := strings.Join(parts[2:], " ")

		dmMsg := protocol.Message{
			Type:      protocol.MsgTypeDirectMessage,
			ID:        protocol.GenerateID(),
			Sender:    username,
			Recipient: recipient,
			Content:   dmContent,
			Timestamp: time.Now(),
		}
		n.broadcast <- dmMsg

	// ---- Edit Message ----
	case "/edit":
		if len(parts) < 3 {
			n.sendToClient(conn, "Usage: /edit <message_id> <new text>")
			return
		}
		msgID := parts[1]
		newText := strings.Join(parts[2:], " ")

		existing := n.storage.GetMessageByID(msgID)
		if existing == nil {
			n.sendToClient(conn, "❌ Message not found")
			return
		}
		if existing.Sender != username {
			n.sendToClient(conn, "❌ You can only edit your own messages")
			return
		}

		n.storage.UpdateMessage(msgID, newText)
		editMsg := protocol.Message{
			Type:      protocol.MsgTypeEditMessage,
			ID:        msgID,
			Sender:    username,
			Content:   newText,
			Channel:   existing.Channel,
			Edited:    true,
			Timestamp: time.Now(),
		}
		n.broadcast <- editMsg

	// ---- Delete Message ----
	case "/delete":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /delete <message_id>")
			return
		}
		msgID := parts[1]

		existing := n.storage.GetMessageByID(msgID)
		if existing == nil {
			n.sendToClient(conn, "❌ Message not found")
			return
		}
		if existing.Sender != username {
			role := n.auth.GetRole(username)
			if role != "admin" {
				n.sendToClient(conn, "❌ You can only delete your own messages (or be admin)")
				return
			}
		}

		n.storage.DeleteMessage(msgID)
		delMsg := protocol.Message{
			Type:      protocol.MsgTypeDeleteMessage,
			ID:        msgID,
			Sender:    username,
			Content:   "Message deleted",
			Channel:   existing.Channel,
			Timestamp: time.Now(),
		}
		n.broadcast <- delMsg

	// ---- Reaction ----
	case "/react":
		if len(parts) < 3 {
			n.sendToClient(conn, "Usage: /react <message_id> <emoji>")
			return
		}
		msgID := parts[1]
		emoji := parts[2]

		if n.storage.AddReaction(msgID, emoji, username) {
			reactMsg := protocol.Message{
				Type:      protocol.MsgTypeReaction,
				ID:        msgID,
				Sender:    username,
				Content:   emoji,
				Timestamp: time.Now(),
				Metadata:  map[string]string{"emoji": emoji, "target_id": msgID},
			}
			n.broadcast <- reactMsg
		} else {
			n.sendToClient(conn, "❌ Message not found")
		}

	// ---- Reply ----
	case "/reply":
		if len(parts) < 3 {
			n.sendToClient(conn, "Usage: /reply <message_id> <text>")
			return
		}
		replyTo := parts[1]
		replyContent := strings.Join(parts[2:], " ")

		replyMsg := *msg
		replyMsg.Type = protocol.MsgTypeChat
		replyMsg.ReplyTo = replyTo
		replyMsg.Content = replyContent
		replyMsg.Channel = client.Channel
		if replyMsg.Channel == "" {
			replyMsg.Channel = "general"
		}

		n.broadcast <- replyMsg

	// ---- Status ----
	case "/status":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /status <online|away|dnd|offline>")
			return
		}
		status := parts[1]
		validStatuses := map[string]bool{"online": true, "away": true, "dnd": true, "offline": true, "idle": true}
		if !validStatuses[status] {
			n.sendToClient(conn, "Invalid status. Use: online, away, dnd, idle, offline")
			return
		}
		n.auth.SetStatus(username, status)
		n.sendToClient(conn, fmt.Sprintf("Status set to: %s", status))

		// Broadcast status change
		statusMsg := protocol.Message{
			Type:      protocol.MsgTypeStatusChange,
			ID:        protocol.GenerateID(),
			Sender:    username,
			Content:   status,
			Timestamp: time.Now(),
		}
		n.broadcastToClients(statusMsg)

	// ---- Search ----
	case "/search":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /search <query>")
			return
		}
		query := strings.Join(parts[1:], " ")
		results := n.storage.SearchMessages(query)
		if len(results) == 0 {
			n.sendToClient(conn, "No results found.")
			return
		}
		var lines []string
		for _, r := range results {
			lines = append(lines, fmt.Sprintf("  [%s] [%s] %s: %s", r.ID[:8], r.Timestamp.Format(time.Kitchen), r.Sender, r.Content))
		}
		n.sendToClient(conn, fmt.Sprintf("🔍 Search results (%d):\n%s", len(results), strings.Join(lines, "\n")))

	// ---- History ----
	case "/history":
		chName := client.Channel
		if len(parts) >= 2 {
			chName = parts[1]
		}
		if chName == "" {
			chName = "general"
		}
		msgs, total := n.storage.GetChannelMessages(chName, 50, 0)
		if len(msgs) == 0 {
			n.sendToClient(conn, fmt.Sprintf("No history in #%s", chName))
			return
		}
		var lines []string
		for _, m := range msgs {
			editTag := ""
			if m.Edited {
				editTag = " (edited)"
			}
			lines = append(lines, fmt.Sprintf("  [%s] [%s] %s: %s%s", m.ID[:8], m.Timestamp.Format(time.Kitchen), m.Sender, m.Content, editTag))
		}
		n.sendToClient(conn, fmt.Sprintf("📜 History #%s (%d/%d):\n%s", chName, len(msgs), total, strings.Join(lines, "\n")))

	// ---- Typing Indicator ----
	case "/typing":
		typingMsg := protocol.Message{
			Type:      protocol.MsgTypeTyping,
			ID:        protocol.GenerateID(),
			Sender:    username,
			Channel:   client.Channel,
			Timestamp: time.Now(),
		}
		n.broadcastToClients(typingMsg)

	// ---- Health ----
	case "/health", "/nodes":
		info := n.GetNodeInfo()
		infoJSON, _ := json.MarshalIndent(info, "", "  ")
		n.sendToClient(conn, fmt.Sprintf("🏥 Node Health:\n%s", string(infoJSON)))

	// ---- Ban/Mute (Admin) ----
	case "/ban":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /ban <username>")
			return
		}
		role := n.auth.GetRole(username)
		if role != "admin" {
			n.sendToClient(conn, "❌ Admin only")
			return
		}
		target := parts[1]
		n.auth.SetStatus(target, "banned")
		n.sendToClient(conn, fmt.Sprintf("🔨 %s has been banned", target))

		// Disconnect the banned user
		n.mu.Lock()
		for c, cl := range n.clients {
			if cl.User == target {
				n.sendToClient(c, "You have been banned from the server.")
				go func(dc net.Conn) { n.unregister <- dc }(c)
				break
			}
		}
		n.mu.Unlock()

	case "/mute":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /mute <username>")
			return
		}
		role := n.auth.GetRole(username)
		if role != "admin" {
			n.sendToClient(conn, "❌ Admin only")
			return
		}
		target := parts[1]
		n.auth.SetStatus(target, "muted")
		n.sendToClient(conn, fmt.Sprintf("🔇 %s has been muted", target))

	case "/unmute":
		if len(parts) < 2 {
			n.sendToClient(conn, "Usage: /unmute <username>")
			return
		}
		role := n.auth.GetRole(username)
		if role != "admin" {
			n.sendToClient(conn, "❌ Admin only")
			return
		}
		target := parts[1]
		n.auth.SetStatus(target, "online")
		n.sendToClient(conn, fmt.Sprintf("🔊 %s has been unmuted", target))

	// --- Help ---
	case "/help":
		help := `📖 Available Commands:
  /active, /users       — List online users
  /channel create|join|leave|switch|list|topic|info
  /dm <user> <msg>      — Direct message
  /edit <id> <text>     — Edit your message
  /delete <id>          — Delete a message
  /react <id> <emoji>   — React to a message
  /reply <id> <text>    — Reply to a message
  /status <status>      — Set status (online/away/dnd/idle)
  /search <query>       — Search messages
  /history [channel]    — View channel history
  /health, /nodes       — Node health info
  /ban <user>           — Ban user (admin)
  /mute <user>          — Mute user (admin)
  /unmute <user>        — Unmute user (admin)
  /clear                — Clear terminal
  /help                 — Show this help`
		n.sendToClient(conn, help)

	case "/clear":
		// Handled client-side
		return

	default:
		n.sendToClient(conn, fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd))
	}
}

func (n *Node) Shutdown() {
	n.mu.Lock()
	defer n.mu.Unlock()

	log.Println("Shutting down node...")

	if n.listener != nil {
		n.listener.Close()
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
