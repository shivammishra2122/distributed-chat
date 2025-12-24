package main

import (
	"distributed-chat/internal/protocol"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

var (
	serverAddr = flag.String("server", "localhost:8080", "Server address")
	count      = flag.Int("clients", 100, "Number of concurrent clients")
)

func main() {
	flag.Parse()
	fmt.Printf("Starting stress test with %d clients against %s...\n", *count, *serverAddr)

	var wg sync.WaitGroup
	wg.Add(*count)

	for i := 0; i < *count; i++ {
		go func(id int) {
			defer wg.Done()
			runClient(id)
		}(i)
		time.Sleep(10 * time.Millisecond) // Stagger connections
	}

	wg.Wait()
	fmt.Println("Stress test completed.")
}

func runClient(id int) {
	conn, err := net.Dial("tcp", *serverAddr)
	if err != nil {
		log.Printf("[Client %d] Connection failed: %v", id, err)
		return
	}
	defer conn.Close()

	// Login
	username := fmt.Sprintf("Bot-%d", id)

	// For simplicity, let's assume we use the 'admin' user just to spam messages?
	// Or we just send MsgTypeChat immediately?
	// The server requires Auth now.
	// So we must Login.
	// Let's use "Bot" : "pass" and assume we registered them or just use "Alice" for all?
	// Using "Alice" for everyone is fine for load testing, but might confuse presence.
	// Let's Register on fly.

	registerMsg := protocol.Message{
		Type:    protocol.MsgTypeRegister,
		Sender:  username,
		Content: fmt.Sprintf("%s:pass", username),
	}
	protocol.SendMessage(conn, registerMsg)

	// Consume response
	decoder := protocol.NewDecoder(conn)
	decoder.Decode() // READ: OK_REGISTERED or FAIL_EXISTS

	// Now Login
	login := protocol.Message{
		Type:    protocol.MsgTypeLogin,
		Sender:  username,
		Content: fmt.Sprintf("%s:pass", username),
	}
	protocol.SendMessage(conn, login)
	decoder.Decode() // READ: OK

	// Start Chat loop
	go func() {
		// Read loop to drain buffer
		for {
			_, err := decoder.Decode()
			if err != nil {
				return
			}
		}
	}()

	// Send 10 messages
	for k := 0; k < 10; k++ {
		msg := protocol.Message{
			Type:      protocol.MsgTypeChat,
			Sender:    username,
			Content:   fmt.Sprintf("Load Test Message %d from %s", k, username),
			Timestamp: time.Now(),
		}
		if err := protocol.SendMessage(conn, msg); err != nil {
			log.Printf("[Client %d] Send error: %v", id, err)
			return
		}

		sleep := time.Duration(rand.Intn(1000)) * time.Millisecond
		time.Sleep(sleep)
	}
}
