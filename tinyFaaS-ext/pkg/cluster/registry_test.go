package cluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePowerController records PowerOn/PowerOff calls instead of talking to
// real Tinkerforge hardware.
type fakePowerController struct {
	mu       sync.Mutex
	onCalls   []int
	offCalls  []int
	onRelays  []string
	offRelays []string
	onErr     error
}

func (f *fakePowerController) PowerOn(relayUID string, channel int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCalls = append(f.onCalls, channel)
	f.onRelays = append(f.onRelays, relayUID)
	return f.onErr
}

func (f *fakePowerController) PowerOff(relayUID string, channel int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offCalls = append(f.offCalls, channel)
	f.offRelays = append(f.offRelays, relayUID)
	return nil
}

func (f *fakePowerController) onCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.onCalls)
}

func (f *fakePowerController) offCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.offCalls)
}

// failingPowerOffController powers on fine but cannot power off, standing in
// for a stuck or unreachable relay channel.
type failingPowerOffController struct {
	fakePowerController
}

func (f *failingPowerOffController) PowerOff(relayUID string, channel int) error {
	return fmt.Errorf("relay %s channel %d stuck", relayUID, channel)
}

// fakeNode simulates a node's management API (/health, /upload, /delete)
// for tests, standing in for a real Raspberry Pi.
type fakeNode struct {
	mu                 sync.Mutex
	uploads            []string
	deletes            []string
	failUpload         map[string]bool
	healthFailuresLeft int

	srv *httptest.Server
}

func newFakeNode() *fakeNode {
	fn := &fakeNode{failUpload: make(map[string]bool)}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fn.mu.Lock()
		fail := fn.healthFailuresLeft > 0
		if fail {
			fn.healthFailuresLeft--
		}
		fn.mu.Unlock()

		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		var d struct {
			FunctionName string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&d)

		fn.mu.Lock()
		defer fn.mu.Unlock()
		if fn.failUpload[d.FunctionName] {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fn.uploads = append(fn.uploads, d.FunctionName)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/delete", func(w http.ResponseWriter, r *http.Request) {
		var d struct {
			FunctionName string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&d)

		fn.mu.Lock()
		defer fn.mu.Unlock()
		fn.deletes = append(fn.deletes, d.FunctionName)
		w.WriteHeader(http.StatusOK)
	})

	fn.srv = httptest.NewServer(mux)
	return fn
}

func (fn *fakeNode) addr() string {
	return strings.TrimPrefix(fn.srv.URL, "http://")
}

func (fn *fakeNode) close() {
	fn.srv.Close()
}

func (fn *fakeNode) uploadNames() []string {
	fn.mu.Lock()
	defer fn.mu.Unlock()
	out := make([]string, len(fn.uploads))
	copy(out, fn.uploads)
	return out
}

func (fn *fakeNode) deleteNames() []string {
	fn.mu.Lock()
	defer fn.mu.Unlock()
	out := make([]string, len(fn.deletes))
	copy(out, fn.deletes)
	return out
}

// withFastHealthCheck shrinks the readiness-poll budget for the duration of
// a test so failure-path tests (nothing listening) don't burn through the
// real ~15s budget.
func withFastHealthCheck(t *testing.T) {
	t.Helper()
	prevAttempts, prevInterval := healthCheckAttempts, healthCheckInterval
	healthCheckAttempts, healthCheckInterval = 3, 5*time.Millisecond
	t.Cleanup(func() {
		healthCheckAttempts, healthCheckInterval = prevAttempts, prevInterval
	})
}

func TestActivateNode_WakesAndDeploysFunctions(t *testing.T) {
	node := newFakeNode()
	defer node.close()

	funcs := NewFunctionStore()
	funcs.Set("fn-a", FunctionDef{Env: "python3", Threads: 1, Zip: []byte("zip-a")})
	funcs.Set("fn-b", FunctionDef{Env: "nodejs", Threads: 2, Zip: []byte("zip-b")})

	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, funcs)
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	if err := reg.ActivateNode("edge-1"); err != nil {
		t.Fatalf("ActivateNode failed: %v", err)
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeActive {
		t.Fatalf("expected node to be active, got %s", n.Status)
	}

	uploads := node.uploadNames()
	if len(uploads) != 2 {
		t.Fatalf("expected 2 functions deployed on wake, got %d: %v", len(uploads), uploads)
	}

	if got := ctrl.onCallCount(); got != 1 {
		t.Fatalf("expected PowerOn to be called once, got %d", got)
	}
}

func TestActivateNode_FailedDeployDoesNotLeaveNodeServing(t *testing.T) {
	// A node whose function failed to deploy has no such function, so
	// routing to it produces 404s for every request. It must not end up
	// active, and it must not be left powered on drawing energy it cannot
	// do any work for.
	node := newFakeNode()
	defer node.close()
	node.failUpload["broken"] = true

	funcs := NewFunctionStore()
	funcs.Set("broken", FunctionDef{Env: "python3", Threads: 1})
	funcs.Set("fine", FunctionDef{Env: "python3", Threads: 1})

	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, funcs)
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	err := reg.ActivateNode("edge-1")
	if err == nil {
		t.Fatal("expected ActivateNode to report the failed deploy")
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeSleeping {
		t.Fatalf("expected node returned to sleeping, got %s", n.Status)
	}

	if got := ctrl.offCallCount(); got != 1 {
		t.Fatalf("expected the node to be powered back off exactly once, got %d", got)
	}

	// The healthy function should still have been attempted: failures are
	// per function, and the batch is not aborted at the first error.
	uploads := node.uploadNames()
	if len(uploads) != 1 || uploads[0] != "fine" {
		t.Fatalf("expected the healthy function to still be attempted, got %v", uploads)
	}
}

func TestActivateNode_FailedDeployAndFailedPowerOffMarksDead(t *testing.T) {
	// If it cannot even be powered down it is both drawing power and
	// unusable, so it has to be kept out of the scheduler permanently
	// rather than retried on the next request.
	node := newFakeNode()
	defer node.close()
	node.failUpload["broken"] = true

	funcs := NewFunctionStore()
	funcs.Set("broken", FunctionDef{Env: "python3", Threads: 1})

	ctrl := &failingPowerOffController{}
	reg := NewRegistry(ctrl, funcs)
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	if err := reg.ActivateNode("edge-1"); err == nil {
		t.Fatal("expected an error when deploy and power-off both fail")
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeDead {
		t.Fatalf("expected node marked dead, got %s", n.Status)
	}
}

func TestActivateNode_HealthyDeployStillActivates(t *testing.T) {
	node := newFakeNode()
	defer node.close()

	funcs := NewFunctionStore()
	funcs.Set("fine", FunctionDef{Env: "python3", Threads: 1})

	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, funcs)
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	if err := reg.ActivateNode("edge-1"); err != nil {
		t.Fatalf("expected a clean wake to succeed, got %v", err)
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeActive {
		t.Fatalf("expected node active, got %s", n.Status)
	}
	if got := ctrl.offCallCount(); got != 0 {
		t.Fatalf("a successful wake must not power the node off, got %d calls", got)
	}
}

func TestActivateNode_FollowerWaitsForRealOutcome(t *testing.T) {
	withFastHealthCheck(t)

	node := newFakeNode()
	defer node.close()
	node.healthFailuresLeft = 1 // forces at least one poll interval before ready

	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, NewFunctionStore())
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[0] = reg.ActivateNode("edge-1")
	}()

	// give the first caller a head start so it reliably wins the
	// Sleeping->Waking transition; the second caller should then observe
	// NodeWaking and take the follower path instead of powering on again
	time.Sleep(2 * time.Millisecond)

	wg.Add(1)
	go func() {
		defer wg.Done()
		errs[1] = reg.ActivateNode("edge-1")
	}()

	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("expected both callers to succeed, got %v and %v", errs[0], errs[1])
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeActive {
		t.Fatalf("expected node to be active, got %s", n.Status)
	}

	if got := ctrl.onCallCount(); got != 1 {
		t.Fatalf("expected exactly one PowerOn call (the follower should not re-trigger a wake), got %d", got)
	}
}

func TestActivateNode_AlreadyActiveSkipsPowerOn(t *testing.T) {
	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, NewFunctionStore())
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive})

	if err := reg.ActivateNode("local"); err != nil {
		t.Fatalf("ActivateNode failed: %v", err)
	}

	if got := ctrl.onCallCount(); got != 0 {
		t.Fatalf("expected no PowerOn calls for an already-active node, got %d", got)
	}
}

func TestActivateNode_FailedReadinessMarksDead(t *testing.T) {
	withFastHealthCheck(t)

	reg := NewRegistry(&fakePowerController{}, NewFunctionStore())
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: "127.0.0.1:1", // reserved port, nothing listens here
		Channel:        0,
		Status:         NodeSleeping,
	})

	if err := reg.ActivateNode("edge-1"); err == nil {
		t.Fatal("expected an error for a node that never becomes ready")
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeDead {
		t.Fatalf("expected node to be marked dead, got %s", n.Status)
	}
}

func TestDeactivateNode_RefusesLocalNode(t *testing.T) {
	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, NewFunctionStore())
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive})

	if err := reg.DeactivateNode("local"); err == nil {
		t.Fatal("expected DeactivateNode to refuse powering off the local node")
	}

	if got := len(ctrl.offCalls); got != 0 {
		t.Fatalf("expected no PowerOff calls for the local node, got %d", got)
	}
}

func TestBroadcastFunction_OnlyReachesActiveRemoteNodes(t *testing.T) {
	activeNode := newFakeNode()
	defer activeNode.close()
	sleepingNode := newFakeNode()
	defer sleepingNode.close()

	reg := NewRegistry(&fakePowerController{}, NewFunctionStore())
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive, ManagerAddress: "unused"})
	reg.AddNode(Node{ID: "edge-active", ManagerAddress: activeNode.addr(), Status: NodeActive})
	reg.AddNode(Node{ID: "edge-sleeping", ManagerAddress: sleepingNode.addr(), Status: NodeSleeping})

	reg.BroadcastFunction("late-fn", FunctionDef{Env: "python3", Threads: 1})

	if got := activeNode.uploadNames(); len(got) != 1 || got[0] != "late-fn" {
		t.Fatalf("expected active node to receive the broadcast function, got %v", got)
	}
	if got := sleepingNode.uploadNames(); len(got) != 0 {
		t.Fatalf("expected sleeping node to be skipped by broadcast, got %v", got)
	}
}

func TestBroadcastDelete_OnlyReachesActiveRemoteNodes(t *testing.T) {
	activeNode := newFakeNode()
	defer activeNode.close()

	reg := NewRegistry(&fakePowerController{}, NewFunctionStore())
	reg.AddNode(Node{ID: "local", Local: true, Status: NodeActive})
	reg.AddNode(Node{ID: "edge-active", ManagerAddress: activeNode.addr(), Status: NodeActive})

	reg.BroadcastDelete("old-fn")

	if got := activeNode.deleteNames(); len(got) != 1 || got[0] != "old-fn" {
		t.Fatalf("expected active node to receive the broadcast delete, got %v", got)
	}
}

func TestEnforceSleeping_PowersOffNodesRecordedAsAsleep(t *testing.T) {
	// A relay keeps its last position across restarts, so a node recorded as
	// sleeping may well still be running. Nothing else corrects that: the
	// controller only powers down nodes it thinks are active, so the draw
	// would go unaccounted for indefinitely.
	ctrl := &fakePowerController{}
	reg := NewRegistry(ctrl, NewFunctionStore())

	reg.AddNode(Node{ID: "leader", Local: true, Status: NodeActive, DispatchOnly: true})
	reg.AddNode(Node{ID: "pi1", Address: "a:1", ManagerAddress: "a:2", RelayUID: "2brr", Channel: 1, Status: NodeSleeping})
	reg.AddNode(Node{ID: "pi2", Address: "b:1", ManagerAddress: "b:2", RelayUID: "2brr", Channel: 0, Status: NodeSleeping})
	reg.AddNode(Node{ID: "pi3", Address: "c:1", ManagerAddress: "c:2", RelayUID: "2bro", Channel: 1, Status: NodeActive})

	reg.EnforceSleeping()

	if got := ctrl.offCallCount(); got != 2 {
		t.Fatalf("expected the two sleeping workers to be powered off, got %d calls", got)
	}

	if n, _ := reg.GetNode("pi3"); n.Status != NodeActive {
		t.Errorf("an active node must be left alone, got %s", n.Status)
	}
}
