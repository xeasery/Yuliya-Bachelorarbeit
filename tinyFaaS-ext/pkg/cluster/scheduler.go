package cluster

// DefaultLoadThreshold is the in-flight request count above which a node
// stops being a preferred target. Callers pass the configured value; this
// is the fallback and what the tests pin against.
const DefaultLoadThreshold = 10

// PickNode selects the best node for a request from a snapshot of nodes.
// It only decides — it does NOT mutate state or activate anything.
//
// loadThreshold is passed in rather than read from a package global so the
// decision stays a pure function of its inputs, which is what makes it
// testable without standing up a cluster.
func PickNode(nodes []Node, loadThreshold int) (Node, bool) {
	var bestActive *Node
	var bestIdleActive *Node
	var bestSleeping *Node
	var fallback *Node

	for i := range nodes {
		n := &nodes[i]

		// ignore dead nodes completely
		if n.Status == NodeDead {
			continue
		}

		// 1. Prefer active nodes under load threshold
		if n.Status == NodeActive && n.Load < loadThreshold {
			if bestActive == nil || n.Load < bestActive.Load {
				bestActive = n
			}
			continue
		}

		// 2. Active but slightly loaded nodes (fallback within active pool)
		if n.Status == NodeActive {
			if bestIdleActive == nil || n.Load < bestIdleActive.Load {
				bestIdleActive = n
			}
			continue
		}

		// 3. Sleeping nodes (cold start candidates)
		if n.Status == NodeSleeping {
			if bestSleeping == nil {
				bestSleeping = n
			}
			continue
		}

		// fallback safety
		if fallback == nil {
			fallback = n
		}
	}

	// Priority order:

	// 1. best healthy active node
	if bestActive != nil {
		return *bestActive, true
	}

	// 2. any active node under pressure (still better than waking)
	if bestIdleActive != nil {
		return *bestIdleActive, true
	}

	// 3. sleeping node (will trigger activation in HTTP layer)
	if bestSleeping != nil {
		return *bestSleeping, true
	}

	// 4. absolute fallback
	if fallback != nil {
		return *fallback, true
	}

	return Node{}, false
}
