package cluster

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// activationSafetyTimeout is an outer bound for a node that never finishes
// waking (hardware fault, hung deploy, etc). It is not meant to fire in the
// normal case: followers otherwise learn the real outcome the instant the
// waking goroutine finishes, via the node's activation channel, rather than
// on some independently-guessed schedule that can drift out of sync with
// how long waking actually takes.
const activationSafetyTimeout = 5 * time.Minute

// healthClient is used for readiness polling: short timeout, called
// repeatedly, so a single hung attempt shouldn't stall activation for long.
var healthClient = &http.Client{Timeout: 2 * time.Second}

// deployClient is used for /upload and /delete calls to a node's management
// API. Building a function image can be slow, so this gets a much longer
// timeout than the health check does.
var deployClient = &http.Client{Timeout: 60 * time.Second}

// healthCheckAttempts/healthCheckInterval bound how long waitForNodeReady
// polls before giving up. Package-level vars (rather than constants) so
// tests can shrink them instead of waiting out the real 15s budget.
var (
	healthCheckAttempts = 30
	healthCheckInterval = 500 * time.Millisecond
)

// PowerController abstracts the hardware relay so the registry can be
// tested without real Tinkerforge hardware. *tinkerforgefunc.TinkerforgeController
// already satisfies this interface as-is.
type PowerController interface {
	PowerOn(channel int) error
	PowerOff(channel int) error
}

// activation tracks a single in-flight wake attempt for a node. done is
// closed once the attempt finishes, at which point err holds the result.
// Followers that arrive while a node is NodeWaking wait on done instead of
// polling status on a fixed schedule.
type activation struct {
	done chan struct{}
	err  error
}

func (a *activation) wait(id string) error {
	select {
	case <-a.done:
		return a.err
	case <-time.After(activationSafetyTimeout):
		return fmt.Errorf("timed out waiting for node %s to activate", id)
	}
}

type Registry struct {
	nodes map[string]*Node
	mu    sync.RWMutex

	ctrl        PowerController
	funcs       *FunctionStore
	activations map[string]*activation
}

func NewRegistry(ctrl PowerController, funcs *FunctionStore) *Registry {
	return &Registry{
		nodes:       make(map[string]*Node),
		ctrl:        ctrl,
		funcs:       funcs,
		activations: make(map[string]*activation),
	}
}

func (r *Registry) AddNode(node Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes[node.ID] = &node
}

func (r *Registry) RemoveNode(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, id)
}

// ListNodes returns a point-in-time snapshot of all nodes. The returned
// values are copies, so callers may read them freely without further
// synchronization; mutations must go through the Registry's ID-based
// methods.
func (r *Registry) ListNodes() []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodes := make([]Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, *node)
	}

	return nodes
}

// GetNode returns a snapshot of a single node by ID.
func (r *Registry) GetNode(id string) (Node, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n, ok := r.nodes[id]
	if !ok {
		return Node{}, false
	}
	return *n, true
}

func (r *Registry) IncLoad(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n, ok := r.nodes[id]; ok {
		n.Load++
	}
}

func (r *Registry) DecLoad(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n, ok := r.nodes[id]; ok {
		if n.Load > 0 {
			n.Load--
		}
	}
}

func (r *Registry) TouchNode(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n, ok := r.nodes[id]; ok {
		n.LastUsed = time.Now()
	}
}

// SetStatus safely updates a node's status by ID.
func (r *Registry) SetStatus(id string, status NodeStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n, ok := r.nodes[id]; ok {
		n.Status = status
	}
}

// ActivateNode ensures the node with the given ID is active, powering it on
// via the Tinkerforge relay if necessary. If another goroutine is already
// waking the node, it waits on that attempt's completion signal instead of
// triggering a second power-on or polling on its own schedule -- so it
// learns the real outcome the moment the wake finishes, however long that
// actually takes.
func (r *Registry) ActivateNode(id string) error {
	r.mu.Lock()

	n, ok := r.nodes[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("unknown node %s", id)
	}

	switch n.Status {
	case NodeActive:
		r.mu.Unlock()
		return nil
	case NodeWaking:
		act := r.activations[id]
		r.mu.Unlock()
		return act.wait(id)
	}

	act := &activation{done: make(chan struct{})}
	r.activations[id] = act

	n.Status = NodeWaking
	channel := n.Channel
	managerAddress := n.ManagerAddress
	r.mu.Unlock()

	log.Printf("activating node %s", id)

	err := r.wakeNode(id, channel, managerAddress)

	r.mu.Lock()
	delete(r.activations, id)
	r.mu.Unlock()

	act.err = err
	close(act.done)

	if err == nil {
		log.Printf("node %s is active", id)
	}

	return err
}

// wakeNode runs the actual wake sequence for a node: power on, wait for
// readiness, then redeploy known functions. It sets the node's final status
// (Active or Dead) itself.
func (r *Registry) wakeNode(id string, channel int, managerAddress string) error {
	// hardware call outside any lock
	if err := r.ctrl.PowerOn(channel); err != nil {
		r.SetStatus(id, NodeDead)
		return fmt.Errorf("failed to power on node %s: %w", id, err)
	}

	// wait for readiness (replace sleep-based startup)
	if !waitForNodeReady(managerAddress) {
		r.SetStatus(id, NodeDead)
		return fmt.Errorf("node %s failed readiness check", id)
	}

	// the node booted with no containers running, so every known function
	// must be redeployed before it can be trusted to serve traffic. this is
	// best-effort per function: one broken function shouldn't take an
	// otherwise-healthy node out of the pool.
	r.deployFunctions(id, managerAddress)

	r.SetStatus(id, NodeActive)
	return nil
}

// DeactivateNode powers a node off via the Tinkerforge relay and marks it
// sleeping.
func (r *Registry) DeactivateNode(id string) error {
	r.mu.RLock()
	n, ok := r.nodes[id]
	if !ok {
		r.mu.RUnlock()
		return fmt.Errorf("unknown node %s", id)
	}
	if n.Local {
		r.mu.RUnlock()
		return fmt.Errorf("refusing to power-cycle local node %s", id)
	}
	channel := n.Channel
	r.mu.RUnlock()

	if err := r.ctrl.PowerOff(channel); err != nil {
		return fmt.Errorf("failed to power off node %s: %w", id, err)
	}

	r.SetStatus(id, NodeSleeping)
	return nil
}

// waitForNodeReady polls a node's management API until it responds to
// /health.
func waitForNodeReady(managerAddr string) bool {
	for i := 0; i < healthCheckAttempts; i++ {
		resp, err := healthClient.Get("http://" + managerAddr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(healthCheckInterval)
	}
	return false
}

// deployFunctions pushes every function known to this registry's function
// store to a node's management API, e.g. right after it wakes up with no
// containers running. Functions are deployed concurrently -- sequentially
// deploying N functions, each with its own slow-build timeout, would make
// total wake time balloon with every function added -- and failures are
// logged per function rather than aborting the whole batch.
func (r *Registry) deployFunctions(nodeID, managerAddr string) {
	var wg sync.WaitGroup

	for name, def := range r.funcs.All() {
		wg.Add(1)
		go func(name string, def FunctionDef) {
			defer wg.Done()
			if err := deployFunction(managerAddr, name, def); err != nil {
				log.Printf("failed to deploy function %s to node %s: %v", name, nodeID, err)
				return
			}
			log.Printf("deployed function %s to node %s", name, nodeID)
		}(name, def)
	}

	wg.Wait()
}

// BroadcastFunction pushes a single function to every node that is already
// active (excluding local, which got it directly from the management
// service that created it). This covers functions uploaded or updated after
// a node woke up -- deploy-on-wake alone only reaches nodes at the moment
// they transition from sleeping to active.
func (r *Registry) BroadcastFunction(name string, def FunctionDef) {
	var wg sync.WaitGroup

	for _, n := range r.ListNodes() {
		if n.Local || n.Status != NodeActive {
			continue
		}

		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			if err := deployFunction(n.ManagerAddress, name, def); err != nil {
				log.Printf("failed to broadcast function %s to node %s: %v", name, n.ID, err)
				return
			}
			log.Printf("broadcast function %s to node %s", name, n.ID)
		}(n)
	}

	wg.Wait()
}

// BroadcastDelete removes a function from every node that is currently
// active (excluding local, which handles its own deletion directly).
func (r *Registry) BroadcastDelete(name string) {
	var wg sync.WaitGroup

	for _, n := range r.ListNodes() {
		if n.Local || n.Status != NodeActive {
			continue
		}

		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			if err := deleteFunction(n.ManagerAddress, name); err != nil {
				log.Printf("failed to broadcast delete of %s to node %s: %v", name, n.ID, err)
				return
			}
			log.Printf("broadcast delete of %s to node %s", name, n.ID)
		}(n)
	}

	wg.Wait()
}

// deployFunction pushes a single function to a node's management API, using
// the same wire format as the management service's own /upload endpoint, so
// no special handling is required on the receiving node.
func deployFunction(managerAddr, name string, def FunctionDef) error {
	envs := make([]string, 0, len(def.Envs))
	for k, v := range def.Envs {
		envs = append(envs, k+"="+v)
	}

	body, err := json.Marshal(struct {
		FunctionName    string   `json:"name"`
		FunctionEnv     string   `json:"env"`
		FunctionThreads int      `json:"threads"`
		FunctionZip     string   `json:"zip"`
		FunctionEnvs    []string `json:"envs"`
	}{
		FunctionName:    name,
		FunctionEnv:     def.Env,
		FunctionThreads: def.Threads,
		FunctionZip:     base64.StdEncoding.EncodeToString(def.Zip),
		FunctionEnvs:    envs,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal function %s: %w", name, err)
	}

	resp, err := deployClient.Post("http://"+managerAddr+"/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node rejected upload: status %d", resp.StatusCode)
	}

	return nil
}

// deleteFunction removes a single function from a node's management API.
func deleteFunction(managerAddr, name string) error {
	body, err := json.Marshal(struct {
		FunctionName string `json:"name"`
	}{
		FunctionName: name,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal delete request for %s: %w", name, err)
	}

	resp, err := deployClient.Post("http://"+managerAddr+"/delete", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node rejected delete: status %d", resp.StatusCode)
	}

	return nil
}
