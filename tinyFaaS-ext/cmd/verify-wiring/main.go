// verify-wiring checks that the cluster is wired the way the configuration
// says it is.
//
// Three mappings have to agree, and none of them can be checked from the
// software side once an experiment is running:
//
//	relay + channel -> physical machine
//	IP address      -> physical machine
//	energy bricklet -> physical machine
//
// A relay mistake surfaces eventually as a node marked dead, which points at
// the wrong thing. A bricklet mistake never surfaces at all: each node's
// energy is simply attributed to another, and every number downstream still
// looks reasonable.
//
// So this powers each worker on by itself and checks all three at once: that
// the expected address answers, and that the expected bricklet is the one
// whose current rose.
//
// Usage:
//
//	NODES_CONFIG=deploy/nodes.json \
//	ENERGY_NODES="leader=26gZ,edge-1=26vg,..." \
//	ENERGY_ADDR=<thinkpad>:4223 \
//	  verify-wiring
//
// It leaves every worker powered off.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Tinkerforge/go-api-bindings/industrial_dual_relay_bricklet"
	"github.com/Tinkerforge/go-api-bindings/ipconnection"
	"github.com/Tinkerforge/go-api-bindings/voltage_current_v2_bricklet"

	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/cluster"
)

const (
	// A booting Pi draws watts; a powered-off one draws milliwatts. Well
	// clear of both, so noise cannot trip it and a real boot cannot miss it.
	onThresholdW = 0.5

	settleAfterOff = 8 * time.Second
	bootTimeout    = 90 * time.Second
	pollInterval   = 2 * time.Second
)

type result struct {
	node        string
	poweredOK   bool
	sawBricklet string // which bricklet actually rose
	brickletOK  bool
	addressOK   bool
	note        string
}

func main() {
	log.SetFlags(0)

	nodesPath := getenv("NODES_CONFIG", "deploy/nodes.json")
	nodes, err := cluster.LoadNodes(nodesPath)
	if err != nil {
		log.Fatalf("node config: %v", err)
	}

	bricklets, err := parseBricklets(os.Getenv("ENERGY_NODES"))
	if err != nil {
		log.Printf("WARNING: %v", err)
		log.Printf("         continuing without energy checks — relay and address only")
	}

	// Relays live on whichever host runs this; energy bricklets may be
	// elsewhere, so they get their own connection.
	relayCon := ipconnection.New()
	defer relayCon.Close()
	if err := relayCon.Connect(getenv("RELAY_ADDR", "localhost:4223")); err != nil {
		log.Fatalf("connect to relay brickd: %v", err)
	}

	var energyCon *ipconnection.IPConnection
	vcs := map[string]*voltage_current_v2_bricklet.VoltageCurrentV2Bricklet{}
	if len(bricklets) > 0 {
		c := ipconnection.New()
		defer c.Close()
		if err := c.Connect(getenv("ENERGY_ADDR", "localhost:4223")); err != nil {
			log.Printf("WARNING: connect to energy brickd: %v", err)
			log.Printf("         continuing without energy checks")
			bricklets = nil
		} else {
			energyCon = &c
			for node, uid := range bricklets {
				vc, err := voltage_current_v2_bricklet.New(uid, energyCon)
				if err != nil {
					log.Fatalf("open bricklet %s for %s: %v", uid, node, err)
				}
				vcs[node] = &vc
			}
		}
	}

	workers := make([]cluster.Node, 0, len(nodes))
	for _, n := range nodes {
		if !n.Local {
			workers = append(workers, n)
		}
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })

	fmt.Printf("Verifying %d workers from %s\n", len(workers), nodesPath)
	fmt.Printf("Relays via %s", getenv("RELAY_ADDR", "localhost:4223"))
	if len(bricklets) > 0 {
		fmt.Printf(", energy via %s", getenv("ENERGY_ADDR", "localhost:4223"))
	}
	fmt.Printf("\n\n")

	// Start from a known state: everything off.
	fmt.Println("Powering all workers off…")
	for _, w := range workers {
		if err := setRelay(&relayCon, w.RelayUID, w.Channel, false); err != nil {
			log.Fatalf("power off %s: %v", w.ID, err)
		}
	}
	time.Sleep(settleAfterOff)

	results := make([]result, 0, len(workers))
	for _, w := range workers {
		results = append(results, verifyWorker(&relayCon, vcs, w))
	}

	// Leave the cluster as we found it.
	fmt.Println("\nPowering all workers off…")
	for _, w := range workers {
		_ = setRelay(&relayCon, w.RelayUID, w.Channel, false)
	}

	report(results, len(bricklets) > 0)
}

func verifyWorker(con *ipconnection.IPConnection,
	vcs map[string]*voltage_current_v2_bricklet.VoltageCurrentV2Bricklet,
	w cluster.Node) result {

	res := result{node: w.ID}
	fmt.Printf("── %s (relay %s ch%d, %s)\n", w.ID, w.RelayUID, w.Channel, w.ManagerAddress)

	// Baseline every bricklet while everything is off, so "rose" is measured
	// against this node's own starting point rather than an assumed zero.
	before := readAll(vcs)

	if err := setRelay(con, w.RelayUID, w.Channel, true); err != nil {
		res.note = fmt.Sprintf("power on failed: %v", err)
		fmt.Printf("   ✗ %s\n", res.note)
		return res
	}
	res.poweredOK = true
	fmt.Printf("   · powered on, waiting for it to boot…\n")

	deadline := time.Now().Add(bootTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)

		if !res.addressOK && healthy(w.ManagerAddress) {
			res.addressOK = true
			fmt.Printf("   ✓ %s answered /health\n", w.ManagerAddress)
		}
		if res.sawBricklet == "" && len(vcs) > 0 {
			if node := roseAbove(before, readAll(vcs), onThresholdW); node != "" {
				res.sawBricklet = node
				res.brickletOK = node == w.ID
				mark := "✓"
				if !res.brickletOK {
					mark = "✗"
				}
				fmt.Printf("   %s current rose on the bricklet mapped to %q\n", mark, node)
			}
		}
		if res.addressOK && (len(vcs) == 0 || res.sawBricklet != "") {
			break
		}
	}

	if !res.addressOK {
		fmt.Printf("   ✗ %s never answered /health within %s\n", w.ManagerAddress, bootTimeout)
	}
	if len(vcs) > 0 && res.sawBricklet == "" {
		fmt.Printf("   ✗ no bricklet showed a current rise\n")
	}

	_ = setRelay(con, w.RelayUID, w.Channel, false)
	time.Sleep(settleAfterOff)
	return res
}

func readAll(vcs map[string]*voltage_current_v2_bricklet.VoltageCurrentV2Bricklet) map[string]float64 {
	out := make(map[string]float64, len(vcs))
	for node, vc := range vcs {
		if mw, err := vc.GetPower(); err == nil {
			out[node] = float64(mw) / 1000.0
		}
	}
	return out
}

// roseAbove returns the node whose power increased the most, if that increase
// clears the threshold. Comparing rises rather than absolute values means a
// node that happens to idle high cannot be mistaken for the one that booted.
func roseAbove(before, after map[string]float64, threshold float64) string {
	best, bestDelta := "", 0.0
	for node, a := range after {
		delta := a - before[node]
		if delta > bestDelta {
			best, bestDelta = node, delta
		}
	}
	if bestDelta >= threshold {
		return best
	}
	return ""
}

func healthy(managerAddr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + managerAddr + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func setRelay(con *ipconnection.IPConnection, uid string, channel int, on bool) error {
	relay, err := industrial_dual_relay_bricklet.New(uid, con)
	if err != nil {
		return fmt.Errorf("open relay %s: %w", uid, err)
	}
	c0, c1, err := relay.GetValue()
	if err != nil {
		return fmt.Errorf("read relay %s: %w", uid, err)
	}
	switch channel {
	case 0:
		c0 = on
	case 1:
		c1 = on
	default:
		return fmt.Errorf("invalid channel %d", channel)
	}
	return relay.SetValue(c0, c1)
}

func report(results []result, energyChecked bool) {
	fmt.Printf("\n%-10s %-8s %-10s %s\n", "node", "address", "bricklet", "verdict")
	fmt.Println(strings.Repeat("─", 58))

	problems := 0
	for _, r := range results {
		addr, brick := "✗", "—"
		if r.addressOK {
			addr = "✓"
		}
		if energyChecked {
			brick = "✗"
			if r.brickletOK {
				brick = "✓"
			}
		}

		verdict := "ok"
		switch {
		case !r.poweredOK:
			verdict = "could not power on: " + r.note
		case !r.addressOK:
			verdict = "wrong IP for this relay channel, or node did not boot"
		case energyChecked && r.sawBricklet == "":
			verdict = "no bricklet responded — check the energy host"
		case energyChecked && !r.brickletOK:
			verdict = fmt.Sprintf("bricklet mismatch: this machine is measured by the one mapped to %q", r.sawBricklet)
		}
		if verdict != "ok" {
			problems++
		}

		fmt.Printf("%-10s %-8s %-10s %s\n", r.node, addr, brick, verdict)
	}

	fmt.Println()
	if problems == 0 {
		fmt.Println("All mappings verified. Relay, address and bricklet agree for every worker.")
		return
	}
	fmt.Printf("%d worker(s) mismatched — fix nodes.json or ENERGY_NODES before measuring.\n", problems)
	fmt.Println("A bricklet mismatch in particular is invisible once a run starts: the")
	fmt.Println("energy would be recorded against the wrong node and still look plausible.")
	os.Exit(1)
}

func parseBricklets(spec string) (map[string]string, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("ENERGY_NODES not set")
	}
	out := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		name, uid, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			return nil, fmt.Errorf("malformed ENERGY_NODES entry %q", pair)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(uid)
	}
	return out, nil
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
