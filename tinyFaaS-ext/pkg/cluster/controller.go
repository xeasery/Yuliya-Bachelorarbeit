package cluster

import (
	"log"
	"time"
)

// StartController runs the scale-down loop in the background: nodes that
// have been idle long enough are powered off via the relay.
//
// With cfg.Enabled false it does nothing at all, which is the always-on
// baseline the power-aware configuration is measured against.
func StartController(reg *Registry, cfg ControllerConfig) {
	if !cfg.Enabled {
		log.Printf("controller: power management disabled (baseline mode) — no node will be powered down")
		return
	}

	log.Printf("controller: idle timeout %s, check interval %s, min active %d",
		cfg.IdleTimeout, cfg.CheckInterval, cfg.MinActiveNodes)

	go func() {
		for {
			time.Sleep(cfg.CheckInterval)

			nodes := reg.ListNodes()

			// Count only nodes that can actually serve: a dispatch-only
			// leader is always active but contributes no capacity, so
			// counting it would let the floor be satisfied while every
			// executing node was asleep.
			activeCount := 0
			for _, n := range nodes {
				if n.Status == NodeActive && !n.DispatchOnly {
					activeCount++
				}
			}

			for _, n := range nodes {
				// the local node isn't behind a relay and must never be
				// power-cycled
				if n.Local {
					continue
				}

				// only active nodes can be scaled down
				if n.Status != NodeActive {
					continue
				}

				// never kill busy nodes
				if n.Load > 0 {
					continue
				}

				// keep minimum capacity alive
				if activeCount <= cfg.MinActiveNodes {
					continue
				}

				// must be idle long enough
				if time.Since(n.LastUsed) < cfg.IdleTimeout {
					continue
				}

				log.Printf("controller: scaling down node %s", n.ID)

				if err := reg.DeactivateNode(n.ID); err != nil {
					log.Printf("controller: failed to power off %s: %v", n.ID, err)
					continue
				}

				activeCount--
			}
		}
	}()
}
