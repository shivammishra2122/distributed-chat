package node

import (
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

// handleCommand dispatches slash commands from a client
func (n *Node) handleCommand(conn net.Conn, msg *protocol.Message, content string) {
	parts := strings.Fields(content)
	cmd := parts[0]

	n.mu.RLock()
	client := n.clients[conn]
	username := client.User
	n.mu.RUnlock()

	switch cmd {

	// ---- Active Users ----
	case "/active", "/users":
		n.mu.RLock()
		var users []string
		for _, c := range n.clients {
			status := n.auth.GetStatus(c.User)
			if status == "" {
				status = "online"
			}
			users = append(users, fmt.Sprintf("%s (%s)", c.User, status))
		}
		n.mu.RUnlock()
		response := fmt.Sprintf("Online Users (%d): %s", len(users), strings.Join(users, ", "))
		n.sendToClient(conn, response)

	// ---- Channel Commands ----
	case "/channel":
		n.handleChannelCommand(conn, parts, username)

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
		n.handleAdminBan(conn, parts, username)
	case "/mute":
		n.handleAdminMute(conn, parts, username)
	case "/unmute":
		n.handleAdminUnmute(conn, parts, username)

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

// handleChannelCommand handles /channel subcommands
func (n *Node) handleChannelCommand(conn net.Conn, parts []string, username string) {
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
			n.sendToClient(conn, fmt.Sprintf("❌ Channel #%s already exists or invalid name", chName))
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
}

// handleAdminBan handles the /ban command
func (n *Node) handleAdminBan(conn net.Conn, parts []string, username string) {
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
}

// handleAdminMute handles the /mute command
func (n *Node) handleAdminMute(conn net.Conn, parts []string, username string) {
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
}

// handleAdminUnmute handles the /unmute command
func (n *Node) handleAdminUnmute(conn net.Conn, parts []string, username string) {
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
}
