package http

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/cluster"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/rproxy"
)

func Start(r *rproxy.RProxy, reg *cluster.Registry, listenAddr string) {

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path

		for p != "" && p[0] == '/' {
			p = p[1:]
		}

		async := req.Header.Get("X-tinyFaaS-Async") != ""

		log.Printf("have request for path: %s (async: %v)", p, async)

		req_body, err := io.ReadAll(req.Body)

		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Print(err)
			return
		}

		headers := make(map[string]string)
		for k, v := range req.Header {
			headers[k] = v[0]
		}

		nodes := reg.ListNodes()
		node, ok := cluster.PickNode(nodes)

		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		if err := reg.ActivateNode(node.ID); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		reg.IncLoad(node.ID)
		defer reg.DecLoad(node.ID)

		// tell the client which node served this request: without it there is
		// no way to attribute an invocation to a node from the outside, and
		// therefore no way to observe the scheduler waking and draining nodes
		w.Header().Set("X-tinyFaaS-Node", node.ID)

		if node.Local {
			status, res := r.Call(p, req_body, async, headers)

			// only mark usage if successful update last used

			if status == rproxy.StatusOK || status == rproxy.StatusAccepted {
				reg.TouchNode(node.ID)
			}

			switch status {
			case rproxy.StatusOK:
				w.WriteHeader(http.StatusOK)
				w.Write(res)
			case rproxy.StatusAccepted:
				w.WriteHeader(http.StatusAccepted)
			case rproxy.StatusNotFound:
				w.WriteHeader(http.StatusNotFound)
			case rproxy.StatusError:
				w.WriteHeader(http.StatusInternalServerError)
			}

			return

		}
		log.Printf("forwarding to remote node %s at %s", node.ID, node.Address)

		url := fmt.Sprintf("http://%s/%s", node.Address, p)

		forwardReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(req_body))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		for k, v := range headers {
			forwardReq.Header.Set(k, v)
		}

		resp, err := http.DefaultClient.Do(forwardReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
			reg.TouchNode(node.ID)
		}

		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)

	})

	log.Printf("Starting HTTP server on %s", listenAddr)
	err := http.ListenAndServe(listenAddr, mux)

	if err != nil {
		log.Fatal(err)
	}

	log.Print("HTTP server stopped")

}
