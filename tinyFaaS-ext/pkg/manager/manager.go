package manager

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xeasery/Yuliya-Bachelorarbeit/tinyFaaS-ext/pkg/util"
)

const (
	TmpDir = "./tmp"
)

type ManagementService struct {
	id                    string
	backend               Backend
	functionHandlers      map[string]Handler
	functionHandlersMutex sync.Mutex
	rproxyListenAddress   string
	rproxyPort            map[string]int
	rproxyConfigPort      int
	functionLastUsed      map[string]time.Time
}

type Backend interface {
	Create(name string, env string, threads int, filedir string, envs map[string]string) (Handler, error)
	Stop() error
	Ping() error
}

type Handler interface {
	IPs() []string
	Start() error
	Destroy() error
	Logs() (io.Reader, error)
}

func New(id string, rproxyListenAddress string, rproxyPort map[string]int, rproxyConfigPort int, tfBackend Backend) *ManagementService {

	ms := &ManagementService{
		id:                  id,
		backend:             tfBackend,
		functionHandlers:    make(map[string]Handler),
		rproxyListenAddress: rproxyListenAddress,
		rproxyPort:          rproxyPort,
		rproxyConfigPort:    rproxyConfigPort,
		functionLastUsed:    make(map[string]time.Time),
	}

	return ms
}

func (ms *ManagementService) createFunction(name string, env string, threads int, funczip []byte, subfolderPath string, envs map[string]string) (string, error) {

	// only allow alphanumeric characters
	if !util.IsAlphaNumeric(name) {
		return "", fmt.Errorf("function name %s contains non-alphanumeric characters", name)
	}

	// make a uuidv4 for the function
	uuid, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	log.Println("creating function", name, "with uuid", uuid.String())

	// create a new function handler

	p := path.Join(TmpDir, uuid.String())

	err = os.MkdirAll(p, 0777)

	if err != nil {
		return "", err
	}

	log.Println("created folder", p)

	// write zip to file
	zipPath := path.Join(TmpDir, uuid.String()+".zip")
	err = os.WriteFile(zipPath, funczip, 0777)

	if err != nil {
		return "", err
	}

	err = util.Unzip(zipPath, p)

	if err != nil {
		return "", err
	}

	defer func() {
		// remove folder
		err = os.RemoveAll(p)
		if err != nil {
			log.Println("error removing folder", p, err)
		}

		err = os.Remove(zipPath)
		if err != nil {
			log.Println("error removing zip", zipPath, err)
		}

		log.Println("removed folder", p)
		log.Println("removed zip", zipPath)
	}()

	if subfolderPath != "" {
		p = path.Join(p, subfolderPath)
	}

	// if function already exists, keep it while deploying the new version
	var oldHandler Handler
	if existingHandler, ok := ms.functionHandlers[name]; ok {
		oldHandler = existingHandler
	}

	// create new function handler
	ms.functionHandlersMutex.Lock()
	defer ms.functionHandlersMutex.Unlock()

	fh, err := ms.backend.Create(name, env, threads, p, envs)

	if err != nil {
		return "", err
	}

	ms.functionHandlers[name] = fh

	err = ms.functionHandlers[name].Start()

	if err != nil {
		// container did not start properly...
		return "", err
	}

	// tell rproxy about the new function
	// curl -X POST http://localhost:80/add -d '{"name": "<name>", "ips": ["<ip1>", "<ip2>"]}'
	// env/threads/envs/zip are included so the cluster leader can redeploy
	// this function to other nodes when they wake from sleep
	d := struct {
		FunctionName    string            `json:"name"`
		FunctionIPs     []string          `json:"ips"`
		FunctionEnv     string            `json:"env"`
		FunctionThreads int               `json:"threads"`
		FunctionEnvs    map[string]string `json:"envs"`
		FunctionZip     []byte            `json:"zip"`
	}{
		FunctionName:    name,
		FunctionIPs:     fh.IPs(),
		FunctionEnv:     env,
		FunctionThreads: threads,
		FunctionEnvs:    envs,
		FunctionZip:     funczip,
	}

	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}

	log.Println("telling rproxy about new function", name, "with ips", fh.IPs(), ":", d)

	resp, err := http.Post(fmt.Sprintf("http://%s:%d", ms.rproxyListenAddress, ms.rproxyConfigPort), "application/json", bytes.NewBuffer(b))
	if err != nil && !errors.Is(err, io.EOF) {
		log.Println("error telling rproxy about new function", name, err)
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("rproxy returned status code %d", resp.StatusCode)
	}

	defer resp.Body.Close()

	r, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	log.Println("rproxy response:", string(r))

	// destroy the old handler if it exists
	if oldHandler != nil {
		err = oldHandler.Destroy()
		if err != nil {
			return "", err
		}
	}

	return name, nil
}

func (ms *ManagementService) Logs() (io.Reader, error) {

	var logs bytes.Buffer

	for name := range ms.functionHandlers {
		l, err := ms.LogsFunction(name)
		if err != nil {
			return nil, err
		}

		_, err = io.Copy(&logs, l)
		if err != nil {
			return nil, err
		}

		logs.WriteString("\n")
	}

	return &logs, nil
}

func (ms *ManagementService) LogsFunction(name string) (io.Reader, error) {

	fh, ok := ms.functionHandlers[name]
	if !ok {
		return nil, fmt.Errorf("function %s not found", name)
	}

	return fh.Logs()
}

func (ms *ManagementService) List() []string {
	list := make([]string, 0, len(ms.functionHandlers))
	for name := range ms.functionHandlers {
		list = append(list, name)
	}

	return list
}

func (ms *ManagementService) Wipe() error {
	for name := range ms.functionHandlers {
		log.Println("destroying function", name)
		ms.Delete(name)
	}

	return nil
}
func (ms *ManagementService) Delete(name string) error {

	fh, ok := ms.functionHandlers[name]
	if !ok {
		return fmt.Errorf("function %s not found", name)
	}

	log.Println("destroying function", name)

	ms.functionHandlersMutex.Lock()
	defer ms.functionHandlersMutex.Unlock()

	err := fh.Destroy()
	if err != nil {
		return err
	}

	// tell rproxy about the delete function
	// curl -X POST http://localhost:80 -d '{"name": "<name>"}'
	d := struct {
		FunctionName string `json:"name"`
	}{
		FunctionName: name,
	}

	b, err := json.Marshal(d)
	if err != nil {
		return err
	}

	log.Println("telling rproxy about deleted function", name)

	resp, err := http.Post(fmt.Sprintf("http://%s:%d", ms.rproxyListenAddress, ms.rproxyConfigPort), "application/json", bytes.NewBuffer(b))

	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rproxy returned status code %d", resp.StatusCode)
	}

	defer resp.Body.Close()

	r, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	log.Println("rproxy response:", string(r))

	delete(ms.functionHandlers, name)

	return nil
}

func (ms *ManagementService) Upload(name string, env string, threads int, zipped string, envs map[string]string) (string, error) {

	// b64 decode zip
	zip, err := base64.StdEncoding.DecodeString(zipped)
	if err != nil {
		// w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return "", err
	}

	// create function handler
	n, err := ms.createFunction(name, env, threads, zip, "", envs)

	if err != nil {
		// w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return "", err
	}

	// return success
	// w.WriteHeader(http.StatusOK)
	r := ""
	for prot, port := range ms.rproxyPort {
		r += fmt.Sprintf("%s://%s:%d/%s\n", prot, ms.rproxyListenAddress, port, n)
	}

	return r, nil
}

func (ms *ManagementService) UrlUpload(name string, env string, threads int, funcurl string, subfolder string, envs map[string]string) (string, error) {

	// download url
	resp, err := http.Get(funcurl)
	if err != nil {
		// w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return "", err
	}

	// reading body to memory
	// not the smartest thing
	zip, err := io.ReadAll(resp.Body)

	if err != nil {
		// w.WriteHeader(http.StatusBadRequest)
		log.Println(err)
		return "", err
	}

	// create function handler
	n, err := ms.createFunction(name, env, threads, zip, subfolder, envs)

	if err != nil {
		// w.WriteHeader(http.StatusInternalServerError)
		log.Println(err)
		return "", err
	}

	// return success
	// w.WriteHeader(http.StatusOK)
	r := ""
	for prot, port := range ms.rproxyPort {
		r += fmt.Sprintf("%s://%s:%d/%s\n", prot, ms.rproxyListenAddress, port, n)
	}

	return r, nil
}

// Healthy reports whether the backend (e.g. the Docker daemon) is actually
// reachable, not just that the management service's own HTTP server is up.
func (ms *ManagementService) Healthy() error {
	return ms.backend.Ping()
}

func (ms *ManagementService) Stop() error {
	err := ms.Wipe()
	if err != nil {
		return err
	}

	return ms.backend.Stop()
}

// functionIdleTimeout is how long a function may go unused before this node
// tears it down -- tinyFaaS's own function-level scale-to-zero.
//
// In a cluster that already scales by powering whole nodes down, the two
// mechanisms fight: the leader considers a node active and keeps routing to
// it, while the node quietly deletes the function underneath, so requests
// arrive at a node that no longer has anything to serve them. With the
// leader's idle timeout at 60s and this at 30s, there is a guaranteed window
// where that happens.
//
// FUNCTION_IDLE_TIMEOUT=0 disables the reaper, which is what a cluster wants:
// node power management is the scale-to-zero. Any other duration overrides
// the default. Left alone, behaviour is unchanged from upstream.
func functionIdleTimeout() time.Duration {
	const fallback = 30 * time.Second

	v := os.Getenv("FUNCTION_IDLE_TIMEOUT")
	if v == "" {
		return fallback
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		// Bare "0" is not a valid duration string but is the obvious way to
		// ask for "off", so accept it.
		if v == "0" {
			return 0
		}
		log.Printf("controller: invalid FUNCTION_IDLE_TIMEOUT %q, using %s", v, fallback)
		return fallback
	}
	return d
}

func (ms *ManagementService) StartController() {
	idleTimeout := functionIdleTimeout()

	if idleTimeout <= 0 {
		log.Printf("controller: function idle teardown disabled (FUNCTION_IDLE_TIMEOUT=%s)",
			os.Getenv("FUNCTION_IDLE_TIMEOUT"))
	} else {
		log.Printf("controller: tearing down functions unused for %s", idleTimeout)
	}

	go func() {
		for {
			time.Sleep(30 * time.Second)

			//if more than one func
			ms.functionHandlersMutex.Lock()
			count := len(ms.functionHandlers)
			ms.functionHandlersMutex.Unlock()

			log.Printf("controller: currently managing %d functions\n", count)

			if count == 0 {
				log.Println("controller: idle system")
			} else if count < 3 {
				log.Println("controller: active system")
				// scaling
			} else {
				log.Println("controller: overloaded system")
			}

			// Nothing below this point does anything when teardown is
			// disabled, and polling /lastused every 30s just fills the log.
			if idleTimeout <= 0 {
				continue
			}

			//fetch fromn proxy
			resp, err := http.Get(fmt.Sprintf("http://%s:%d/lastused", ms.rproxyListenAddress, ms.rproxyConfigPort))
			if err != nil {
				log.Println("controller: error getting last used times from rproxy:", err)
				continue
			}

			var functionLastUsed map[string]time.Time
			err = json.NewDecoder(resp.Body).Decode(&functionLastUsed)
			if err != nil {
				log.Println("controller: error decoding last used times from rproxy:", err)
				continue
			}

			//scale down functions/computers
			//delete functions that have not been used for more than 30 seconds
			toDelete := []string{}
			for name, lastUsed := range functionLastUsed {
				if time.Since(lastUsed) > idleTimeout {
					log.Printf("controller: function %s has not been used for %s, deleting\n", name, time.Since(lastUsed))
					toDelete = append(toDelete, name)
				}
			}

			for _, name := range toDelete {

				ms.functionHandlersMutex.Lock()
				_, exists := ms.functionHandlers[name]
				ms.functionHandlersMutex.Unlock()

				if !exists {
					log.Printf("controller: function %s already deleted\n", name)
					continue
				}

				log.Printf("controller: deleting function %s\n", name)
				err := ms.Delete(name)
				if err != nil {
					log.Printf("controller: error deleting function %s: %s\n", name, err)
				}
			}

		}
	}()

}
