package cluster

import (
	"sort"
	"sync"
)

// FunctionDef captures everything needed to redeploy a function to a node
// via its management API. Powering a node off drops its containers with
// it, so on wake a node starts with nothing deployed and must be brought
// back up to date from scratch.
type FunctionDef struct {
	Env     string
	Threads int
	Envs    map[string]string
	Zip     []byte
}

// FunctionStore mirrors the set of functions currently known to the local
// management service, keyed by function name, so that a newly woken node
// can be redeployed to without needing access to the management service's
// process (they run as separate processes/binaries).
type FunctionStore struct {
	mu    sync.RWMutex
	funcs map[string]FunctionDef
}

func NewFunctionStore() *FunctionStore {
	return &FunctionStore{
		funcs: make(map[string]FunctionDef),
	}
}

func (s *FunctionStore) Set(name string, def FunctionDef) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.funcs[name] = def
}

func (s *FunctionStore) Remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.funcs, name)
}

// Names returns the known function names, sorted. Cheaper than All when the
// caller only needs to address the functions rather than deploy them -- the
// definitions carry a base64 zip each.
func (s *FunctionStore) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.funcs))
	for k := range s.funcs {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}

// All returns a point-in-time copy of every known function definition.
func (s *FunctionStore) All() map[string]FunctionDef {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]FunctionDef, len(s.funcs))
	for k, v := range s.funcs {
		out[k] = v
	}

	return out
}
