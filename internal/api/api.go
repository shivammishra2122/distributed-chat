package api

import (
	"crypto/subtle"
	"distributed-chat/internal/node"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Server provides an authenticated REST API for the chat node
type Server struct {
	node    *node.Node
	port    int
	apiKey  string
	certFile string
	keyFile  string
}

// NewServer creates a new API server. Reads API_KEY from env or generates one.
// certFile/keyFile are optional — if provided, the API uses HTTPS.
func NewServer(n *node.Node, port int, certFile, keyFile string) *Server {
	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		apiKey = protocol.GenerateID() + protocol.GenerateID() // 32 hex chars
		log.Printf("⚠️  No API_KEY set. Generated ephemeral key: %s... (redacted)", apiKey[:4])
		log.Printf("   Set API_KEY env var for persistent auth. Full key printed to stderr once.")
		fmt.Fprintf(os.Stderr, "API_KEY=%s\n", apiKey)
	}

	return &Server{node: n, port: port, apiKey: apiKey, certFile: certFile, keyFile: keyFile}
}

// authMiddleware validates the API key from the Authorization header
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Allow health check without auth
		if r.URL.Path == "/api/health" && r.Method == http.MethodGet {
			next(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" {
			auth = r.URL.Query().Get("api_key")
		}

		// Constant-time comparison to prevent timing attacks
		bearerKey := "Bearer " + s.apiKey
		bearerMatch := subtle.ConstantTimeCompare([]byte(auth), []byte(bearerKey)) == 1
		directMatch := subtle.ConstantTimeCompare([]byte(auth), []byte(s.apiKey)) == 1

		if !bearerMatch && !directMatch {
			http.Error(w, `{"error":"unauthorized","hint":"Set Authorization: Bearer <API_KEY> header"}`, http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// Start begins listening on the configured port with timeouts
func (s *Server) Start() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/message", s.authMiddleware(s.handleMessage))
	mux.HandleFunc("/api/channels", s.authMiddleware(s.handleChannels))
	mux.HandleFunc("/api/channels/", s.authMiddleware(s.handleChannelHistory))
	mux.HandleFunc("/api/users", s.authMiddleware(s.handleUsers))
	mux.HandleFunc("/api/health", s.handleHealth) // No auth required
	mux.HandleFunc("/api/search", s.authMiddleware(s.handleSearch))

	addr := fmt.Sprintf(":%d", s.port)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Use HTTPS if TLS certs are available
	if s.certFile != "" && s.keyFile != "" {
		log.Printf("🔒 REST API listening on %s (HTTPS)", addr)
		log.Fatal(server.ListenAndServeTLS(s.certFile, s.keyFile))
	} else {
		log.Printf("🌐 REST API listening on %s (HTTP — set certs for HTTPS)", addr)
		log.Fatal(server.ListenAndServe())
	}
}

// POST /api/message
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, protocol.MaxMessageSize)

	var req struct {
		Content   string `json:"content"`
		Sender    string `json:"sender"`
		Channel   string `json:"channel"`
		Recipient string `json:"recipient,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON or body too large", http.StatusBadRequest)
		return
	}

	if req.Content == "" {
		http.Error(w, `{"error":"content is required"}`, http.StatusBadRequest)
		return
	}

	// Validate sender: must be a registered user, or tag as api-bot
	if req.Sender == "" {
		req.Sender = "api-bot"
	} else {
		// Verify sender is a real user
		if s.node.GetAuth().GetRole(req.Sender) == "" {
			req.Sender = "api-bot" // Don't allow impersonation of non-existent users either
		}
	}

	if len(req.Content) > protocol.MaxMessageSize {
		http.Error(w, `{"error":"content exceeds maximum size"}`, http.StatusRequestEntityTooLarge)
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
	w.WriteHeader(http.StatusCreated)
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
	// Cap limit
	if limit > 200 {
		limit = 200
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

// GET /api/health (no auth required)
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
		http.Error(w, `{"error":"query parameter 'q' is required"}`, http.StatusBadRequest)
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
