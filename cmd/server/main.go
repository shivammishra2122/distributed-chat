package main

import (
	"distributed-chat/internal/node"
	sshserver "distributed-chat/internal/ssh"
	"flag"
	"fmt"
	"strings"
)

func main() {
	port := flag.Int("port", 8080, "Port to listen on")
	peers := flag.String("peers", "", "Comma-separated list of peer addresses")
	sshPort := flag.Int("ssh", 2222, "SSH Port to listen on")
	flag.Parse()

	fmt.Printf("Starting Chat Server on port %d...\n", *port)

	// Initialize and run the node
	chatNode := node.NewNode(*port)

	if *peers != "" {
		peerList := strings.Split(*peers, ",")
		chatNode.Join(peerList)
	}

	// Start SSH Server
	go sshserver.StartServer(*sshPort, chatNode)

	chatNode.Run()
}
