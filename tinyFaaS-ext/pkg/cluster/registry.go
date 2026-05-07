package cluster

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

type Registry struct {
	nodes map[string]*Node
	mu    sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{
		nodes: make(map[string]*Node),
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

func (r *Registry) ListNodes() []*Node {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodes := make([]*Node, 0, len(r.nodes))
	for _, node := range r.nodes {
		nodes = append(nodes, node)
	}

	return nodes
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

func (r *Registry) ActivateNode(n *Node) error {
	if n == nil {
		return nil
	}

	r.mu.Lock()

	if n.Status == NodeActive {
		r.mu.Unlock()
		return nil
	}

	if n.Status == NodeWaking {
		r.mu.Unlock()
		for n.Status == NodeWaking {
			time.Sleep(100 * time.Millisecond)
		}
		return nil
	}

	n.Status = NodeWaking
	r.mu.Unlock()

	log.Printf("activating node %s", n.ID)

	// hardware call outside lock
	ctrl := tinkerforgefunc.NewTinkerforgeController("localhost", 4223, "YOUR_UID")
	err := ctrl.PowerOn(n.Channel)
	if err != nil {
		r.mu.Lock()
		n.Status = NodeDead
		r.mu.Unlock()
		return fmt.Errorf("failed to power on node %s: %w", n.ID, err)
	}

	// wait for readiness (replace sleep-based startup)
	ok := waitForNodeReady(n.Address)
	if !ok {
		r.mu.Lock()
		n.Status = NodeDead
		r.mu.Unlock()
		return fmt.Errorf("node %s failed readiness check", n.ID)
	}

	r.mu.Lock()
	n.Status = NodeActive
	r.mu.Unlock()

	log.Printf("node %s is active", n.ID)
	return nil
}

func waitForNodeReady(addr string) bool {
	for i := 0; i < 30; i++ {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil && resp.StatusCode == 200 {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
