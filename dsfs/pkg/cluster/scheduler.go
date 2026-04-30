package cluster

func PickLeastLoadedNode(nodes []*Node) *Node {
	if len(nodes) == 0 {
		return nil
	}

	best := nodes[0]

	for _, node := range nodes {
		if node.Load < best.Load {
			best = node
		}
	}

	return best
}
