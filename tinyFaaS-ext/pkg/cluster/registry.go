package cluster

import "sync"

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
