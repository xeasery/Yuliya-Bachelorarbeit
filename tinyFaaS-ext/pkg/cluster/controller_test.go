package cluster

import (
	"sync"
	"testing"
	"time"
)

// Verifies the two experiment arms actually differ in behaviour, not just in
// a log line: with power management enabled an idle node gets powered off,
// and with it disabled the same node is left alone.

type countingPower struct {
	mu  sync.Mutex
	off []int
	on  []int
}

func (c *countingPower) PowerOn(relayUID string, ch int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = append(c.on, ch)
	return nil
}

func (c *countingPower) PowerOff(relayUID string, ch int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.off = append(c.off, ch)
	return nil
}

func (c *countingPower) offCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.off)
}

func newIdleCluster(ctrl PowerController) *Registry {
	reg := NewRegistry(ctrl, NewFunctionStore())
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive})
	reg.AddNode(Node{
		ID: "edge-1", Address: "127.0.0.1:1", ManagerAddress: "127.0.0.1:2",
		Channel: 0, Status: NodeActive,
		// idle well past any timeout under test
		LastUsed: time.Now().Add(-time.Hour),
	})
	return reg
}

func TestController_PowersDownIdleNodeWhenEnabled(t *testing.T) {
	ctrl := &countingPower{}
	reg := newIdleCluster(ctrl)

	StartController(reg, ControllerConfig{
		Enabled:        true,
		IdleTimeout:    10 * time.Millisecond,
		CheckInterval:  10 * time.Millisecond,
		MinActiveNodes: 1,
		LoadThreshold:  10,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.offCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if ctrl.offCount() == 0 {
		t.Fatal("expected the idle node to be powered down")
	}
	if n, _ := reg.GetNode("edge-1"); n.Status != NodeSleeping {
		t.Fatalf("expected edge-1 sleeping, got %s", n.Status)
	}
}

func TestController_LeavesNodesAloneWhenDisabled(t *testing.T) {
	ctrl := &countingPower{}
	reg := newIdleCluster(ctrl)

	StartController(reg, ControllerConfig{
		Enabled:        false,
		IdleTimeout:    10 * time.Millisecond,
		CheckInterval:  10 * time.Millisecond,
		MinActiveNodes: 1,
	})

	time.Sleep(300 * time.Millisecond)

	if got := ctrl.offCount(); got != 0 {
		t.Fatalf("baseline must never power a node down, got %d power-offs", got)
	}
	if n, _ := reg.GetNode("edge-1"); n.Status != NodeActive {
		t.Fatalf("expected edge-1 to stay active, got %s", n.Status)
	}
}

func TestController_RespectsMinActiveNodes(t *testing.T) {
	ctrl := &countingPower{}
	reg := NewRegistry(ctrl, NewFunctionStore())
	// Only one non-local node is active, and the floor is 2 counting local:
	// nothing may be powered down.
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive})
	reg.AddNode(Node{
		ID: "edge-1", Address: "a:1", ManagerAddress: "a:2", Channel: 0,
		Status: NodeActive, LastUsed: time.Now().Add(-time.Hour),
	})

	StartController(reg, ControllerConfig{
		Enabled:        true,
		IdleTimeout:    10 * time.Millisecond,
		CheckInterval:  10 * time.Millisecond,
		MinActiveNodes: 2,
	})

	time.Sleep(300 * time.Millisecond)

	if got := ctrl.offCount(); got != 0 {
		t.Fatalf("MinActiveNodes=2 should have prevented power-off, got %d", got)
	}
}
