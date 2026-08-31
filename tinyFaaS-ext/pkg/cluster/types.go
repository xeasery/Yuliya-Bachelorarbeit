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
	ID             string `json:"id"`
	Address        string `json:"address"`         // rproxy HTTP address (IP:port), used to forward function calls
	ManagerAddress string `json:"manager_address"` // management service address (IP:port), used for health checks and function deployment

	// A cluster larger than two workers needs more than one relay, since an
	// Industrial Dual Relay Bricklet has only two channels. RelayUID names
	// which bricklet switches this node; empty means the default relay, so
	// a single-relay setup needs no per-node UID. Channel is the channel on
	// that bricklet, not a cluster-wide index.
	RelayUID string `json:"relay_uid,omitempty"`
	Channel  int    `json:"channel"`

	Status NodeStatus `json:"status"`
	Load   int        `json:"load"`

	LastUsed time.Time `json:"last_used"`

	// DeadSince is when the node was last marked dead, used to pace recovery
	// attempts. Zero unless Status is NodeDead.
	DeadSince time.Time `json:"dead_since,omitempty"`

	Local bool `json:"local"` // true if this is the current machine

	// DispatchOnly excludes a node from executing functions: it schedules
	// and powers other nodes but never serves a request itself.
	//
	// This is what the leader normally wants. It is never powered down, so
	// if it also executed functions its draw would vary with load and mix
	// work energy into what should be a constant baseline -- and, because
	// an active node is always preferred to a sleeping one when it has
	// capacity, a leader that serves would absorb light load indefinitely
	// and no worker would ever wake.
	DispatchOnly bool `json:"dispatch_only"`
}
