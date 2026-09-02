# tinyFaaS: A Lightweight FaaS Platform for Edge Environments

tinyFaaS is a lightweight FaaS (Function-as-a-Service) platform for edge environment with a focus on performance in constrained environments.

## Research

To use tinyFaaS in the version used in our paper, use `git checkout v0.1`.
If you use this software in a publication, please cite it as:

### Text

T. Pfandzelter and D. Bermbach, **tinyFaaS: A Lightweight FaaS Platform for Edge Environments**, Proceedings of the 2020 IEEE International Conference on Fog Computing (ICFC '20), Sydney, Australia, 2020, pp. 17-24, DOI: 10.1109/ICFC49376.2020.00011.

### BibTeX

```bibtex
@inproceedings{pfandzelter_tinyfaas:_2020,
    author = "Pfandzelter, Tobias and Bermbach, David",
    title = "tinyFaaS: A Lightweight FaaS Platform for Edge Environments",
    booktitle = "Proceedings of the 2020 IEEE International Conference on Fog Computing (ICFC '20)",
    year = 2020,
    publisher = "IEEE",
    pages = "17--24",
    doi = "10.1109/ICFC49376.2020.00011"
}
```

For a full list of publications, please see [our website](https://www.tu.berlin/en/mcc/research/publications).

### License

The code in this repository is licensed under the terms of the [MIT](./LICENSE) license.

## Instructions

**Disclaimer**: Please note that this will use your computer's Docker instance to manage containers and will allow anyone in your network to start Docker containers with arbitrary code.
If you don't know what this means you do _not_ want to run this on your computer.
Additionally, note that this software is provided as a research prototype and is not production-ready.

### About

tinyFaaS comprises the _management service_, the _reverse proxy_, and a number of _function handlers_.
In order to run tinyFaaS, the management service has to be deployed.
It will then automatically start the reverse proxy.
Once a function is deployed to tinyFaaS, function handlers are created automatically.

### Prerequisites

Before you get started, make sure you have the following dependencies installed:

- Go (>=v1.22) to compile management service and reverse proxy
- Docker (>=v24)
- Make
- a writable directory (tinyFaaS writes temporary files to a `./tmp` directory)

Note that tinyFaaS is intended for Linux hosts (`x86_64` and `arm64`).
Due to limitations of Docker Desktop for Mac, installing and running [`docker-mac-net-connect`](https://github.com/chipmk/docker-mac-net-connect) is necessary to run tinyFaaS on macOS hosts.
Running tinyFaaS on Windows computers (native or through WSL) is probably possible but has not been tested and is thus not recommended.

### Getting Started

Start tinyFaaS with:

```sh
make start
```

The reverse proxy will be started automatically.
Please note that you cannot use tinyFaaS until the reverse proxy is running.

### Managing Functions

To manage functions on tinyFaaS, use the included scripts included in `./src/scripts`.

To upload a function, run `upload.sh {FOLDER} {NAME} {ENV} {THREADS}`, where `{FOLDER}` is the path to your function code, `{NAME}` is the name for your function, `{ENV}` is the environment you would like to use (`python3`, `nodejs`, or `binary`), and `{THREADS}` is a number specifying the number of function handlers for your function.
For example, you might call `./scripts/upload.sh "./test/fns/sieve-of-eratosthenes" "sieve" "nodejs" 1` to upload the _sieve of Eratosthenes_ example function included in this repository.
This requires the `zip`, `base64`, and `curl` utilities.

Alternatively, you can also upload functions from a zipped file available at some URL.
Use the included script as a starting point: `uploadURL.sh {URL} {NAME} {ENV} {THREADS} {SUBFOLDER_PATH}`, where `{URL}` is the URL to a zip that has your function code, `{SUBFOLDER_PATH}` is the folder of the code within that zip (use `/` if the code is in the top-level), `{NAME}` is the name for your function, `{ENV}` is the environment, and `{THREADS}` is a number specifying the number of function handlers for your function.
For example, you might call `uploadURL.sh "https://github.com/OpenFogStack/tinyFaas/archive/main.zip" "tinyFaaS-main/test/fns/sieve-of-eratosthenes" "sieve" "nodejs" 1` to upload the _sieve of Eratosthenes_ example function included in this repository.

To get a list of existing functions, run `list.sh`.

To delete a function, run `delete.sh {NAME}`, where `{NAME}` is the name of the function you want to remove.

Additionally, we provide scripts to read logs from your function and to wipe all functions from tinyFaaS.

### Writing Functions

This tinyFaaS prototype only supports functions written for NodeJS 20, Python 3.9, and binary functions.
A good place to get started with writing functions is the selection of test functions in [`./test/fns`](./test/fns/).
HTTP headers and GRPC Metadata are accessible in NodeJS and Python functions as key values. Check the "show-headers" test functions for more information.

#### NodeJS 20

Your function must be supplied as a Node module with the name `fn` that exports a single function that takes the `req` and `res` parameters for request and response, respectively.
`res` supports the `send()` function that has one parameter, a string that is passed to the client as-is.

#### Python 3.9

Your function must be supplied as a file named `fn.py` that exposes a method `fn` that is invoked for every function invocation.
This method must accept a string as an input (that can also be `None`) and must provide a string as a return value.
You may also provide a `requirements.txt` file from which dependencies will be installed alongside your function.
Any other data you provide will be available.

#### Binary

Your function must be provided as a `fn.sh` shell script that is invoked for every function call.
This shell script may also call other binaries as needed.
Input data is provided from `stdin`.
Output responses should be provided on `stdout`.

### Calling Functions

tinyFaaS supports different application layer protocols at its reverse proxy.
Different protocols are useful for different use-cases: CoAP for lightweight communication, e.g., for IoT devices; HTTP to support traditional web applications; GRPC for inter-process communication.

#### CoAP

To call a tinyFaaS function using its CoAP endpoint, make a GET or POST request to `coap://{HOST}:{PORT}/{NAME}` where `{HOST}` is the address of the tinyFaaS host, `{PORT}` is the port for the tinyFaaS CoAP endpoint (default is `5683`), and `{NAME}` is the name of your function.
You may include data in any form you want, it will be passed to your function.

Unfortunately, [`curl` does not yet support CoAP](https://curl.se/mail/lib-2018-05/0017.html), but [a number](https://github.com/coapjs/coap-cli) [of other](https://aiocoap.readthedocs.io/en/latest/tools.html) [tools are available](https://fitbit.github.io/golden-gate/tools/coap_client.html).

#### HTTP

To call a tinyFaaS function using its HTTP endpoint, make a GET or POST request to `http://{HOST}:{PORT}/{NAME}` where `{HOST}` is the address of the tinyFaaS host, `{PORT}` is the port for the tinyFaaS HTTP endpoint (default is `80`), and `{NAME}` is the name of your function.
You may include data in any form you want, it will be passed to your function.

TLS is not supported (but contributions are welcome).

To make an asynchronous request, pass the `X-tinyFaaS-Async` header with any value.
An asynchronous request means the client will receive a `202` response code immediately and no function results will be sent back.

```sh
curl --header "X-tinyFaaS-Async: true" "http://localhost:8000/sieve"
```

#### gRPC

To use the gRPC endpoint, compile the `tinyfaas` protocol buffer (included in [`./pkg/grpc/tinyfaas`](./pkg/grpc/tinyfaas)) for your programming language and import it into your application.
We already provide compiled versions for Go and Python in that directory.
Specify the tinyFaaS host and port (default is `9000`) for the GRPC endpoint and use the `Request` function with the `functionIdentifier` being your function's name and the `data` field including data in any form you want.

### Removing tinyFaaS

When you stop the management service with `SIGINT` (`Ctrl+C`), the reverse proxy and all function handlers should be stopped.
You can also use:

```bash
make clean
```

OR

```bash
docker rm -f $$(docker ps -a -q --filter label=tinyFaaS)
docker network rm $$(docker network ls -q --filter label=tinyFaaS)
docker rmi $$(docker image ls -q --filter label=tinyFaaS)
rm -rf ./tmp
```

### Cluster Mode and Hardware Orchestration

This fork can spread invocations across a cluster of nodes and power those
nodes on and off via Tinkerforge relays according to demand.

Describe the cluster in a JSON file (see
[`deploy/nodes.example.json`](./deploy/nodes.example.json)) and point
`NODES_CONFIG` at it. Without that variable, tinyFaaS runs single-node exactly
as upstream does.

#### Every node runs this build, not upstream

Install the binary from this repository on **all** cluster machines, leader and
workers alike. Workers are not stock tinyFaaS nodes: the leader decides a
worker is ready by polling `/health` on its management API, and that endpoint
does not exist upstream. A worker running upstream tinyFaaS powers on, never
answers `/health`, fails the readiness check and is marked dead — so every
wake fails.

The same binary covers both roles; only the environment differs:

| Role | Environment | Behaviour |
| --- | --- | --- |
| Leader | `NODES_CONFIG`, `TINKERFORGE_UID`, `POWER_AWARE` | Schedules across the cluster, powers nodes on and off. |
| Worker | *(none)* | Plain single-node tinyFaaS. Never power-cycles anything. |

Workers need no configuration and no Tinkerforge connection: with
`NODES_CONFIG` unset the registry holds only the local node, which is never
power-cycled, so the relay is never contacted.

```bash
NODES_CONFIG=./deploy/nodes.json TINKERFORGE_UID=ABC ./rproxy ...
```

Exactly one node must be marked `"local": true` — the leader running the
management service. It is never power-cycled, since it is what would have to
issue the power-off.

#### Relays and channels

An Industrial Dual Relay Bricklet switches **two** nodes, so a cluster with
more than two workers needs more than one bricklet. Each node names its relay
with `relay_uid` and its channel *on that relay* (`0` or `1`) — `channel` is
not a cluster-wide index:

| Node | `relay_uid` | `channel` |
| --- | --- | --- |
| edge-1 | relay A | 0 |
| edge-2 | relay A | 1 |
| edge-3 | relay B | 0 |
| edge-4 | relay B | 1 |

`relay_uid` may be omitted on a single-relay cluster, where `TINKERFORGE_UID`
is used instead. Find UIDs with Brick Viewer.

Two configurations are rejected at startup rather than at first wake: two
nodes sharing a relay *and* channel (one relay silently switching two nodes),
and a channel outside `0..1` — which is what numbering four workers `0,1,2,3`
on a single relay would produce, and which otherwise fails only mid-benchmark
as a node marked dead for no visible reason.

#### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `NODES_CONFIG` | unset | Path to the topology file. Unset = single-node. |
| `POWER_AWARE` | `true` | `false` disables power management (see below). |
| `NODE_IDLE_TIMEOUT` | `60s` | Idle time before a node is powered off. |
| `CONTROLLER_INTERVAL` | `10s` | How often the controller re-evaluates. |
| `MIN_ACTIVE_NODES` | `1` | Floor the controller will not scale below. |
| `LOAD_THRESHOLD` | `10` | In-flight requests above which a node stops being preferred. |
| `TINKERFORGE_HOST` / `TINKERFORGE_PORT` / `TINKERFORGE_UID` | `localhost` / `4223` / — | Relay connection. |
| `NODE_BOOT_TIMEOUT` | `3m` | How long a woken node has to answer `/health`. |
| `DEPLOY_TIMEOUT` | `5m` | How long a function deploy to a woken node may take. |
| `FUNCTION_READY_TIMEOUT` | `60s` | How long a deployed function has to start answering before the wake is treated as failed. |

##### Why a node is not active the moment its function deploys

A node's management API returns OK from `/upload` once it has accepted the
function and started its containers — not once those containers are
listening. Marking the node active there hands the scheduler a node that
cannot serve yet, and under load the backlog arrives immediately: measured on
a 4-node cluster at 10 req/s, 4.7% of a run's requests failed with 500s in
the first minute, almost all of them on the node that woke first and so took
the backlog alone.

The wake therefore ends by polling the function on the address the scheduler
will actually route to, with the same method it forwards with, until it
answers.

The probe **fails open**: a function that has not answered within
`FUNCTION_READY_TIMEOUT` is logged and the node is activated anyway. This is
deliberate. A probe that is wrong about a healthy node would otherwise cost
the entire cluster, while the gap it guards costs a few seconds of requests
on one node — and the first version of this check was wrong that way, probing
with GET where the scheduler forwards POST, which took the error rate from
4.7% to 100%. Only deployment failure, which is unambiguous, keeps a node out
of the pool.

#### Baseline vs. power-aware

Evaluating hardware orchestration needs both arms of the comparison, differing
only in whether nodes are ever powered down:

```bash
POWER_AWARE=true  NODES_CONFIG=... ./rproxy ...   # power-aware
POWER_AWARE=false NODES_CONFIG=... ./rproxy ...   # always-on baseline
```

In baseline mode the controller never runs, every node starts active, and the
cluster is powered up at startup. Nodes are brought up explicitly rather than
left to wake on first request — waking on demand is still hardware
orchestration, just with a different trigger, so it would not be a baseline.

#### Observing the cluster

`GET /nodes` on the management API returns each node's current status, load and
last-used time. Responses also carry an `X-tinyFaaS-Node` header naming the node
that served the request. Both exist so a benchmark can attribute invocations to
nodes and record how many were awake over time — neither is otherwise
observable from outside.

### Specifying Ports

By default, tinyFaaS will use the following ports:

| Port | Protocol | Description        |
| ---- | -------- | ------------------ |
| 8080 | TCP      | Management Service |
| 5683 | UDP      | CoAP Endpoint      |
| 8000 | TCP      | HTTP Endpoint      |
| 9000 | TCP      | GRPC Endpoint      |

To change the port of the management service, change the port binding in the `docker run` command.

To change or deactivate the endpoints of tinyFaaS, you can use the `COAP_PORT`, `HTTP_PORT`, and `GRPC_PORT` environment variables, which must be passed to the management service Docker container.
Specify `-1` to deactivate a specific endpoint.
For example, to use `6000` as the port for the CoAP and deactivate GRPC, run the management service with this command:

```bash
docker run --env COAP_PORT=6000 --env GRPC_PORT=-1 -v /var/run/docker.sock:/var/run/docker.sock -p 8080:8080 --name tinyfaas-mgmt -d tinyfaas-mgmt tinyfaas-mgmt
```

### Tests

The tests in [`./test`](./test) test the end-to-end functionality of tinyFaaS and are expected to complete successfully.
We use these tests during development to ensure no patches break any functionality.
The tests can also serve as documentation on the expected behavior of tinyFaaS.

Running the tests requires:

- Python >3.10 with the `venv` module
- a tinyFaaS binary built for your host

Create a virtual environment for Python and install the necessary dependencies for CoAP and gRPC:

```sh
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install -r test/requirements.txt
```

If you do not install these requirements, test runs are limited to invocations with HTTP.

Run the tests with:

```sh
$ make test
.............
----------------------------------------------------------------------
Ran 13 tests in 28.063s

OK
```

Tests will output a `.` (dot) for successful tests and `E` or `F` for failed tests.
tinyFaaS output will be written to `tf_test.out`.

On macOS, [`docker-mac-net-connect`](https://github.com/chipmk/docker-mac-net-connect) is necessary to run tinyFaaS.
There is a [known issue in `docker-mac-net-connect`](https://github.com/chipmk/docker-mac-net-connect/issues/36) that silently breaks the tunnel when Docker Desktop enters its resource saver mode.
If you find that tinyFaaS does not start properly on your macOS host, try restarting the tunnel:

```sh
sudo brew services restart docker-mac-net-connect
```
