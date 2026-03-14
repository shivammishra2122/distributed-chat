package node

import (
	"crypto/tls"
	"distributed-chat/internal/metrics"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// Join connects to peer nodes
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

// broadcastToPeers sends a message to all connected peer nodes
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

// startHeartbeat sends periodic heartbeats to all peers
func (n *Node) startHeartbeat() {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			heartbeat := protocol.Message{Type: protocol.MsgTypeHeartbeat, Sender: "Node", Timestamp: time.Now()}
			n.broadcastToPeers(heartbeat)
		case <-n.ctx.Done():
			return
		}
	}
}

// startMonitor checks for timed-out peer connections
func (n *Node) startMonitor() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
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
		case <-n.ctx.Done():
			return
		}
	}
}

// startElectionLoop runs a simple bully-style leader election
func (n *Node) startElectionLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
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
		case <-n.ctx.Done():
			return
		}
	}
}

// handlePeerMessage processes messages received from peer nodes
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
