package cluster

import "testing"

func TestPickNode_NoNodes(t *testing.T) {
	if _, ok := PickNode(nil, DefaultLoadThreshold); ok {
		t.Fatal("expected no node to be picked from an empty set")
	}
}

func TestPickNode_AllDead(t *testing.T) {
	nodes := []Node{
		{ID: "a", Status: NodeDead},
		{ID: "b", Status: NodeDead},
	}

	if _, ok := PickNode(nodes, DefaultLoadThreshold); ok {
		t.Fatal("expected no node to be picked when all nodes are dead")
	}
}

func TestPickNode_PrefersLeastLoadedActive(t *testing.T) {
	nodes := []Node{
		{ID: "busy", Status: NodeActive, Load: 5},
		{ID: "idle", Status: NodeActive, Load: 1},
		{ID: "asleep", Status: NodeSleeping},
	}

	n, ok := PickNode(nodes, DefaultLoadThreshold)
	if !ok {
		t.Fatal("expected a node to be picked")
	}
	if n.ID != "idle" {
		t.Fatalf("expected least-loaded active node 'idle', got %s", n.ID)
	}
}

func TestPickNode_WakesSleepingRatherThanOverloadingActive(t *testing.T) {
	// The cluster only ever scales out because of this. The leader is always
	// active and never sleeps, so preferring a loaded active node here would
	// mean no worker is ever woken however much load arrives.
	nodes := []Node{
		{ID: "leader", Status: NodeActive, Load: DefaultLoadThreshold + 5},
		{ID: "asleep", Status: NodeSleeping},
	}

	n, ok := PickNode(nodes, DefaultLoadThreshold)
	if !ok {
		t.Fatal("expected a node to be picked")
	}
	if n.ID != "asleep" {
		t.Fatalf("expected the sleeping node to be woken, got %s", n.ID)
	}
}

func TestPickNode_PrefersSpareCapacityOverWaking(t *testing.T) {
	// A wake costs seconds; an active node with room costs nothing.
	nodes := []Node{
		{ID: "leader", Status: NodeActive, Load: 1},
		{ID: "asleep", Status: NodeSleeping},
	}

	n, _ := PickNode(nodes, DefaultLoadThreshold)
	if n.ID != "leader" {
		t.Fatalf("expected the idle active node, got %s", n.ID)
	}
}

func TestPickNode_UsesLoadedActiveWhenNothingLeftToWake(t *testing.T) {
	nodes := []Node{
		{ID: "leader", Status: NodeActive, Load: DefaultLoadThreshold + 5},
		{ID: "gone", Status: NodeDead},
	}

	n, ok := PickNode(nodes, DefaultLoadThreshold)
	if !ok || n.ID != "leader" {
		t.Fatalf("expected the loaded active node as last resort, got %s (ok=%v)", n.ID, ok)
	}
}

func TestPickNode_FallsBackToSleepingWhenNoneActive(t *testing.T) {
	nodes := []Node{
		{ID: "dead", Status: NodeDead},
		{ID: "asleep", Status: NodeSleeping},
	}

	n, ok := PickNode(nodes, DefaultLoadThreshold)
	if !ok {
		t.Fatal("expected a node to be picked")
	}
	if n.ID != "asleep" {
		t.Fatalf("expected sleeping node as cold-start candidate, got %s", n.ID)
	}
}
