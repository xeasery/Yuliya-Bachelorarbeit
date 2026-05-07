package cluster

import (
	"log"
	"time"

	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/tinkerforgefunc"
)

const (
	NodeIdleTimeout = 60 * time.Second
	CheckInterval   = 10 * time.Second
	MinActiveNodes  = 1
)

func StartController(reg *Registry) {
	go func() {

		ctrl := tinkerforgefunc.NewTinkerforgeController(
			"localhost",
			4223,
			"YOUR_UID",
		)

		for {
			time.Sleep(CheckInterval)

			nodes := reg.ListNodes()

			activeCount := 0
			for _, n := range nodes {
				if n.Status == NodeActive {
					activeCount++
				}
			}

			for _, n := range nodes {
				if n == nil {
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
				if activeCount <= MinActiveNodes {
					continue
				}

				// must be idle long enough
				if time.Since(n.LastUsed) < NodeIdleTimeout {
					continue
				}

				log.Printf("controller: scaling down node %s", n.ID)

				err := ctrl.PowerOff(n.Channel)
				if err != nil {
					log.Printf("controller: failed to power off %s: %v", n.ID, err)
					continue
				}

				// update state safely via registry rules
				func() {
					reg.mu.Lock()
					defer reg.mu.Unlock()

					n.Status = NodeSleeping
				}()

				activeCount--
			}
		}
	}()
}
