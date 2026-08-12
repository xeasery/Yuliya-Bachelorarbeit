# Cluster Setup

Bringing up the 5-machine cluster for the hardware-orchestration experiments:
one leader and four workers, whose power is switched by Tinkerforge relays.

Work through this in order. Each step ends with a check — do not move on
until it passes, because most failures here are silent later rather than loud
now.

## What you are building

```
                    ┌──────────────────────────────────────┐
   your Mac ─ ssh ─▶│ LEADER                               │
   (k6 client)      │  tinyFaaS mgmt :8080                 │
                    │  rproxy        :8000 fn  :8081 /nodes│
                    │  brickd :4223                        │
                    └───┬──────────────────────────┬───────┘
                        │ relay A          relay B │
                   ┌────┴────┐                ┌────┴────┐
                 ch0│      ch1│             ch0│     ch1│
              ┌─────▼─┐  ┌────▼──┐       ┌─────▼─┐ ┌────▼──┐
              │edge-1 │  │edge-2 │       │edge-3 │ │edge-4 │
              └───────┘  └───────┘       └───────┘ └───────┘
                     Voltage/Current Bricklet on the shared supply
```

Ports (tinyFaaS defaults):

| Port | Server | Used for |
| --- | --- | --- |
| 8000 | rproxy | function invocations |
| 8080 | management service | `/health`, `/upload`, `/delete` |
| 8081 | rproxy config server | `/nodes`, `/lastused` |
| 4223 | brickd | Tinkerforge (leader only) |

**8080 and 8081 are different servers.** The leader health-checks workers on
8080; the benchmark client reads cluster power state from 8081.

## 0. Before you wire anything: the SD card problem

The controller powers a node off by opening the relay. There is no
`shutdown -h now` — it is a hard cut, every time. Across an Azure trace run
that is on the order of a hundred unclean power cuts per node, and SD card
corruption is the expected outcome, usually partway through an experiment.

**Put the workers on a read-only root filesystem** before running anything
long:

```bash
sudo raspi-config      # Performance Options → Overlay File System → enable
```

Docker needs a writable data directory, so give it one that is not on the
overlay — a second partition, or an external USB SSD, mounted at
`/var/lib/docker`. An SSD is worth it here anyway: the repeated image pulls
and container churn are hard on SD cards even without the power cuts.

The alternative is a graceful shutdown before each cut, but that adds ~15 s
to every power-off, and that delay lands directly in the latency and energy
figures you are trying to measure — it would distort the result rather than
just cost time.

## 1. Build once

Every machine runs the **same binary from this repository**. Workers are not
stock tinyFaaS nodes: the leader decides a worker is ready by polling
`/health`, and that endpoint does not exist upstream. A worker running
upstream tinyFaaS powers on, never answers, fails readiness, and is marked
dead — so every wake fails.

```bash
cd tinyFaaS-ext
make build            # produces tinyfaas-linux-arm64 (match your Pi's arch)
```

Role is decided entirely by environment, not by the binary:

| Role | Environment | Behaviour |
| --- | --- | --- |
| Leader | `NODES_CONFIG`, `TINKERFORGE_UID`, `POWER_AWARE` | Schedules, powers nodes |
| Worker | *(none)* | Plain single-node tinyFaaS |

## 2. Each worker (×4)

Workers need no configuration, no `nodes.json`, and no brickd.

```bash
# Docker, with the service user able to reach the socket
sudo apt install -y docker.io
sudo useradd -r -m -d /opt/tinyfaas tinyfaas || true
sudo usermod -aG docker tinyfaas

sudo mkdir -p /opt/tinyfaas
sudo cp tinyfaas-linux-arm64 /opt/tinyfaas/tinyfaas-mgmt
sudo chown -R tinyfaas:tinyfaas /opt/tinyfaas

sudo cp tinyFaaS-ext/deploy/tinyfaas.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tinyfaas
```

**Check — and do this with the power, not with `reboot`:**

```bash
# from the leader
curl -f http://<worker-ip>:8080/health && echo OK
```

Now physically cut and restore power to that worker, wait, and run the same
curl again without touching the machine. If it does not come back on its own,
nothing else in this setup will work — the entire design assumes a node
recovers unattended from a power cut.

## 3. The leader

Install brickd and both relay bricklets, then find their UIDs:

```bash
sudo apt install -y brickd brickv
brickv     # note the UID of each Industrial Dual Relay Bricklet,
           # and of the Voltage/Current Bricklet 2.0
```

Write the topology:

```bash
cp tinyFaaS-ext/deploy/nodes.example.json /opt/tinyfaas/nodes.json
```

Fill in the four workers' IPs and the two relay UIDs. `channel` is the channel
**on that relay** (0 or 1), not a cluster-wide index:

| Node | `relay_uid` | `channel` |
| --- | --- | --- |
| edge-1 | relay A | 0 |
| edge-2 | relay A | 1 |
| edge-3 | relay B | 0 |
| edge-4 | relay B | 1 |

Numbering them 0,1,2,3 on one relay is rejected at startup, since a dual relay
has only two channels.

Give the leader its environment through a systemd drop-in rather than editing
the unit file:

```bash
sudo systemctl edit tinyfaas
```
```ini
[Service]
Environment=NODES_CONFIG=/opt/tinyfaas/nodes.json
Environment=TINKERFORGE_UID=<relay A UID>
Environment=POWER_AWARE=true
```
```bash
sudo systemctl daemon-reload && sudo systemctl restart tinyfaas
```

**Check:**

```bash
curl -s localhost:8081/nodes | python3 -m json.tool
```

You should see five nodes: the leader `active`, four workers `sleeping`. If
you get a connection refused, you are on the wrong port — `/nodes` is 8081,
not 8080.

## 4. Pre-build the function image on every worker

**Do not skip this.** The `edge` function needs numpy and Pillow, and the
first image build on a Pi takes minutes. On a wake, a deploy that fails sends
the node back to sleep, so an uncached image gives you: wake → build times out
→ sleep → next request wakes it again → forever.

Building it once while the node is up puts it in Docker's layer cache, and
later wakes reuse it.

With all four workers powered on:

```bash
cd tinyFaaS-ext
./scripts/upload.sh ./myfunc/edge edge python3 1
```

The leader broadcasts it to every active node. **Check each worker
individually:**

```bash
for ip in <edge-1> <edge-2> <edge-3> <edge-4>; do
  echo -n "$ip: "
  base64 -w0 ../k6_client_BA/assets/input.jpg \
    | curl -s --data-binary @- "http://$ip:8000/edge" | head -c 20
  echo
done
```

Each should return base64 PNG data, not `ERROR:` and not a 404.

Watch that first build. If `pip install` **fails** rather than merely being
slow, you have hit the Alpine/musl problem: the runtime image is
`python:3.11-alpine`, and if numpy or Pillow have no musllinux wheel for your
architecture, pip tries to compile them and the image has no compiler. Stop
and fix the base image — do not work around it per-run.

## 5. End-to-end smoke test

Wake a sleeping node deliberately and watch it happen:

```bash
# on the leader, watch the log
journalctl -u tinyfaas -f

# from your Mac, one request
base64 -w0 k6_client_BA/assets/input.jpg \
  | curl -s -D- --data-binary @- http://<leader>:8000/edge | head -20
```

Expect in the log: `activating node edge-N`, a relay power-on, readiness
polling, function deploy, `node edge-N is active`. Expect in the response
headers: `X-tinyFaaS-Node: edge-N`.

Then leave it idle for longer than `NODE_IDLE_TIMEOUT` (60 s default) and
confirm `controller: scaling down node edge-N` appears and `/nodes` shows it
sleeping again.

## 6. Energy logger

On the machine with the Voltage/Current Bricklet — measuring the **shared
supply**, so the figure is whole-cluster power:

```bash
git clone <your energy-measurements repo> ~/energy-measurements
cd ~/energy-measurements && make build
ENERGY_UID=<vc bricklet UID> sudo ./energy-logger
```

It prints `ENERGY_FILE=...` and starts writing. Confirm the CSV is actually
growing — a wrong UID produces a file containing only a header, which would
score an entire run as zero energy.

## 7. Run the experiment

From `k6_client_BA` on your Mac:

```bash
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt   # once

export ORCH_USER=<user> ORCH_HOST=<leader-ip> ORCH_SSH_PORT=22
export REMOTE_ENERGY_HOST=<energy host> REMOTE_ENERGY_USER=<user>

# baseline: leader started with POWER_AWARE=false
EXPERIMENT_NAME=low_load_baseline    scripts/run_low_load.sh

# treatment: leader restarted with POWER_AWARE=true
EXPERIMENT_NAME=low_load_poweraware  scripts/run_low_load.sh

python3 tools/compare_runs.py \
    --baseline  results/processed/<server>/low_load_baseline/<run> \
    --treatment results/processed/<server>/low_load_poweraware/<run> \
    --output    results/plots/comparison.png
```

Run each configuration several times — one run per arm gives you no error
bars, and wake timing in particular varies a lot.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| Node marked `dead` right after power-on | Worker not answering `/health`: running upstream tinyFaaS, tinyFaaS not enabled in systemd, or wrong `manager_address`. |
| `invalid relay channel 2` | Four workers numbered 0..3 on one relay. Channels are per-relay; use `relay_uid`. |
| Node wakes, then immediately sleeps again | Function deploy failed — almost always the uncached image build (step 4). |
| Requests 404 after a wake | Should no longer happen; a failed deploy now returns the node to sleep. If it does, the function was deleted between wakes. |
| No node-state timeline in results | Sampler could not reach `/nodes`. It is on **8081**, not 8080. |
| Energy CSV has only a header | Wrong `ENERGY_UID`, or the bricklet is not connected. |
| Workers stop booting after some days | SD card corruption from hard power cuts — see step 0. |
