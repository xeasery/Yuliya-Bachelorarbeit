package rproxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"sync"
	"time"
)

type Status uint32

const (
	StatusOK Status = iota
	StatusAccepted
	StatusNotFound
	StatusError
)

type RProxy struct {
	hosts            map[string][]string
	hl               sync.RWMutex
	functionLastUsed map[string]time.Time
}

func New() *RProxy {
	return &RProxy{
		hosts:            make(map[string][]string),
		functionLastUsed: make(map[string]time.Time),
	}

}

func (r *RProxy) Add(name string, ips []string) error {
	if len(ips) == 0 {
		return fmt.Errorf("no ips given")
	}

	r.hl.Lock()
	defer r.hl.Unlock()

	// if function exists, we should update!
	// if _, ok := r.hosts[name]; ok {
	// 	return fmt.Errorf("function already exists")
	// }

	r.hosts[name] = ips
	return nil
}

func (r *RProxy) Del(name string) error {
	r.hl.Lock()
	defer r.hl.Unlock()

	if _, ok := r.hosts[name]; !ok {
		return fmt.Errorf("function not found")
	}

	delete(r.hosts, name)
	return nil
}

func (r *RProxy) Call(name string, payload []byte, async bool, headers map[string]string) (Status, []byte) {

	handler, ok := r.hosts[name]

	if !ok {
		log.Printf("function not found: %s", name)
		return StatusNotFound, nil
	}

	log.Printf("have handlers: %s", handler)

	// choose random handler
	h := handler[rand.Intn(len(handler))]

	log.Printf("chosen handler: %s", h)

	req, err := http.NewRequest("POST", fmt.Sprintf("http://%s:8000/fn", h), bytes.NewBuffer(payload))
	if err != nil {
		log.Print(err)
		return StatusError, nil
	}
	for k, v := range headers {
		cleanedKey := cleanHeaderKey(k) // remove special chars from key
		req.Header.Set(cleanedKey, v)
	}

	// call function asynchronously
	if async {
		log.Printf("async request accepted")
		go func() {
			resp, err2 := http.DefaultClient.Do(req)
			if err2 != nil {
				return
			}
			resp.Body.Close()
			log.Printf("async request finished")
		}()
		return StatusAccepted, nil
	}

	// call function and return results
	log.Printf("sync request starting")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Print(err)
		return StatusError, nil
	}

	log.Printf("sync request finished")

	defer resp.Body.Close()
	res_body, err := io.ReadAll(resp.Body)

	if err != nil {
		log.Print(err)
		return StatusError, nil
	}

	r.hl.Lock()
	r.functionLastUsed[name] = time.Now()
	r.hl.Unlock()
	// log.Printf("have response for sync request: %s", res_body)

	return StatusOK, res_body
}
func cleanHeaderKey(key string) string {
	// a regex pattern to match special characters
	re := regexp.MustCompile(`[:()<>@,;:\"/[\]?={} \t]`)
	// Replace special characters with an empty string
	return re.ReplaceAllString(key, "")
}

// why not just return r.functionLastUsed? because we don't want to return a reference to the original map, which could be modified by the caller. By creating a copy of the map and returning it, we ensure that the caller cannot modify the original
func (r *RProxy) GetFunctionLastUsed() map[string]time.Time {
	r.hl.Lock()
	defer r.hl.Unlock()

	copy := make(map[string]time.Time)
	for k, v := range r.functionLastUsed {
		copy[k] = v
	}

	return copy
}
