package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ConnectedClients tracks the number of currently connected chat clients
	ConnectedClients = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chat_connected_clients",
		Help: "The total number of currently connected chat clients",
	})

	// MessagesTotal tracks the total number of messages processed by the node
	MessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chat_messages_total",
		Help: "The total number of processed chat messages",
	})

	// ActivePeers tracks the number of connected peer nodes
	ActivePeers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chat_active_peers",
		Help: "The number of active peer connections in the mesh",
	})
)
