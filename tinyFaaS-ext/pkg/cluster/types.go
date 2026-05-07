package cluster

import "time"

// Node lifecycle states
type NodeStatus string

const (
	NodeSleeping NodeStatus = "sleeping" // powered off / not serving
	NodeWaking   NodeStatus = "waking"   // power on in progress
	NodeActive   NodeStatus = "active"   // ready to serve requests
	NodeDead     NodeStatus = "dead"     // failed / unusable
)

// Core cluster node representation
type Node struct {
	ID      string `json:"id"`
	Address string `json:"address"` // IP or host of node
	Channel int    `json:"channel"` // Tinkerforge relay channel

	Status NodeStatus `json:"status"`
	Load   int        `json:"load"`

	LastUsed time.Time `json:"last_used"`

	Local bool `json:"local"` // true if this is the current machine
}
