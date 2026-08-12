package cluster

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

// ControllerConfig holds the scale-down policy. These were compile-time
// constants, which made the idle timeout -- the single most interesting
// policy parameter to vary in an evaluation -- impossible to sweep without
// rebuilding, and made the always-on baseline impossible to express at all.
type ControllerConfig struct {
	// Enabled false is the baseline: no node is ever powered down, so a run
	// measures the cluster without hardware orchestration.
	Enabled bool

	// IdleTimeout is how long a node must be idle before it is powered off.
	IdleTimeout time.Duration

	// CheckInterval is how often the controller re-evaluates the cluster.
	CheckInterval time.Duration

	// MinActiveNodes is the floor the controller will not scale below, so
	// the cluster always retains some capacity to serve without a wake.
	MinActiveNodes int

	// LoadThreshold is the in-flight request count above which a node is no
	// longer considered a preferred target by the scheduler.
	LoadThreshold int
}

func DefaultControllerConfig() ControllerConfig {
	return ControllerConfig{
		Enabled:        true,
		IdleTimeout:    60 * time.Second,
		CheckInterval:  10 * time.Second,
		MinActiveNodes: 1,
		LoadThreshold:  10,
	}
}

// ControllerConfigFromEnv layers environment overrides onto the defaults.
// An unparseable value is reported and ignored rather than silently taking
// effect as a zero, which would turn e.g. IDLE_TIMEOUT into "power down
// immediately" and quietly invalidate a whole run.
func ControllerConfigFromEnv() ControllerConfig {
	cfg := DefaultControllerConfig()

	cfg.Enabled = envBool("POWER_AWARE", cfg.Enabled)
	cfg.IdleTimeout = envDuration("NODE_IDLE_TIMEOUT", cfg.IdleTimeout)
	cfg.CheckInterval = envDuration("CONTROLLER_INTERVAL", cfg.CheckInterval)
	cfg.MinActiveNodes = envInt("MIN_ACTIVE_NODES", cfg.MinActiveNodes)
	cfg.LoadThreshold = envInt("LOAD_THRESHOLD", cfg.LoadThreshold)

	return cfg
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("config: invalid %s=%q, using %v", key, v, fallback)
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("config: invalid %s=%q, using %d", key, v, fallback)
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("config: invalid %s=%q, using %s", key, v, fallback)
		return fallback
	}
	return parsed
}

// ─── Node topology ───────────────────────────────────────────────────────────

type nodesFile struct {
	Nodes []Node `json:"nodes"`
}

// LoadNodes reads a cluster topology from a JSON file:
//
//	{"nodes": [
//	  {"id": "local",  "local": true},
//	  {"id": "edge-1", "address": "192.168.1.101:8000",
//	   "manager_address": "192.168.1.101:8080", "channel": 0}
//	]}
//
// Statuses are filled in automatically: the leader starts active, workers
// start asleep.
func LoadNodes(path string) ([]Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read node config: %w", err)
	}

	var parsed nodesFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse node config %s: %w", path, err)
	}

	for i := range parsed.Nodes {
		if parsed.Nodes[i].Status != "" {
			continue
		}
		if parsed.Nodes[i].Local {
			parsed.Nodes[i].Status = NodeActive
		} else {
			parsed.Nodes[i].Status = NodeSleeping
		}
	}

	if err := ValidateNodes(parsed.Nodes); err != nil {
		return nil, fmt.Errorf("invalid node config %s: %w", path, err)
	}

	return parsed.Nodes, nil
}

// ValidateNodes rejects topologies that would produce a misleading run
// rather than an obvious failure. A duplicate relay channel, for instance,
// means one relay switches two nodes: the cluster still serves traffic and
// the numbers still look plausible, but they describe something other than
// the configured topology.
func ValidateNodes(nodes []Node) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no nodes defined")
	}

	seenID := make(map[string]bool, len(nodes))
	seenChannel := make(map[int]string)
	locals := 0

	for _, n := range nodes {
		if n.ID == "" {
			return fmt.Errorf("node with empty id")
		}
		if seenID[n.ID] {
			return fmt.Errorf("duplicate node id %q", n.ID)
		}
		seenID[n.ID] = true

		if n.Local {
			locals++
			// The leader runs the management service that would have to
			// issue the power-off, so it cannot be power-cycled; a channel
			// here means the topology is describing something impossible.
			continue
		}

		if n.Address == "" {
			return fmt.Errorf("node %q has no address", n.ID)
		}
		if n.ManagerAddress == "" {
			return fmt.Errorf("node %q has no manager_address", n.ID)
		}
		if n.Channel < 0 {
			return fmt.Errorf("node %q has negative relay channel %d", n.ID, n.Channel)
		}
		if other, dup := seenChannel[n.Channel]; dup {
			return fmt.Errorf("nodes %q and %q share relay channel %d",
				other, n.ID, n.Channel)
		}
		seenChannel[n.Channel] = n.ID
	}

	if locals != 1 {
		return fmt.Errorf("expected exactly one node with \"local\": true, found %d", locals)
	}

	return nil
}

// AllActive returns a copy of nodes with every node marked active, for the
// always-on baseline.
func AllActive(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	copy(out, nodes)
	for i := range out {
		out[i].Status = NodeActive
	}
	return out
}
