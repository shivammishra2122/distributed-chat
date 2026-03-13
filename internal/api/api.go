package api

import (
	"distributed-chat/internal/node"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// Server provides a REST API for the chat node
type Server struct {
	node *node.Node
	port int
}

// NewServer creates a new API server
func NewServer(n *node.Node, port int) *Server {
	return &Server{node: n, port: port}
}

// Start begins listening on the configured port
func (s *Server) Start() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/message", s.handleMessage)
	mux.HandleFunc("/api/channels", s.handleChannels)
	mux.HandleFunc("/api/channels/", s.handleChannelHistory) // /api/channels/{name}/history
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/search", s.handleSearch)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("🌐 REST API listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// POST /api/message
// Body: {"content": "hello", "sender": "bot", "channel": "general"}
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Content   string `json:"content"`
		Sender    string `json:"sender"`
		Channel   string `json:"channel"`
		Recipient string `json:"recipient,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Content == "" || req.Sender == "" {
		http.Error(w, "content and sender are required", http.StatusBadRequest)
		return
	}

	msgType := protocol.MsgTypeChat
	if req.Recipient != "" {
		msgType = protocol.MsgTypeDirectMessage
	}

	msg := protocol.Message{
		Type:      msgType,
		ID:        protocol.GenerateID(),
		Sender:    req.Sender,
		Content:   req.Content,
		Channel:   req.Channel,
		Recipient: req.Recipient,
		Timestamp: time.Now(),
	}

	s.node.InjectMessage(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "sent",
		"message_id": msg.ID,
	})
}

// GET /api/channels
func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	channels := s.node.GetChannelManager().List(false)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(channels)
}

// GET /api/channels/{name}/history?limit=50&offset=0
func (s *Server) handleChannelHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse channel name from path: /api/channels/{name}/history
	path := strings.TrimPrefix(r.URL.Path, "/api/channels/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.Error(w, "Channel name required", http.StatusBadRequest)
		return
	}
	channelName := parts[0]

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		fmt.Sscanf(o, "%d", &offset)
	}

	msgs, total := s.node.GetStorage().GetChannelMessages(channelName, limit, offset)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"channel":  channelName,
		"messages": msgs,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}

// GET /api/users
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	onlineUsers := s.node.GetOnlineUsers()
	profiles := s.node.GetAuth().GetAllProfiles()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"online":   onlineUsers,
		"profiles": profiles,
	})
}

// GET /api/health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := s.node.GetNodeInfo()
	info["status"] = "healthy"
	info["timestamp"] = time.Now()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// GET /api/search?q=query
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}

	results := s.node.GetStorage().SearchMessages(query)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"query":   query,
		"results": results,
		"count":   len(results),
	})
}
