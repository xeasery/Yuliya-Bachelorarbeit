package cluster

import (
	"os"
	"strings"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadNodes_DefaultsStatusByRole(t *testing.T) {
	path := writeConfig(t, `{"nodes": [
		{"id": "local", "local": true},
		{"id": "edge-1", "address": "10.0.0.1:8000",
		 "manager_address": "10.0.0.1:8080", "channel": 0}
	]}`)

	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	byID := map[string]Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	if byID["local"].Status != NodeActive {
		t.Errorf("leader should start active, got %s", byID["local"].Status)
	}
	if byID["edge-1"].Status != NodeSleeping {
		t.Errorf("worker should start sleeping, got %s", byID["edge-1"].Status)
	}
}

func TestLoadNodes_RejectsDuplicateRelayChannel(t *testing.T) {
	// Two nodes on one relay channel still serves traffic and produces
	// plausible numbers, so this has to fail loudly at load time.
	path := writeConfig(t, `{"nodes": [
		{"id": "local", "local": true},
		{"id": "edge-1", "address": "10.0.0.1:8000",
		 "manager_address": "10.0.0.1:8080", "channel": 0},
		{"id": "edge-2", "address": "10.0.0.2:8000",
		 "manager_address": "10.0.0.2:8080", "channel": 0}
	]}`)

	if _, err := LoadNodes(path); err == nil {
		t.Fatal("expected an error for a shared relay channel")
	}
}

func TestValidateNodes_Rejects(t *testing.T) {
	cases := map[string][]Node{
		"no nodes": {},
		"no local": {
			{ID: "edge-1", Address: "a:1", ManagerAddress: "a:2"},
		},
		"two locals": {
			{ID: "local-1", Local: true},
			{ID: "local-2", Local: true},
		},
		"duplicate id": {
			{ID: "local", Local: true},
			{ID: "dup", Address: "a:1", ManagerAddress: "a:2", Channel: 0},
			{ID: "dup", Address: "b:1", ManagerAddress: "b:2", Channel: 1},
		},
		"empty id": {
			{ID: "local", Local: true},
			{ID: "", Address: "a:1", ManagerAddress: "a:2"},
		},
		"worker without address": {
			{ID: "local", Local: true},
			{ID: "edge-1", ManagerAddress: "a:2"},
		},
		"worker without manager address": {
			{ID: "local", Local: true},
			{ID: "edge-1", Address: "a:1"},
		},
	}

	for name, nodes := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNodes(nodes); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestValidateNodes_AcceptsFourWorkersAcrossTwoRelays(t *testing.T) {
	// The real cluster: one leader plus four workers, which needs two dual
	// relays. Channel 0 on one relay and channel 0 on the other are
	// different physical outlets and must not collide.
	nodes := []Node{
		{ID: "local", Local: true, Status: NodeActive},
		{ID: "edge-1", Address: "10.0.0.1:8000", ManagerAddress: "10.0.0.1:8080", RelayUID: "26mi", Channel: 0},
		{ID: "edge-2", Address: "10.0.0.2:8000", ManagerAddress: "10.0.0.2:8080", RelayUID: "26mi", Channel: 1},
		{ID: "edge-3", Address: "10.0.0.3:8000", ManagerAddress: "10.0.0.3:8080", RelayUID: "27ab", Channel: 0},
		{ID: "edge-4", Address: "10.0.0.4:8000", ManagerAddress: "10.0.0.4:8080", RelayUID: "27ab", Channel: 1},
	}

	if err := ValidateNodes(nodes); err != nil {
		t.Fatalf("expected a four-worker two-relay topology to be valid, got %v", err)
	}
}

func TestValidateNodes_RejectsChannelBeyondRelay(t *testing.T) {
	// Numbering four workers 0..3 on one relay is the obvious mistake, and
	// without this check it fails only when a node is first woken, showing
	// up mid-benchmark as a node marked dead for no visible reason.
	nodes := []Node{
		{ID: "local", Local: true},
		{ID: "edge-1", Address: "a:1", ManagerAddress: "a:2", Channel: 0},
		{ID: "edge-2", Address: "b:1", ManagerAddress: "b:2", Channel: 1},
		{ID: "edge-3", Address: "c:1", ManagerAddress: "c:2", Channel: 2},
	}

	err := ValidateNodes(nodes)
	if err == nil {
		t.Fatal("expected channel 2 on a dual relay to be rejected")
	}
	if !strings.Contains(err.Error(), "relay_uid") {
		t.Errorf("error should point at the fix (relay_uid), got: %v", err)
	}
}

func TestValidateNodes_RejectsSameRelayAndChannel(t *testing.T) {
	nodes := []Node{
		{ID: "local", Local: true},
		{ID: "edge-1", Address: "a:1", ManagerAddress: "a:2", RelayUID: "26mi", Channel: 0},
		{ID: "edge-2", Address: "b:1", ManagerAddress: "b:2", RelayUID: "26mi", Channel: 0},
	}

	if err := ValidateNodes(nodes); err == nil {
		t.Fatal("expected two nodes on the same relay channel to be rejected")
	}
}

func TestValidateNodes_AcceptsValidTopology(t *testing.T) {
	nodes := []Node{
		{ID: "local", Local: true, Status: NodeActive},
		{ID: "edge-1", Address: "10.0.0.1:8000", ManagerAddress: "10.0.0.1:8080", Channel: 0},
		{ID: "edge-2", Address: "10.0.0.2:8000", ManagerAddress: "10.0.0.2:8080", Channel: 1},
	}

	if err := ValidateNodes(nodes); err != nil {
		t.Fatalf("expected valid topology to be accepted, got %v", err)
	}
}

func TestControllerConfigFromEnv(t *testing.T) {
	t.Setenv("POWER_AWARE", "false")
	t.Setenv("NODE_IDLE_TIMEOUT", "5m")
	t.Setenv("MIN_ACTIVE_NODES", "3")
	t.Setenv("LOAD_THRESHOLD", "42")

	cfg := ControllerConfigFromEnv()

	if cfg.Enabled {
		t.Error("POWER_AWARE=false should disable the controller")
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("expected 5m idle timeout, got %s", cfg.IdleTimeout)
	}
	if cfg.MinActiveNodes != 3 {
		t.Errorf("expected 3 min active nodes, got %d", cfg.MinActiveNodes)
	}
	if cfg.LoadThreshold != 42 {
		t.Errorf("expected load threshold 42, got %d", cfg.LoadThreshold)
	}
}

func TestControllerConfigFromEnv_InvalidValuesFallBackToDefaults(t *testing.T) {
	// A zero idle timeout would mean "power everything down immediately",
	// so a typo must not be allowed to take effect as one.
	t.Setenv("NODE_IDLE_TIMEOUT", "sixty seconds")
	t.Setenv("MIN_ACTIVE_NODES", "lots")

	cfg := ControllerConfigFromEnv()
	def := DefaultControllerConfig()

	if cfg.IdleTimeout != def.IdleTimeout {
		t.Errorf("expected fallback to %s, got %s", def.IdleTimeout, cfg.IdleTimeout)
	}
	if cfg.MinActiveNodes != def.MinActiveNodes {
		t.Errorf("expected fallback to %d, got %d", def.MinActiveNodes, cfg.MinActiveNodes)
	}
}

func TestValidateNodes_RejectsAllDispatchOnly(t *testing.T) {
	nodes := []Node{
		{ID: "leader", Local: true, DispatchOnly: true},
		{ID: "pi1", Address: "a:1", ManagerAddress: "a:2", Channel: 0, DispatchOnly: true},
	}

	if err := ValidateNodes(nodes); err == nil {
		t.Fatal("a cluster where nothing can execute must be rejected")
	}
}
