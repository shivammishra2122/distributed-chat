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

	// ChannelsTotal tracks number of channels
	ChannelsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "chat_channels_total",
		Help: "The total number of chat channels",
	})

	// DirectMessagesTotal tracks DMs sent
	DirectMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chat_direct_messages_total",
		Help: "The total number of direct messages sent",
	})

	// APIRequestsTotal tracks REST API requests
	APIRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "chat_api_requests_total",
		Help: "The total number of REST API requests",
	})
)
