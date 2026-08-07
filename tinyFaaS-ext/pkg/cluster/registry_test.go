package cluster

import (
	"encoding/json"
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
	onCalls  []int
	offCalls []int
	onErr    error
}

func (f *fakePowerController) PowerOn(channel int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCalls = append(f.onCalls, channel)
	return f.onErr
}

func (f *fakePowerController) PowerOff(channel int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offCalls = append(f.offCalls, channel)
	return nil
}

func (f *fakePowerController) onCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.onCalls)
}

// fakeNode simulates a node's management API (/health, /upload, /delete)
// for tests, standing in for a real Raspberry Pi.
type fakeNode struct {
	mu         sync.Mutex
	uploads    []string
	deletes    []string
	failUpload map[string]bool

	srv *httptest.Server
}

func newFakeNode() *fakeNode {
	fn := &fakeNode{failUpload: make(map[string]bool)}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
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

func TestActivateNode_OneBadFunctionDoesNotKillNode(t *testing.T) {
	node := newFakeNode()
	defer node.close()
	node.failUpload["broken"] = true

	funcs := NewFunctionStore()
	funcs.Set("broken", FunctionDef{Env: "python3", Threads: 1})
	funcs.Set("fine", FunctionDef{Env: "python3", Threads: 1})

	reg := NewRegistry(&fakePowerController{}, funcs)
	reg.AddNode(Node{
		ID:             "edge-1",
		ManagerAddress: node.addr(),
		Channel:        0,
		Status:         NodeSleeping,
	})

	if err := reg.ActivateNode("edge-1"); err != nil {
		t.Fatalf("ActivateNode should succeed despite one bad function, got: %v", err)
	}

	n, _ := reg.GetNode("edge-1")
	if n.Status != NodeActive {
		t.Fatalf("expected node to still become active, got %s", n.Status)
	}

	uploads := node.uploadNames()
	if len(uploads) != 1 || uploads[0] != "fine" {
		t.Fatalf("expected only the healthy function to be deployed, got %v", uploads)
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
