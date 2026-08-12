# Cluster Setup

Bringing up the 5-machine cluster for the hardware-orchestration experiments:
one leader and four workers, whose power is switched by Tinkerforge relays.

Work through this in order. Each step ends with a check — do not move on
until it passes, because most failures here are silent later rather than loud
now.

One exception to the ordering: read step 0 first, but the final action in it
(0.3, enabling the read-only root) happens *after* a worker is fully
configured, since nothing written afterwards persists. The guide points back
to it at the right moment.

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
long. The leader does not need this — it is never power-cycled.

The alternative would be a graceful shutdown before each cut, but that adds
~15 s to every power-off, and that delay lands directly in the latency and
energy figures being measured. It would distort the result rather than just
cost time.

### Do not simply enable the overlay

`raspi-config`'s Overlay File System puts the *entire* root in a RAM overlay
and discards every write at boot. That includes `/var/lib/docker` — so the
pre-built `edge` image (step 4) would be lost on every power cut, and every
wake would re-download and reinstall ~24 MB of wheels before the node could
serve. That makes wakes far slower and defeats the caching the design relies
on.

Overlay the root, but give Docker persistent storage outside it.

### 0.1 Persistent storage for Docker

A USB SSD is the better choice — the repeated image and container churn is
hard on SD cards even without power cuts — but a second partition works.

```bash
lsblk                                    # find the device, e.g. /dev/sda1
sudo mkfs.ext4 -L dockerdata /dev/sda1

sudo systemctl stop docker
sudo mkdir -p /mnt/dockerdata
sudo mount /dev/sda1 /mnt/dockerdata
sudo rsync -aHAX /var/lib/docker/ /mnt/dockerdata/   # keep existing images
sudo umount /mnt/dockerdata

echo 'LABEL=dockerdata /var/lib/docker ext4 defaults,noatime,nofail 0 2' \
  | sudo tee -a /etc/fstab
sudo mount -a && sudo systemctl start docker
```

Check:

```bash
docker info | grep "Docker Root Dir"     # /var/lib/docker, on the new filesystem
```

`nofail` matters: without it, a worker whose SSD did not enumerate in time
refuses to finish booting, and a node that will not boot unattended breaks
the whole design.

### 0.2 Keep the logs

journald writes to `/var/log`, which is inside the overlay — so a worker's
logs disappear exactly when a power cut makes you want to read them. Put the
journal on the persistent disk:

```bash
sudo mkdir -p /mnt/dockerdata/journal
sudo sed -i 's|^#\?Storage=.*|Storage=persistent|' /etc/systemd/journald.conf
echo '/mnt/dockerdata/journal /var/log/journal none bind,nofail 0 0' \
  | sudo tee -a /etc/fstab
```

### 0.3 Enable the overlay last

Finish **all** remaining configuration first — Docker, tinyFaaS, the systemd
unit, and the pre-built function image (steps 1, 2 and 4). Once the overlay
is on, none of it persists.

```bash
sudo raspi-config      # Performance Options → Overlay File System → enable
                       # answer yes to write-protecting the boot partition too
sudo reboot
```

Check — this pair is the whole test, that the root discards writes while
Docker's cache survives:

```bash
findmnt / | head -2          # shows overlay, not /dev/mmcblk0p2
sudo touch /root/canary && sudo reboot
ls /root/canary              # must be gone
docker images                # the edge image must still be here
```

### 0.4 Changing anything afterwards

With the overlay active, config edits, `/etc/fstab` and `apt install` do not
survive a reboot. To make a change:

```bash
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...make the change...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

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

### Will the dependencies build on Alpine?

Yes — verified on `linux/arm64`. The runtime image is `python:3.11-alpine`
(musl libc), which cannot use the usual manylinux wheels, and the image has
no compiler, so a source build would fail outright. Both dependencies publish
musllinux aarch64 wheels, so pip installs prebuilt binaries:

```
numpy-2.4.6-cp311-cp311-musllinux_1_2_aarch64.whl    (17.3 MB)
pillow-12.3.0-cp311-cp311-musllinux_1_2_aarch64.whl   (6.3 MB)
```

The `edge` function itself was run end to end on that image: a 25 KB JPEG in,
a 7.9 KB 512×512 grayscale PNG out, 29 ms on an arm64 host. Expect a few
hundred milliseconds on a Pi.

So the first deploy is a ~24 MB download plus unpacking, not a compile — a
couple of minutes on a Pi, comfortably inside the 5-minute `DEPLOY_TIMEOUT`.
It still has to happen **once while the node is powered on**, because on a
wake that same work has to finish before the node can serve.

If you ever change `myfunc/edge/requirements.txt`, re-check this: a dependency
without a musllinux wheel would have to compile, and would fail.

### Now enable the read-only root

This is the point where the worker's configuration is complete — Docker,
tinyFaaS, the systemd unit and the cached function image are all in place.
Go back and do **step 0.3** on this worker before it starts getting
power-cycled in anger.

## 4b. Do one worker before wiring four

Bring up the leader plus **one** worker on one relay channel, and get a full
wake/sleep cycle working (step 5) before connecting the other three.

Debugging five machines at once is miserable, and everything that goes wrong
here — readiness, relay wiring, image caching — goes wrong identically on all
of them. Prove it once, then replicate. Adding workers afterwards is an edit
to `nodes.json` and a restart.

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
| `edge` image gone after a power cut, wakes suddenly slow | `/var/lib/docker` is inside the overlay. It needs persistent storage (step 0.1). |
| A worker hangs at boot | Persistent disk missing from `/etc/fstab` without `nofail`. |
| Config change vanished after reboot | The overlay is active; see step 0.4. |
