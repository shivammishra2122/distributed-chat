package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/protocol"
	"encoding/base64"
	"flag"
	"fmt"

	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ANSI color codes
const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorGray    = "\033[90m"
	colorBold    = "\033[1m"
)

func main() {
	serverAddr := flag.String("server", "localhost:8080", "Server address to connect to")
	username := flag.String("user", "User", "Username for chat")
	password := flag.String("pass", "", "Password for authentication")
	register := flag.Bool("register", false, "Register a new user")
	secretKey := flag.String("key", "", "Secret key for E2EE (optional)")
	flag.Parse()

	// TLS Dial
	caCert, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatalf("Failed to load CA cert: %v\nRun ./scripts/gen_certs.sh first!", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	conf := &tls.Config{
		RootCAs: caCertPool,
	}

	conn, err := tls.Dial("tcp", *serverAddr, conf)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()
	fmt.Printf("%s%sConnected to server at %s as %s%s\n", colorBold, colorGreen, *serverAddr, *username, colorReset)

	decoder := protocol.NewDecoder(conn)

	if *register {
		regMsg := protocol.Message{
			Type:      protocol.MsgTypeRegister,
			Sender:    *username,
			Content:   fmt.Sprintf("%s:%s", *username, *password),
			Timestamp: time.Now(),
		}
		if err := protocol.SendMessage(conn, regMsg); err != nil {
			log.Fatalf("Failed to send registration: %v", err)
		}

		fmt.Println("Attempting registration...")
		res, err := decoder.Decode()
		if err != nil {
			log.Fatalf("Failed to read registration response: %v", err)
		}

		if res.Content == "OK_REGISTERED" {
			fmt.Printf("%s✅ Registration Successful! Please restart without -register to login.%s\n", colorGreen, colorReset)
		} else {
			fmt.Printf("%s❌ Registration Failed: %s%s\n", colorRed, res.Content, colorReset)
		}
		return
	}

	// Perform Login
	loginMsg := protocol.Message{
		Type:      protocol.MsgTypeLogin,
		Sender:    *username,
		Content:   fmt.Sprintf("%s:%s", *username, *password),
		Timestamp: time.Now(),
	}
	if err := protocol.SendMessage(conn, loginMsg); err != nil {
		log.Fatalf("Failed to send login: %v", err)
	}

	// Wait for Auth Result
	authRes, err := decoder.Decode()
	if err != nil {
		log.Fatalf("Failed to read auth result: %v", err)
	}
	if authRes.Type == protocol.MsgTypeAuthResult {
		if authRes.Content != "OK" {
			log.Fatalf("%s❌ Authentication Failed: %s%s", colorRed, authRes.Content, colorReset)
		}
		fmt.Printf("%s🔓 Authentication Successful!%s\n", colorGreen, colorReset)
	} else {
		log.Printf("Warning: Expected AuthResult, got %d", authRes.Type)
	}

	fmt.Printf("%sType /help for available commands%s\n\n", colorGray, colorReset)

	go func() {
		for {
			msg, err := decoder.Decode()
			if err != nil {
				log.Printf("%sDisconnected from server: %v%s", colorRed, err, colorReset)
				os.Exit(0)
			}

			switch msg.Type {
			case protocol.MsgTypeChat:
				content := msg.Content
				if *secretKey != "" {
					decrypted, err := crypto.Decrypt(msg.Content, *secretKey)
					if err == nil {
						content = decrypted + fmt.Sprintf(" %s[E2EE]%s", colorGreen, colorReset)
					} else {
						content = fmt.Sprintf("%s[Encrypted]%s", colorYellow, colorReset)
					}
				}

				// Format message ID (short)
				idTag := ""
				if msg.ID != "" {
					short := msg.ID
					if len(short) > 8 {
						short = short[:8]
					}
					idTag = fmt.Sprintf("%s[%s]%s ", colorGray, short, colorReset)
				}

				editTag := ""
				if msg.Edited {
					editTag = fmt.Sprintf(" %s(edited)%s", colorYellow, colorReset)
				}

				replyTag := ""
				if msg.ReplyTo != "" {
					short := msg.ReplyTo
					if len(short) > 8 {
						short = short[:8]
					}
					replyTag = fmt.Sprintf(" %s↩ reply to [%s]%s", colorCyan, short, colorReset)
				}

				chTag := ""
				if msg.Channel != "" && msg.Channel != "general" {
					chTag = fmt.Sprintf(" %s#%s%s", colorMagenta, msg.Channel, colorReset)
				}

				// Highlight mentions
				content = highlightMentions(content, *username)

				// Reactions
				reactTag := ""
				if len(msg.Reactions) > 0 {
					var reacts []string
					for emoji, users := range msg.Reactions {
						reacts = append(reacts, fmt.Sprintf("%s(%d)", emoji, len(users)))
					}
					reactTag = fmt.Sprintf(" %s[%s]%s", colorCyan, strings.Join(reacts, " "), colorReset)
				}

				senderColor := colorBlue
				if msg.Sender == "Server" {
					senderColor = colorYellow
				}

				fmt.Printf("%s%s[%s]%s%s %s%s%s%s: %s%s%s%s\n",
					idTag,
					colorGray, msg.Timestamp.Format(time.Kitchen), colorReset,
					chTag,
					senderColor, msg.Sender, colorReset,
					replyTag,
					content, editTag, reactTag, colorReset)

			case protocol.MsgTypeDirectMessage:
				content := msg.Content
				direction := "→"
				if msg.Sender == *username {
					direction = "←"
				}
				fmt.Printf("%s[DM %s %s]%s %s%s%s: %s\n",
					colorMagenta, direction, msg.Recipient, colorReset,
					colorBold, msg.Sender, colorReset,
					content)

			case protocol.MsgTypeJoin, protocol.MsgTypeLeave:
				color := colorGreen
				if msg.Type == protocol.MsgTypeLeave {
					color = colorRed
				}
				fmt.Printf("%s%s%s\n", color, msg.Content, colorReset)

			case protocol.MsgTypeEditMessage:
				short := msg.ID
				if len(short) > 8 {
					short = short[:8]
				}
				fmt.Printf("%s✏️  Message [%s] edited by %s: %s%s\n",
					colorYellow, short, msg.Sender, msg.Content, colorReset)

			case protocol.MsgTypeDeleteMessage:
				short := msg.ID
				if len(short) > 8 {
					short = short[:8]
				}
				fmt.Printf("%s🗑️  Message [%s] deleted by %s%s\n",
					colorRed, short, msg.Sender, colorReset)

			case protocol.MsgTypeReaction:
				short := msg.ID
				if len(short) > 8 {
					short = short[:8]
				}
				fmt.Printf("%s%s reacted %s to [%s]%s\n",
					colorCyan, msg.Sender, msg.Content, short, colorReset)

			case protocol.MsgTypeTyping:
				if msg.Sender != *username {
					fmt.Printf("%s%s is typing...%s\r", colorGray, msg.Sender, colorReset)
				}

			case protocol.MsgTypeStatusChange:
				fmt.Printf("%s📡 %s is now %s%s\n", colorCyan, msg.Sender, msg.Content, colorReset)

			case protocol.MsgTypeImage:
				imgContent := msg.Content
				if *secretKey != "" {
					decrypted, err := crypto.Decrypt(msg.Content, *secretKey)
					if err == nil {
						imgContent = decrypted
					} else {
						fmt.Printf("%s[Image] Failed to decrypt image data%s\n", colorRed, colorReset)
						continue
					}
				}

				data, err := base64.StdEncoding.DecodeString(imgContent)
				if err != nil {
					fmt.Printf("%s[Image] Failed to decode base64: %v%s\n", colorRed, err, colorReset)
					continue
				}

				filename := fmt.Sprintf("image_%d_%s.png", time.Now().Unix(), msg.Sender)
				path := filepath.Join("downloads", filename)
				os.MkdirAll("downloads", 0755)
				if err := os.WriteFile(path, data, 0644); err != nil {
					fmt.Printf("%s[Image] Failed to save file: %v%s\n", colorRed, err, colorReset)
				} else {
					fmt.Printf("%s📎 [%s] %s sent an image: Saved to %s%s\n",
						colorCyan, msg.Timestamp.Format(time.Kitchen), msg.Sender, path, colorReset)
				}

			default:
				// Silently ignore other message types (heartbeat etc.)
			}
		}
	}()

	// Read from stdin and send messages
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()

		if strings.TrimSpace(text) == "/clear" {
			cmd := exec.Command("clear")
			cmd.Stdout = os.Stdout
			cmd.Run()
			continue
		}

		// Commands that are handled server-side
		if strings.HasPrefix(text, "/") {
			// Pass commands straight to server
			msg := protocol.Message{
				Type:      protocol.MsgTypeChat,
				Sender:    *username,
				Content:   text,
				Timestamp: time.Now(),
			}
			if err := protocol.SendMessage(conn, msg); err != nil {
				log.Printf("%sError sending command: %v%s", colorRed, err, colorReset)
				break
			}
			continue
		}

		// Check for image command
		if strings.HasPrefix(text, "/image ") {
			path := strings.TrimPrefix(text, "/image ")
			data, err := os.ReadFile(path)
			if err != nil {
				fmt.Printf("%sError reading file: %v%s\n", colorRed, err, colorReset)
				continue
			}

			encoded := base64.StdEncoding.EncodeToString(data)

			finalContent := encoded
			if *secretKey != "" {
				encrypted, err := crypto.Encrypt(encoded, *secretKey)
				if err != nil {
					fmt.Printf("%sError encrypting image: %v%s\n", colorRed, err, colorReset)
					continue
				}
				finalContent = encrypted
			}

			msg := protocol.Message{
				Type:      protocol.MsgTypeImage,
				Sender:    *username,
				Content:   finalContent,
				Timestamp: time.Now(),
			}
			if err := protocol.SendMessage(conn, msg); err != nil {
				fmt.Printf("%sError sending image: %v%s\n", colorRed, err, colorReset)
			} else {
				fmt.Printf("%s📤 Image sent: %s%s\n", colorGreen, path, colorReset)
			}
			continue
		}

		finalContent := text
		if *secretKey != "" {
			encrypted, err := crypto.Encrypt(text, *secretKey)
			if err != nil {
				log.Printf("Error encrypting message: %v", err)
				continue
			}
			finalContent = encrypted
		}

		msg := protocol.Message{
			Type:      protocol.MsgTypeChat,
			Sender:    *username,
			Content:   finalContent,
			Timestamp: time.Now(),
		}
		if err := protocol.SendMessage(conn, msg); err != nil {
			log.Printf("Error sending message: %v", err)
			break
		}
	}
}

// highlightMentions highlights @username mentions in content
func highlightMentions(content, currentUser string) string {
	// Highlight @currentUser in bold red
	content = strings.ReplaceAll(content,
		"@"+currentUser,
		fmt.Sprintf("%s%s@%s%s", colorBold, colorRed, currentUser, colorReset))

	return content
}
