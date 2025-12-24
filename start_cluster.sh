#!/bin/bash

# Kill any running servers
pkill server

# Build
echo "Building..."
make build

# Start Node 1
echo "Starting Node 1 on :8080 (SSH :2222)"
./bin/server -port 8080 -ssh 2222 &
PID1=$!
sleep 1

# Start Node 2 (Connects to Node 1)
echo "Starting Node 2 on :8081 (SSH :2223, peer: :8080)"
./bin/server -port 8081 -ssh 2223 -peers localhost:8080 &
PID2=$!
sleep 1

# Start Node 3 (Connects to Node 1 and Node 2 for Full Mesh - simplified config just connecting to known peers)
# Note: In a real Full Mesh, Node 3 should connect to everyone. 
# Here we manually specify both or rely on discovery (which we don't have yet).
# Let's connect to both to ensure full mesh manually.
echo "Starting Node 3 on :8082 (SSH :2224, peers: :8080,:8081)"
./bin/server -port 8082 -ssh 2224 -peers localhost:8080,localhost:8081 &
PID3=$!
sleep 1

echo "Cluster started!"
echo "To test:"
echo "1. Open a new terminal and run: ./bin/client -server localhost:8080 -user Alice"
echo "2. Open another terminal and run: ./bin/client -server localhost:8082 -user Bob"
echo "3. Send messages and verify sync."
echo ""
echo "Press Enter to kill cluster..."
read
kill $PID1 $PID2 $PID3
