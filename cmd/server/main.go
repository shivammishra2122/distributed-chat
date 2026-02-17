package main

import (
	"crypto/tls"
	"distributed-chat/internal/node"
	sshserver "distributed-chat/internal/ssh"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
)

func main() {
	// 1. Load .env file (if exists)
	if err := godotenv.Load(); err != nil {
		// It's okay if .env doesn't exist (e.g. running in Docker/Cloud with real Env Vars)
		// But if it does, it's loaded.
	}

	// 2. Parse Environment Variables or Defaults
	defaultPort := getEnvInt("PORT", 8080)
	defaultSSH := getEnvInt("SSH_PORT", 2222)
	defaultPeers := os.Getenv("PEERS")
	secretKey := os.Getenv("SECRET_KEY")

	// 3. Flags Override Environment
	port := flag.Int("port", defaultPort, "Port to listen on")
	peers := flag.String("peers", defaultPeers, "Comma-separated list of peer addresses")
	sshPort := flag.Int("ssh", defaultSSH, "SSH Port to listen on")
	flag.Parse()

	fmt.Printf("Starting Chat Server on port %d...\n", *port)

	// Load TLS Certificates
	cert, err := tls.LoadX509KeyPair("server.crt", "server.key")
	if err != nil {
		log.Fatalf("Failed to load TLS keys: %v\nRun ./scripts/gen_certs.sh first!", err)
	}

	tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}

	// Initialize and run the node
	chatNode := node.NewNode(*port, tlsConfig)

	// Set E2EE Secret Key if provided
	if secretKey != "" {
		chatNode.SetSSHKey(secretKey)
		fmt.Println("🔒 E2EE Enabled with provided Secret Key")
	} else {
		fmt.Println("⚠️  Warning: No SECRET_KEY provided. Messages will be plaintext!")
	}

	// Peers
	if *peers != "" {
		peerList := strings.Split(*peers, ",")
		chatNode.Join(peerList)
	}

	// Start SSH Server
	go sshserver.StartServer(*sshPort, chatNode)

	// Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		chatNode.Run()
	}()

	<-stop
	fmt.Println("\nReceived interrupt, shutting down...")
	chatNode.Shutdown()
	fmt.Println("Graceful shutdown complete.")
}

func getEnvInt(key string, fallback int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return fallback
	}
	if val, err := strconv.Atoi(valueStr); err == nil {
		return val
	}
	return fallback
}
