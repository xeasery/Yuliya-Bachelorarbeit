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

func TestPickNode_FallsBackToOverloadedActiveBeforeSleeping(t *testing.T) {
	nodes := []Node{
		{ID: "overloaded", Status: NodeActive, Load: DefaultLoadThreshold + 5},
		{ID: "asleep", Status: NodeSleeping},
	}

	n, ok := PickNode(nodes, DefaultLoadThreshold)
	if !ok {
		t.Fatal("expected a node to be picked")
	}
	if n.ID != "overloaded" {
		t.Fatalf("expected active-but-loaded node to win over sleeping, got %s", n.ID)
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
