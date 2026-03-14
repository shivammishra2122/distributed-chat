package main

import (
	"crypto/tls"
	"crypto/x509"
	"distributed-chat/internal/protocol"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
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

	// Load CA cert for TLS
	caCert, err := os.ReadFile("ca.crt")
	if err != nil {
		log.Fatalf("Failed to load CA cert: %v\nRun ./scripts/gen_certs.sh first!", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	var wg sync.WaitGroup
	wg.Add(*count)

	for i := 0; i < *count; i++ {
		go func(id int) {
			defer wg.Done()
			runClient(id, caCertPool)
		}(i)
		time.Sleep(10 * time.Millisecond) // Stagger connections
	}

	wg.Wait()
	fmt.Println("Stress test completed.")
}

func runClient(id int, caPool *x509.CertPool) {
	conf := &tls.Config{RootCAs: caPool}
	conn, err := tls.Dial("tcp", *serverAddr, conf)
	if err != nil {
		log.Printf("[Client %d] Connection failed: %v", id, err)
		return
	}
	defer conn.Close()

	username := fmt.Sprintf("Bot-%d", id)

	// Register
	registerMsg := protocol.Message{
		Type:      protocol.MsgTypeRegister,
		Sender:    username,
		Content:   fmt.Sprintf("%s:pass1234", username),
		Timestamp: time.Now(),
	}
	protocol.SendMessage(conn, registerMsg)

	decoder := protocol.NewDecoder(conn)
	decoder.Decode() // READ: OK_REGISTERED or FAIL_EXISTS

	// Login
	login := protocol.Message{
		Type:      protocol.MsgTypeLogin,
		Sender:    username,
		Content:   fmt.Sprintf("%s:pass1234", username),
		Timestamp: time.Now(),
	}
	protocol.SendMessage(conn, login)
	decoder.Decode() // READ: OK

	// Read loop to drain incoming messages
	go func() {
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
			Channel:   "general",
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
