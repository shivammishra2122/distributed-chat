# 📡 Distributed Chat System

![Go Version](https://img.shields.io/github/go-mod/go-version/shivammishra2122/distributed-chat)
![License](https://img.shields.io/badge/license-MIT-blue)
![Build Status](https://img.shields.io/github/actions/workflow/status/shivammishra2122/distributed-chat/codeql.yml?label=CodeQL)

A high-performance, fault-tolerant, and secure distributed chat application built in Go. Featuring in-memory synchronization, auto-snapshot persistence, and mesh networking.

---

## 🚀 Features

-   **⚡ In-Memory Sync**: Uses a high-speed Ring Buffer for instant message availability across the cluster.
-   **💾 Auto-Snapshotting**: Periodically (every 10s) saves state to disk, ensuring durability without sacrificing speed.
-   **🔒 Secure Encryption**:
    -   End-to-End Encryption using **AES-GCM**.
    -   Secure Key Derivation with **Argon2id** (Salt + Key stretching).
-   **🌐 Mesh Networking**: Decentralized architecture where nodes sync seamlessly.
-   **🖥️ Multi-Interface**:
    -   **CLI Client**: Native terminal experience.
    -   **SSH Access**: Connect via any standard SSH client (e.g., `ssh -p 2222 localhost`).

---

## 🛠️ Installation & Quick Start

### Prerequisites
-   Go 1.25+
-   Make (optional)

### One-Click Cluster (Recommended)
You can spin up a local 3-node cluster instantly:

```bash
./start_cluster.sh
```
This will:
1.  Build the project.
2.  Start 3 interconnected nodes (`:8080`, `:8081`, `:8082`).
3.  Ready for clients!

### Manual Build
```bash
make build
# Binaries will be in ./bin/
```

---

## 📖 Usage

### Connecting a Client
Open a new terminal and connect to any running node:

```bash
./bin/client -server localhost:8080 -user Alice
```

### connecting via SSH
If you don't want to install the client binary:

```bash
ssh -p 2222 localhost
```
*(Note: Port depends on the node configuration, default cluster nodes use 2222, 2223, 2224)*

---

## 🔒 Security

This project implements **Argon2id** for Password-Based Key Derivation (PBKDF2 replacement).
-   **Salt**: 16-byte random salt generated per encryption.
-   **Memory**: 64KB
-   **Iterations**: 1
-   **Parallelism**: 4 threads

---

## 🤝 Contributing

1.  Fork the repository.
2.  Create a feature branch.
3.  Commit your changes.
4.  Open a Pull Request.

**Note**: This project uses **CodeRabbit** for AI-driven code reviews. Your PRs will be automatically analyzed.

---

## 📜 License

MIT License. See [LICENSE](LICENSE) file.
