package node

import (
	"bufio"
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/protocol"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/gliderlabs/ssh"
)

// JoinSSH handles a raw SSH session by bridging it to the node
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

// NewSSHAdapter creates an SSH-to-JSON protocol bridge
func NewSSHAdapter(s ssh.Session, key string) *SSHAdapterPipe {
	r, w := io.Pipe()
	return &SSHAdapterPipe{
		Session: s,
		pr:      r,
		pw:      w,
		key:     key,
	}
}

// SSHAdapterPipe bridges SSH sessions (raw text) to the node's JSON protocol
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
			// Safe truncation to prevent panic on short content
			preview := msg.Content
			if len(preview) > 10 {
				preview = preview[:10]
			}
			content = fmt.Sprintf("[Encrypted blob: %s...]", preview)
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
