package cluster

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
// API. Building a function image can be very slow -- the first build of a
// function with compiled dependencies (numpy, Pillow) on a constrained edge
// device runs for minutes, and only later wakes hit Docker's layer cache.
// A timeout shorter than that turns every first deploy into a failed wake.
// Override with DEPLOY_TIMEOUT (a Go duration, e.g. "10m").
var deployClient = &http.Client{Timeout: deployTimeoutFromEnv()}

func deployTimeoutFromEnv() time.Duration {
	const fallback = 5 * time.Minute

	v := os.Getenv("DEPLOY_TIMEOUT")
	if v == "" {
		return fallback
	}

	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("config: invalid DEPLOY_TIMEOUT=%q, using %s", v, fallback)
		return fallback
	}
	return d
}

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
	PowerOn(relayUID string, channel int) error
	PowerOff(relayUID string, channel int) error
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
	relayUID := n.RelayUID
	channel := n.Channel
	managerAddress := n.ManagerAddress
	r.mu.Unlock()

	log.Printf("activating node %s", id)

	err := r.wakeNode(id, relayUID, channel, managerAddress)

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
func (r *Registry) wakeNode(id, relayUID string, channel int, managerAddress string) error {
	// hardware call outside any lock
	if err := r.ctrl.PowerOn(relayUID, channel); err != nil {
		r.SetStatus(id, NodeDead)
		return fmt.Errorf("failed to power on node %s: %w", id, err)
	}

	// wait for readiness (replace sleep-based startup)
	if !waitForNodeReady(managerAddress) {
		r.SetStatus(id, NodeDead)
		return fmt.Errorf("node %s failed readiness check", id)
	}

	// The node booted with no containers running, so every known function
	// must be redeployed before it can be trusted to serve traffic.
	if failed := r.deployFunctions(id, managerAddress); failed > 0 {
		// Marking the node active here would be worse than the wake having
		// failed outright: the scheduler would route to a node that does
		// not have the function and every one of those requests would 404.
		// In an evaluation that shows up only in the power-aware arm, since
		// the baseline never redeploys, and looks like power management
		// causing errors rather than a failed deploy.
		//
		// Power it back off and return it to sleeping rather than marking
		// it dead: the failure is usually transient (a slow first image
		// build timing out), a sleeping node is retried on the next request
		// that needs it, and leaving it powered on would burn energy for a
		// node that cannot serve anything -- which this system exists to
		// avoid, and which would skew the very numbers being measured.
		log.Printf("node %s: %d function(s) failed to deploy, returning it to sleep", id, failed)

		if err := r.ctrl.PowerOff(relayUID, channel); err != nil {
			// Cannot power it down, so it is drawing power and is not
			// usable. Dead keeps the scheduler away from it.
			log.Printf("node %s: failed to power off after bad deploy: %v", id, err)
			r.SetStatus(id, NodeDead)
			return fmt.Errorf("node %s: %d function(s) failed to deploy and it could not be powered off: %w", id, failed, err)
		}

		r.SetStatus(id, NodeSleeping)
		return fmt.Errorf("node %s: %d function(s) failed to deploy", id, failed)
	}

	r.SetStatus(id, NodeActive)
	return nil
}

// PrewarmAll brings every node up front, for the always-on baseline.
//
// Nodes stay recorded as sleeping until this actually powers them on, rather
// than being marked active up front: ActivateNode returns immediately for a
// node it already believes is active, so pre-marking them would skip the
// power-on entirely and leave the cluster switched off while claiming
// otherwise.
//
// Marking nodes active without actually powering them on would be worse
// than useless: the scheduler would route to a node that is physically off
// and every one of those requests would fail, quietly ruining the baseline
// it was supposed to establish. Letting them wake on demand instead is not
// a baseline either -- that is still hardware orchestration, only with a
// different trigger -- so the cluster is brought up explicitly.
//
// Nodes are woken concurrently and failures are logged per node: one dead
// node should not prevent the rest of the cluster from coming up.
func (r *Registry) PrewarmAll() {
	var wg sync.WaitGroup

	for _, n := range r.ListNodes() {
		if n.Local {
			continue
		}

		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := r.ActivateNode(id); err != nil {
				log.Printf("prewarm: node %s failed to come up: %v", id, err)
			}
		}(n.ID)
	}

	wg.Wait()

	active := 0
	for _, n := range r.ListNodes() {
		if n.Status == NodeActive {
			active++
		}
	}
	log.Printf("prewarm: %d/%d nodes active", active, len(r.ListNodes()))
}

// EnforceSleeping opens the relay for every node the registry believes is
// asleep, so the cluster's physical state matches its recorded state.
//
// Without this the two can disagree indefinitely. The registry starts every
// worker as sleeping, but a relay holds whatever position it was left in, so
// a node that was powered on before startup keeps running while the leader
// believes it is off. Nothing corrects that later: the controller only ever
// powers down nodes it considers active.
//
// The cost of the disagreement is silent and lands directly on the
// measurement -- several watts per node attributed to nothing, in a system
// whose entire purpose is accounting for those watts.
func (r *Registry) EnforceSleeping() {
	for _, n := range r.ListNodes() {
		if n.Local || n.Status != NodeSleeping {
			continue
		}

		if err := r.ctrl.PowerOff(n.RelayUID, n.Channel); err != nil {
			log.Printf("startup: could not power off %s, which is recorded as sleeping: %v", n.ID, err)
			continue
		}
		log.Printf("startup: powered off %s to match its recorded state", n.ID)
	}
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
	relayUID := n.RelayUID
	channel := n.Channel
	r.mu.RUnlock()

	if err := r.ctrl.PowerOff(relayUID, channel); err != nil {
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
// total wake time balloon with every function added.
//
// Returns the number of functions that failed. Individual failures do not
// abort the batch, but the caller must not treat a node as ready when any
// of them failed: it would serve 404s for a function it does not have.
func (r *Registry) deployFunctions(nodeID, managerAddr string) int {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
	)

	for name, def := range r.funcs.All() {
		wg.Add(1)
		go func(name string, def FunctionDef) {
			defer wg.Done()
			if err := deployFunction(managerAddr, name, def); err != nil {
				log.Printf("failed to deploy function %s to node %s: %v", name, nodeID, err)
				mu.Lock()
				failed++
				mu.Unlock()
				return
			}
			log.Printf("deployed function %s to node %s", name, nodeID)
		}(name, def)
	}

	wg.Wait()

	return failed
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
