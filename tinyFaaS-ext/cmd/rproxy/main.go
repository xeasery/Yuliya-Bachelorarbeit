package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/cluster"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/coap"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/grpc"
	tfhttp "github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/http"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/rproxy"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/tinkerforgefunc"
)

// Tinkerforge relay connection defaults, overridable via env vars so a
// single hardcoded connection isn't duplicated across the codebase.
const (
	defaultTinkerforgeHost = "localhost"
	defaultTinkerforgePort = 4223
	defaultTinkerforgeUID  = "YOUR_UID"
)

func tinkerforgeConfigFromEnv() (host string, port int, uid string) {
	host = defaultTinkerforgeHost
	if v := os.Getenv("TINKERFORGE_HOST"); v != "" {
		host = v
	}

	port = defaultTinkerforgePort
	if v := os.Getenv("TINKERFORGE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		} else {
			log.Printf("invalid TINKERFORGE_PORT %q, using default %d", v, defaultTinkerforgePort)
		}
	}

	uid = defaultTinkerforgeUID
	if v := os.Getenv("TINKERFORGE_UID"); v != "" {
		uid = v
	}

	return host, port, uid
}

// defaultNodes is the single-machine fallback used when NODES_CONFIG is not
// set: just the local node, no relay-controlled workers. It lets tinyFaaS
// run unchanged on a developer machine, while a real cluster is described
// by a config file rather than baked into this binary.
func defaultNodes() []cluster.Node {
	return []cluster.Node{
		{ID: "local", Local: true, Status: cluster.NodeActive},
	}
}

func loadTopology() ([]cluster.Node, error) {
	path := os.Getenv("NODES_CONFIG")
	if path == "" {
		log.Printf("cluster: NODES_CONFIG not set, running single-node (local only)")
		return defaultNodes(), nil
	}

	nodes, err := cluster.LoadNodes(path)
	if err != nil {
		return nil, err
	}

	log.Printf("cluster: loaded %d nodes from %s", len(nodes), path)
	return nodes, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetPrefix("rproxy: ")

	if len(os.Args) <= 3 {
		fmt.Println("Usage: ./rproxy <listen-addr> [<protocol>:<listen-addr>]")
		os.Exit(1)
	}

	rproxyListenAddress := os.Args[1]

	listenAddrs := make(map[string]string)

	for _, arg := range os.Args[2:] {
		prot, listenAddr, ok := strings.Cut(arg, ":")

		if !ok {
			fmt.Println("Usage: ./rproxy <listen-addr> <protocol>:<listen-addr>")
			os.Exit(1)
		}

		prot = strings.ToLower(prot)
		listenAddr = strings.ToLower(listenAddr)

		log.Printf("adding %s listener on %s", prot, listenAddr)
		listenAddrs[prot] = listenAddr
	}

	if len(listenAddrs) == 0 {
		return // nothing to do
	}

	r := rproxy.New()

	tfHost, tfPort, tfUID := tinkerforgeConfigFromEnv()
	ctrl := tinkerforgefunc.NewTinkerforgeController(tfHost, tfPort, tfUID)

	funcs := cluster.NewFunctionStore()
	reg := cluster.NewRegistry(ctrl, funcs)

	cfg := cluster.ControllerConfigFromEnv()

	nodes, err := loadTopology()
	if err != nil {
		log.Fatalf("cluster topology: %v", err)
	}

	for _, n := range nodes {
		log.Printf("cluster: node %s (local=%v channel=%d) starts %s",
			n.ID, n.Local, n.Channel, n.Status)
		reg.AddNode(n)
	}

	// A no-op when power management is disabled; it logs which mode is in
	// effect either way, so a run's logs record what was measured.
	cluster.StartController(reg, cfg)

	if cfg.Enabled {
		// Make the relays agree with what the registry believes. Workers
		// start recorded as sleeping, but a relay holds its last position,
		// so a node left powered on would keep drawing while the leader
		// counted it as off.
		go reg.EnforceSleeping()
	} else {
		// Bring the cluster up in the background so the endpoints can start
		// listening immediately; waking every node can take a while.
		go reg.PrewarmAll()
	}

	// CoAP
	if listenAddr, ok := listenAddrs["coap"]; ok {
		log.Printf("starting coap server on %s", listenAddr)
		go coap.Start(r, listenAddr)
	}
	// HTTP
	if listenAddr, ok := listenAddrs["http"]; ok {
		log.Printf("starting http server on %s", listenAddr)
		go tfhttp.Start(r, reg, cfg, listenAddr)
	}
	// GRPC
	if listenAddr, ok := listenAddrs["grpc"]; ok {
		log.Printf("starting grpc server on %s", listenAddr)
		go grpc.Start(r, listenAddr)
	}

	server := http.NewServeMux()

	server.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		log.Printf("have request: %+v", req)

		buf := new(bytes.Buffer)
		buf.ReadFrom(req.Body)
		newStr := buf.String()

		log.Printf("have body: %s", newStr)

		var def struct {
			FunctionResource   string            `json:"name"`
			FunctionContainers []string          `json:"ips"`
			FunctionEnv        string            `json:"env"`
			FunctionThreads    int               `json:"threads"`
			FunctionEnvs       map[string]string `json:"envs"`
			FunctionZip        []byte            `json:"zip"`
		}

		err := json.Unmarshal([]byte(newStr), &def)

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		log.Printf("have definition: %+v", def)

		if def.FunctionResource[0] == '/' {
			def.FunctionResource = def.FunctionResource[1:]
		}

		if len(def.FunctionContainers) > 0 {
			// "ips" field not empty: add function
			log.Printf("adding %s", def.FunctionResource)
			err = r.Add(def.FunctionResource, def.FunctionContainers)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			fnDef := cluster.FunctionDef{
				Env:     def.FunctionEnv,
				Threads: def.FunctionThreads,
				Envs:    def.FunctionEnvs,
				Zip:     def.FunctionZip,
			}
			funcs.Set(def.FunctionResource, fnDef)

			// deploy-on-wake only reaches nodes at the moment they wake up,
			// so nodes that are already active need this function pushed
			// to them now
			reg.BroadcastFunction(def.FunctionResource, fnDef)

			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		} else {

			log.Printf("deleting %s", def.FunctionResource)
			err = r.Del(def.FunctionResource)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			funcs.Remove(def.FunctionResource)
			reg.BroadcastDelete(def.FunctionResource)
		}
	})

	// /nodes exposes the cluster's power state so a benchmark run can record
	// how many nodes were actually awake over time. Energy numbers alone
	// can't distinguish "the workload got cheaper" from "the scheduler
	// powered a node down", which is the whole effect under evaluation.
	server.HandleFunc("/nodes", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reg.ListNodes())
	})

	server.HandleFunc("/lastused", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		log.Printf("have request: %+v", req)

		lastUsed := r.GetFunctionLastUsed()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lastUsed)
	})

	log.Printf("listening on %s", rproxyListenAddress)
	err = http.ListenAndServe(rproxyListenAddress, server)

	if err != nil {
		log.Printf("%s", err)
	}

	log.Printf("exiting")
}
