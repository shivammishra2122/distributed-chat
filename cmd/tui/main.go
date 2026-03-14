package main

import (
	"crypto/tls"
	"crypto/x509"
	"distributed-chat/internal/protocol"
	"distributed-chat/internal/tui"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	serverAddr := flag.String("server", "localhost:8080", "Server address to connect to")
	username := flag.String("user", "", "Username for chat")
	password := flag.String("pass", "", "Password for authentication")
	register := flag.Bool("register", false, "Register a new user")
	secretKey := flag.String("key", "", "Secret key for E2EE (optional)")
	flag.Parse()

	if *username == "" {
		fmt.Println("Usage: meshchat -user <username> -pass <password> [-server host:port]")
		os.Exit(1)
	}

	// TLS Setup
	caCert, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatalf("Failed to load CA cert: %v\nRun ./scripts/gen_certs.sh first!", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	conf := &tls.Config{RootCAs: caCertPool}
	conn, err := tls.Dial("tcp", *serverAddr, conf)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	decoder := protocol.NewDecoder(conn)

	// Registration flow
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
		res, err := decoder.Decode()
		if err != nil {
			log.Fatalf("Failed to read response: %v", err)
		}
		if res.Content == "OK_REGISTERED" {
			fmt.Println("✅ Registration successful! Run again without -register to login.")
		} else {
			fmt.Printf("❌ Registration failed: %s\n", res.Content)
		}
		conn.Close()
		return
	}

	// Login
	loginMsg := protocol.Message{
		Type:      protocol.MsgTypeLogin,
		Sender:    *username,
		Content:   fmt.Sprintf("%s:%s", *username, *password),
		Timestamp: time.Now(),
	}
	if err := protocol.SendMessage(conn, loginMsg); err != nil {
		log.Fatalf("Failed to send login: %v", err)
	}

	authRes, err := decoder.Decode()
	if err != nil {
		log.Fatalf("Failed to read auth result: %v", err)
	}
	if authRes.Type == protocol.MsgTypeAuthResult && authRes.Content != "OK" {
		log.Fatalf("❌ Authentication failed: %s", authRes.Content)
	}

	// Send function for the TUI
	sendFunc := func(msg protocol.Message) {
		protocol.SendMessage(conn, msg)
	}

	// Create TUI model
	model := tui.NewModel(*username, *secretKey, sendFunc)

	// Create BubbleTea program
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Background: read server messages and feed to TUI
	go readServerMessages(conn, decoder, p)

	// Run TUI
	if _, err := p.Run(); err != nil {
		log.Fatalf("TUI error: %v", err)
	}

	conn.Close()
}

func readServerMessages(conn net.Conn, decoder *protocol.Decoder, p *tea.Program) {
	for {
		msg, err := decoder.Decode()
		if err != nil {
			p.Send(tui.ErrorMsg{Err: err})
			return
		}
		p.Send(tui.ServerMsg{Msg: *msg})
	}
}
