package main

import (
	"bufio"
	"distributed-chat/internal/crypto"
	"distributed-chat/internal/protocol"
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	serverAddr := flag.String("server", "localhost:8080", "Server address to connect to")
	username := flag.String("user", "User", "Username for chat")
	password := flag.String("pass", "", "Password for authentication")
	secretKey := flag.String("key", "", "Secret key for E2EE (optional)")
	flag.Parse()

	conn, err := net.Dial("tcp", *serverAddr)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()
	fmt.Printf("Connected to server at %s as %s\n", *serverAddr, *username)

	// Start a goroutine to read messages from the server
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
	authRes, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Fatalf("Failed to read auth result: %v", err)
	}
	if authRes.Type == protocol.MsgTypeAuthResult {
		if authRes.Content != "OK" {
			log.Fatalf("Authentication Failed: %s", authRes.Content)
		}
		fmt.Println("Authentication Successful!")
	} else {
		log.Printf("Warning: Expected AuthResult, got %d", authRes.Type)
	}

	go func() {
		for {
			msg, err := protocol.ReadMessage(conn)
			if err != nil {
				log.Printf("Disconnected from server: %v", err)
				os.Exit(0)
			}

			content := msg.Content
			// Decrypt if key provided and it's a chat message
			if *secretKey != "" && msg.Type == protocol.MsgTypeChat {
				decrypted, err := crypto.Decrypt(msg.Content, *secretKey)
				if err == nil {
					content = decrypted + " [E2EE]"
				} else {
					content = fmt.Sprintf("[Encrypted - Failed to decrypt: %v]", err)
				}
			} else if msg.Type == protocol.MsgTypeImage {
				// Handle Image
				// Decrypt if needed (if E2EE is on, key != "" AND msg was encrypted)
				// Note: E2EE logic below just assigns `content`.
				// If E2EE + Image, we might need to decrypt Base64 logic?
				// Complex: Encrypting huge Blob?
				// Let's assume Images are NOT encrypted for this iteration or use same logic.
				// If `*secretKey` is set, `Start connection` logic will try to decrypt `msg.Content`.
				// Yes, the existing logic `if *secretKey != "" ...` applies to `MsgTypeChat`.
				// We should extend it to `MsgTypeImage`.

				imgContent := msg.Content
				if *secretKey != "" {
					decrypted, err := crypto.Decrypt(msg.Content, *secretKey)
					if err == nil {
						imgContent = decrypted
					} else {
						fmt.Printf("[Image] Failed to decrypt image data\n")
						continue
					}
				}

				// Decode Base64
				data, err := base64.StdEncoding.DecodeString(imgContent)
				if err != nil {
					fmt.Printf("[Image] Failed to decode base64: %v\n", err)
					continue
				}

				// Save to file
				filename := fmt.Sprintf("image_%d_%s.png", time.Now().Unix(), msg.Sender)
				path := filepath.Join("downloads", filename)
				if err := ioutil.WriteFile(path, data, 0644); err != nil {
					fmt.Printf("[Image] Failed to save file: %v\n", err)
				} else {
					fmt.Printf("[%s] %s sent an image: Saved to %s\n", msg.Timestamp.Format(time.Kitchen), msg.Sender, path)
				}
				continue
			}

			fmt.Printf("[%s] %s: %s\n", msg.Timestamp.Format(time.Kitchen), msg.Sender, content)
		}
	}()

	// Read from stdin and send messages
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()

		// Check for commands
		if strings.HasPrefix(text, "/image ") {
			path := strings.TrimPrefix(text, "/image ")
			data, err := ioutil.ReadFile(path)
			if err != nil {
				fmt.Printf("Error reading file: %v\n", err)
				continue
			}

			encoded := base64.StdEncoding.EncodeToString(data)

			// Encrypt if needed
			finalContent := encoded
			if *secretKey != "" {
				encrypted, err := crypto.Encrypt(encoded, *secretKey)
				if err != nil {
					fmt.Printf("Error encrypting image: %v\n", err)
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
				fmt.Printf("Error sending image: %v\n", err)
			} else {
				fmt.Printf("Image sent: %s\n", path)
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
