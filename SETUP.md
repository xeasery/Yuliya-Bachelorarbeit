# Cluster Setup

Everything from freshly imaged SD cards to a running experiment: one leader,
four workers, Tinkerforge relays switching worker power, and per-node energy
measurement.

Follow it top to bottom. Each step ends with a **Check** — do not move on
until it passes, because most failures here are silent later rather than loud
now.

**Do one worker first.** Parts 1–8 bring up the leader and a single worker and
prove a complete wake/sleep cycle. Part 9 then replicates to the other three.
Every failure mode at this stage is identical across machines, so find them
once rather than four times over.

---

## What you are building

```
                    ┌──────────────────────────────────────┐
   your Mac ─ ssh ─▶│ LEADER                               │
   (k6 client)      │  tinyFaaS mgmt :8080                 │
                    │  rproxy        :8000 fn  :8081 /nodes│
                    │  brickd :4223  ── relay A, relay B   │
                    └───┬──────────────────────────┬───────┘
                        │ relay A          relay B │
                   ┌────┴────┐                ┌────┴────┐
                 ch0│      ch1│             ch0│     ch1│
              ┌─────▼─┐  ┌────▼──┐       ┌─────▼─┐ ┌────▼──┐
              │ pi1   │  │ pi2   │       │ pi3   │ │ pi4   │
              └───┬───┘  └───┬───┘       └───┬───┘ └───┬───┘
                  │ one Voltage/Current Bricklet per node
                  └──────────┴───────────────┴─────────┘
                                   │
                       ┌───────────▼────────────┐
                       │ energy host (ThinkPad) │
                       │  brickd + energy-logger│
                       └────────────────────────┘
```

The leader has its own Voltage/Current Bricklet too — five in total, so the
cluster figure is the sum across all five rather than one shared reading.

Ports (tinyFaaS defaults):

| Port | Server | Used for |
| --- | --- | --- |
| 8000 | rproxy | function invocations |
| 8080 | management service | `/health`, `/upload`, `/delete` |
| 8081 | rproxy config server | `/nodes`, `/lastused` |
| 4223 | brickd | Tinkerforge |

**8080 and 8081 are different servers.** The leader health-checks workers on
8080; the benchmark client reads cluster power state from 8081.

### Roles

One binary runs on all five machines. Only the environment differs:

| Role | Environment | Behaviour |
| --- | --- | --- |
| Leader | `NODES_CONFIG`, `TINKERFORGE_UID`, `POWER_AWARE` | Schedules, powers nodes |
| Worker | *(none)* | Plain single-node tinyFaaS |

Every machine runs the build **from this repository**, not upstream tinyFaaS.
The leader decides a worker is ready by polling `/health`, and that endpoint
does not exist upstream — a worker running stock tinyFaaS powers on, never
answers, fails readiness and is marked dead, so every wake fails.

---

## Part 0 — Hardware, and one decision to make now

**Per Pi:** the Pi, a real power supply (a Pi 4 needs a genuine 5V/3A USB-C
supply — an underpowered one causes random reboots that will look like
software bugs), an SD card, and an Ethernet cable.

**Also:** 2× Industrial Dual Relay Bricklet, 5× Voltage/Current Bricklet 2.0,
Master Brick(s), and ideally a USB SSD per worker.

**Use Ethernet, not WiFi.** You are measuring energy and latency. WiFi adds
power draw that varies with signal quality and latency that varies with
interference — both land in your results as noise you cannot explain.

### Hard power cuts and SD cards

The controller powers a node off by opening the relay. There is no
`shutdown -h now` — it is a hard cut, every time. Across an Azure trace run
that is on the order of a hundred unclean power cuts per node, and SD cards
do eventually corrupt under that.

**This cluster accepts that risk rather than engineering it away.** The usual
fix is a read-only root filesystem, but that requires Docker's data directory
to live on separate persistent storage — otherwise the cached function image
is discarded on every power cut and each wake re-downloads ~24 MB, which is
worse than the problem. Without a USB disk per worker, the read-only root is
not an option.

That is tolerable here because **workers hold no unique state**. There is no
config on them: `nodes.json` lives on the leader, and the function is pushed
to them on every wake. A corrupted worker is a re-flash, not a rebuild — and
Part 8.2 makes an image so that re-flash is ten minutes.

The alternative — a graceful shutdown before each cut — adds ~15 s to every
power-off, and that delay lands directly in the latency and energy figures
being measured. It would distort the result rather than just cost time.

If USB disks do turn up later, the read-only root is worth revisiting; the
arrangement it needs is a persistent mount at `/var/lib/docker` (with
`nofail`, plus `RequiresMountsFor=/var/lib/docker` on `docker.service` so a
missing disk fails loudly instead of silently emptying the image cache).

---

## Part 1 — Bring up each machine

Do this on all five. It is identical for leader and workers.

### 1.1 Image the card

Use **Raspberry Pi Imager** and choose Raspberry Pi OS **Lite** (64-bit) — no
desktop, which would waste RAM and power on a machine whose power draw you
are measuring.

Before writing, open the gear icon and set:

- **Hostname**: `controller`, `pi1` … `pi4`. You will be SSHing between five
  identical machines for weeks, and `pi@192.168.0.207` tells you nothing.
- **Enable SSH**, with public-key authentication
- **Username and password**
- **Locale and timezone**

> Modern Raspberry Pi OS has **no default user**. If you skip the username
> setting there is no account to log in with.

Alternatively, boot with a monitor and run `sudo raspi-config` → Interface
Options → SSH.

### 1.2 Connect

```bash
ssh pi@pi1.local
```

If `.local` does not resolve (mDNS is often blocked on university networks),
find the address from your router's DHCP list, or:

```bash
nmap -sn 192.168.0.0/24
arp -a | grep -iE "b8:27:eb|dc:a6:32|d8:3a:dd|e4:5f:01|2c:cf:67"
```

**Check:** you have a shell prompt.

### 1.3 Fixed addresses

The leader finds workers by IP from `nodes.json`. If an address changes, the
leader cannot reach that worker, marks it dead, and you will spend an evening
debugging tinyFaaS when the problem is DHCP.

**Best: a DHCP reservation per Pi on the router**, tying its MAC to a fixed
address — then nothing on the Pi needs changing.

```bash
ip link show eth0 | awk '/link\/ether/ {print $2}'
```

If you cannot administer the router, set it on the Pi. Which command depends
on the OS version (`cat /etc/os-release | grep VERSION_CODENAME`):

**Bookworm or newer** (NetworkManager):

```bash
sudo nmcli con mod "Wired connection 1" \
  ipv4.addresses 192.168.0.101/24 \
  ipv4.gateway 192.168.0.1 \
  ipv4.dns "192.168.0.1 1.1.1.1" \
  ipv4.method manual
sudo nmcli con up "Wired connection 1"
```

**Bullseye or older** (dhcpcd) — append to `/etc/dhcpcd.conf`:

```
interface eth0
static ip_address=192.168.0.101/24
static routers=192.168.0.1
static domain_name_servers=192.168.0.1 1.1.1.1
```

**Check:** `ip -4 addr show eth0` shows the intended address, and still does
after a reboot.

Write your table down now — you need it for `nodes.json`:

| Role | Host | IP | Relay | Ch | VC Bricklet |
| --- | --- | --- | --- | --- | --- |
| leader | controller | 192.168.0.199 | — | — | `26gZ` |
| worker | pi1 | 192.168.0.133 | `2brr` | 0 | `26mi` |
| worker | pi2 | 192.168.0.204 | `2bro` | 0 | `26vf` |
| ~~worker~~ | ~~pi3~~ | ~~192.168.0.115~~ | ~~`2bro` 1~~ | | ~~`26iw`~~ — **excluded**, see below |
| worker | pi4 | 192.168.0.207 | `2bro` | 1 | `26iw` |

**pi3 is excluded from the cluster.** It appeared faulty -- it would not power
down when instructed and later would not wake -- but the cause was the relay
mapping above being wrong for every worker, so the system was switching one
machine while health-checking another. The node itself is fine and sits on
`2brr` channel 1; it can be brought back by restoring its entry. Its relay
channel is left open so it stays off, and it is neither scheduled nor
measured. Three workers demonstrate the mechanism as well as four; the
node count is a parameter of the setup, not of the design.

Node ids in `nodes.json` and `ENERGY_NODES` are the same `pi1`..`pi4` used by
the SSH aliases and the physical labels, deliberately: an `edge-N` layer on
top was what produced a first draft in which every IP was attached to the
wrong machine.

Addresses confirmed with `for h in controller pi1 pi2 pi3 pi4; do ssh $h
hostname -I; done`. Relay and channel assignment is still assumed — confirm
it with `make verify-wiring` before measuring.

Every column here was established by observation, not inference: addresses
from `hostname -I`, relay channels from watching which node a closed channel
powers, and bricklets from watching which one draws current when a known node
wakes. An earlier version of this table was wrong in all three, and each error
produced plausible behaviour rather than an obvious failure.

Confirm any change to this table against the machines themselves rather than
against the DHCP list — the hostname is the only thing that ties a row to a
physical Pi:

```bash
for h in controller pi1 pi2 pi3 pi4; do printf "%-12s " $h; ssh $h hostname -I; done
```

### 1.4 Clocks — do not skip this

**This one silently corrupts your results.**

Each invocation's measurement window is timestamped by the **proxy on your
Mac**; the power samples are timestamped by the **energy host**. The pipeline
integrates power between those two timestamps. If those clocks disagree by
even a few seconds, every invocation is integrated over the wrong slice of the
power trace. Nothing errors — the numbers just quietly describe something that
did not happen.

```bash
timedatectl                                   # want: System clock synchronized: yes
sudo timedatectl set-ntp true
sudo timedatectl set-timezone Europe/Berlin
```

**Check** — from your Mac, against every machine including the energy host:

```bash
date -u +%s ; ssh pi@pi1 date -u +%s
```

At most 1 apart. Fix this before measuring anything.

### 1.5 Base system and Docker

```bash
sudo apt update && sudo apt full-upgrade -y

# swap on SD is slow, wears the card, and adds nothing on a 4-8GB Pi
# sudo systemctl disable --now dphys-swapfile
sudo systemctl set-default multi-user.target

# Docker's own installer — the Debian package is often well behind
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker

sudo usermod -aG docker pi
sudo systemctl restart docker

grep docker /etc/group

newgrp docker

```

**Check:** `docker run --rm hello-world`

### 1.6 The tinyfaas account

```bash
sudo useradd -r -m -d /opt/tinyfaas -s /usr/sbin/nologin tinyfaas
sudo usermod -aG docker tinyfaas
sudo mkdir -p /opt/tinyfaas
sudo chown -R tinyfaas:tinyfaas /opt/tinyfaas
```

1. Copy the binary to the ThinkPad once (from your Mac):

scp -P 60001 tinyfaas-linux-arm64 scalable@141.23.28.219:/tmp/

2. Fan it out from there:

ssh -p 60001 scalable@141.23.28.219

for h in controller pi1 pi2 pi3 pi4; do
  echo "→ $h"
  scp /tmp/tinyfaas-linux-arm64 $h:/tmp/
done

3. Install on each (st

for h in controller pi
  echo "→ $h"
  ssh $h 'sudo useradd /usr/sbin/nologintinyfaas 2>/dev/null;
          sudo usermod
          sudo mkdir -p /opt/tinyfaas;
          sudo installm 0755/tmp/tinyfaas-linux-arm64 /opt/tinyfaas/tinyfaas-mgmt;
          echo "  ok"'
done

### 1.7 SSH keys from your Mac

The benchmark scripts open SSH connections non-interactively. A password
prompt mid-run either hangs the run or fails it.

```bash
ssh-keygen -t ed25519 -C "bachelorarbeit"     # on your Mac, once
ssh-copy-id pi@192.168.0.101                  # per machine
```

**Check:** `ssh pi@192.168.0.101 true` returns immediately, no prompt.

---

## Part 3 — Tinkerforge

`brickd` only sees hardware plugged into its own machine's USB, so there are
two independent instances here:

| Host | brickd serves | Used by |
| --- | --- | --- |
| **leader** | 2× Industrial Dual Relay Bricklet | tinyFaaS, to switch worker power |
| **ThinkPad** | 5× Voltage/Current Bricklet 2.0 | the energy logger |

The leader reaches its relays on `localhost`, which is the default — no
`TINKERFORGE_HOST` needed.

Note the leader is *measured* by a bricklet attached to the ThinkPad while
*controlling* relays attached to itself. That is fine: the current sensor sits
inline with the leader's supply, and only its data cable runs to the
ThinkPad's Master Brick.

### 3.1 Install brickd on both hosts

```bash
sudo apt install -y brickd
sudo systemctl enable --now brickd
```

**Check:** `sudo ss -lntp | grep 4223`

### 3.2 Read the UIDs

Open Brick Viewer against each host in turn — `localhost:4223` on the machine
itself, or `<ip>:4223` remotely. The leader should list the two relays; the
ThinkPad should list the five Voltage/Current Bricklets.

You need the two relay UIDs for `nodes.json`, and the five bricklet UIDs for
the energy logger.

The mapping is already recorded in the table in 1.3:

```
leader=26gZ  pi1=26mi  pi2=26vf  pi4=26iw
```

A swapped pair attributes each node's energy to the other, and nothing
downstream would look wrong — it is the one error here that is undetectable
after the fact. `make verify-wiring` checks it.

### 3.3 ⚠ Wiring the relays

The relays switch each worker's power supply on and off. **How you wire this
is an electrical safety question, not a software one.**

Switch the **low-voltage DC side** (between the PSU and the Pi) if at all
possible, rather than mains. If mains switching is unavoidable, have it
checked by your lab supervisor or the institute's technician before
energising it — TU Berlin will have rules about this, and mains wiring is not
something to improvise from a README.

Never wire the leader's own supply through a relay. It is the machine issuing
the power commands. The code refuses to power-cycle the node marked
`"local": true`, but do not rely on software to prevent a wiring mistake.

---

## Part 4 — Build and distribute the binary

On your Mac:

```bash
cd tinyFaaS-ext
uname -m                      # on a Pi: aarch64 → arm64, armv7l → arm
make tinyfaas-linux-arm64     # ~162 MB, includes rproxy and all runtimes

for ip in 192.168.0.199 192.168.0.204 192.168.0.207 192.168.0.133 192.168.0.115; do
  echo "→ $ip"
  scp tinyfaas-linux-arm64 pi@$ip:/tmp/
done
```

On each Pi:

```bash
sudo install -o tinyfaas -g tinyfaas -m 0755 \
  /tmp/tinyfaas-linux-arm64 /opt/tinyfaas/tinyfaas-mgmt
```

**Check:** `/opt/tinyfaas/tinyfaas-mgmt --help` runs. Any output proves it
executes; a wrong architecture gives `cannot execute binary file`.

---
1. Write the unit file once on the ThinkPad:

cat > ~/tinyfaas.service <<'UNIT'
[Unit]
Description=tinyFaaS management service
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
StartLimitIntervalSec=0

[Service]
Type=simple
User=tinyfaas
WorkingDirectory=/opt/tinyfaas
ExecStart=/opt/tinyfaas/tinyfaas-mgmt
Restart=always
RestartSec=5
TimeoutStartSec=120
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT

2. Copy it to all five:

for h in controller pi1 pi2 pi3 pi4; do scp ~/tinyfaas.service $h:/tmp/; done

3. Install and start, one command per host:

for h in controller pi1 pi2 pi3 pi4; do
  echo "═══ $h"
  ssh $h "sudo cp /tmp/tinyfaas.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable --now tinyfaas && sleep 5 && systemctl is-active tinyfaas"
done

Everything in step 3 is one double-quoted string with no nesting, so it pastes cleanly.

If Docker isn't yet installed everywhere, run that separately first:

for h in controller pi1 pi2 pi3 pi4; do
  ssh $h "command -v docker >/dev/null && echo 'docker present' || echo 'DOCKER MISSING'"
done

✻ Sautéed for 23s

──────────────────────────────────────────────────────────────────────────
❯ docker present on all five, running step 3 now
──────────────────────────────────────────────────────────────────────────

1. Are they actually serving? (active only means the process started)

for h in controller pi1 pi2 pi3 pi4; do
  printf "%-12s " $h
  ssh $h "curl -fsS --max-time 5 http://localhost:8080/health >/dev/null" && echo OK || echo FAIL
done

2. Can the leader reach the workers? This is the one that matters — it's exactly what the wake sequence does:

ssh controller 'for ip in 192.168.0.204 192.168.0.207 192.168.0.133 192.168.0.115; do printf "%-16s " $ip; curl -fsS --max-time 5 http://$ip:8080/health >/dev/null && echo OK || echo FAIL; done'

Four OK there means the leader can health-check and deploy to every worker. A FAIL is a firewall or a wrong IP, and it's much easier to find now than as a "node marked dead" during a run.

3. Then brickd on the controller (SETUP 3.1) — this is what lets tinyFaaS switch power at all:

ssh controller "sudo apt install -y brickd && sudo systemctl enable --now brickd && sudo ss -lntp | grep 4223"

Then confirm the two relays are actually visible to it. The Master Brick with 2brr/2bro must be plugged into the controller's USB:

ssh controller "lsusb | grep -i tinkerforge"

If nothing shows, the relays are still on the ThinkPad and need moving — or I point tinyFaaS at the ThinkPad's brickd instead. That's the last unknown before you can test a real wake.
  ⏵⏵
## Part 5 — Start tinyFaaS on boot

On every machine:

```bash
sudo cp tinyFaaS-ext/deploy/tinyfaas.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tinyfaas
```

`enable` is what makes this survive a power cut. The unit is written for
unattended operation — it restarts on *any* exit, not just failures, because
the management service exits cleanly when signalled; and it never stops
retrying, because systemd would otherwise give up after a few rapid attempts
and leave the node dead until someone logged in.

**Check — with the power, not with `reboot`:**

```bash
systemctl is-enabled tinyfaas     # enabled
curl -f http://<worker-ip>:8080/health && echo OK
```

Now physically cut and restore power to that worker, wait, and run the same
curl again without touching the machine.

```bash
systemctl is-active tinyfaas      # active
docker images                     # cached images still here
```

If it does not come back on its own, nothing else in this setup will work —
the entire design assumes a node recovers unattended from a power cut.

---

## Part 6 — Configure the leader

`deploy/nodes.json` already carries the cluster's real addresses and relay
UIDs (A = `2brr`, B = `2bro`), so it can be copied as-is:

```bash
sudo cp tinyFaaS-ext/deploy/nodes.json /opt/tinyfaas/nodes.json
```

### Verify the wiring before the first experiment

Three mappings have to agree, and none can be checked once a run is under way:
relay+channel → machine, IP → machine, and bricklet → machine. A relay mistake
eventually shows up as a node marked dead, which points at the wrong thing. A
bricklet mistake never shows up at all — each node's energy is attributed to
another and every number still looks plausible.

`verify-wiring` powers each worker on by itself and checks all three at once:

```bash
# on the leader
cd tinyFaaS-ext
NODES_CONFIG=deploy/nodes.json \
ENERGY_NODES="leader=26gZ,pi1=26mi,pi2=26vf,pi4=26iw" \
ENERGY_ADDR=<thinkpad-ip>:4223 \
  make verify-wiring
```

```
node       address  bricklet   verdict
──────────────────────────────────────────────────────────
pi1        ✓        ✓          ok
pi2        ✓        ✗          bricklet mismatch: this machine is
                               measured by the one mapped to "pi4"
```

It leaves every worker powered off.

Reading the energy bricklets means reaching the ThinkPad's brickd, which
listens on loopback only — normal, and fine for everything else, because the
logger runs on that machine. Rather than opening it up (it is a shared
machine), tunnel it for the duration of the check. On the leader:

```bash
ssh -N -L 4224:localhost:4223 -p 60001 scalable@141.23.28.219 &
```

then use `ENERGY_ADDR=localhost:4224`. Port 4224 because 4223 is already the
leader's own brickd, serving the relays.

Without the tunnel the tool still checks relays and addresses, and says that
it skipped the rest.

The leader's own bricklet (`26gZ`) cannot be checked this way — it is never
powered off. It is verifiable by elimination: it is the one series that never
rises or falls as workers come and go. `channel` is the channel **on
that relay** (0 or 1), not a cluster-wide index:

| Node | `relay_uid` | `channel` |
| --- | --- | --- |
| pi1 | `2brr` | 0 |
| pi2 | `2bro` | 0 |
| pi4 | `2bro` | 1 |
| ~~pi3~~ | ~~`2brr`~~ | ~~1~~ |

Numbering them 0,1,2,3 on one relay is rejected at startup, since a dual relay
has only two channels. Two nodes sharing a relay *and* channel is rejected
too — one relay silently switching two nodes would still serve traffic and
produce plausible numbers.

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
Environment=FUNCTION_IDLE_TIMEOUT=0
```

`FUNCTION_IDLE_TIMEOUT=0` matters, and it goes on **every** node, workers
included. tinyFaaS tears down any function unused for 30 s — its own
function-level scale-to-zero. In this cluster that fights node-level power
management: the leader still considers a node active and routes to it, while
the node has quietly deleted the function, so the request 404s. With the
leader's idle timeout at 60 s and this at 30 s, there is a guaranteed window
where that happens.

Node power management *is* the scale-to-zero here, so the function-level
reaper is turned off. Leaving it on would also confound the measurement:
some of the energy saved would come from container teardown rather than from
powering nodes down, and it would apply unevenly between the two arms.
```bash
sudo systemctl daemon-reload && sudo systemctl restart tinyfaas
```

**Check:**

```bash
curl -s localhost:8081/nodes | python3 -m json.tool
```

The leader `active`, workers `sleeping`. Connection refused means you are on
the wrong port — `/nodes` is 8081, not 8080.

---

## Part 7 — Deploy the function and warm the cache

**Do not skip the warming.** The `edge` function needs numpy and Pillow, and
the first image build on a Pi takes minutes. On a wake, a deploy that fails
sends the node back to sleep, so an uncached image gives you: wake → build
times out → sleep → next request wakes it again → forever.

With the worker powered on:

```bash
cd tinyFaaS-ext
./scripts/upload.sh ./myfunc/edge edge python3 1
```

**Check** each worker individually:

```bash
base64 -w0 ../k6_client_BA/assets/input.jpg \
  | curl -s --data-binary @- "http://<worker-ip>:8000/edge" | head -c 20
```

Base64 PNG data, not `ERROR:` and not a 404.

### Will the dependencies build on Alpine?

Yes — verified on `linux/arm64`. The runtime image is `python:3.11-alpine`
(musl libc), which cannot use the usual manylinux wheels, and the image has no
compiler, so a source build would fail outright. Both dependencies publish
musllinux aarch64 wheels:

```
numpy-2.4.6-cp311-cp311-musllinux_1_2_aarch64.whl    (17.3 MB)
pillow-12.3.0-cp311-cp311-musllinux_1_2_aarch64.whl   (6.3 MB)
```

The function was run end to end on that image: a 25 KB JPEG in, a 7.9 KB
512×512 grayscale PNG out, 29 ms on an arm64 host. Expect a few hundred
milliseconds on a Pi.

So the first deploy is a ~24 MB download plus unpacking, not a compile —
comfortably inside the 5-minute `DEPLOY_TIMEOUT`. If you ever change
`myfunc/edge/requirements.txt`, re-check this: a dependency without a
musllinux wheel would have to compile, and would fail.

---

## Part 8 — Prove a wake cycle, then image the card

### 8.1 The smoke test

```bash
# on the leader
journalctl -u tinyfaas -f

# from your Mac
base64 -w0 k6_client_BA/assets/input.jpg \
  | curl -s -D- --data-binary @- http://<leader>:8000/edge | head -20
```

Expect in the log: `activating node pi1`, a relay power-on, readiness
polling, function deploy, `node pi1 is active`. Expect in the response
headers: `X-tinyFaaS-Node: pi1`.

Then leave it idle for longer than `NODE_IDLE_TIMEOUT` (60 s default) and
confirm `controller: scaling down node pi1` appears and `/nodes` shows it
sleeping again.

**This is the checkpoint.** Everything before it is setup; everything after
assumes it works.

### 8.2 Make a golden image

This worker is now fully configured, and it holds nothing unique — no
`nodes.json`, no per-node settings. So an image of its card is both the
recovery plan for SD corruption and the fastest way to build the other three.

Shut it down cleanly, take the card out, and image it from your Mac:

```bash
diskutil list                              # find the card, e.g. /dev/disk4
diskutil unmountDisk /dev/disk4
sudo dd if=/dev/rdisk4 of=~/worker-golden.img bs=4m status=progress
```

Keep that image. A worker that stops booting is then a re-flash, not a
re-setup.

**Check:** put the card back, boot it, and confirm the worker still answers
`/health`.

## Part 9 — Replicate to the remaining workers

Flash the golden image onto the other three cards (Raspberry Pi Imager can
write a custom `.img`, or use `dd` in reverse), then fix the three things
that must not be shared between cloned machines:

```bash
# on each freshly flashed worker, via monitor or its DHCP-assigned address
sudo hostnamectl set-hostname pi2

# a cloned machine-id makes DHCP hand out the same lease to every clone
sudo rm -f /etc/machine-id /var/lib/dbus/machine-id
sudo systemd-machine-id-setup
sudo dbus-uuidgen --ensure

# cloned SSH host keys mean all four present the same identity
sudo rm -f /etc/ssh/ssh_host_*
sudo dpkg-reconfigure openssh-server

sudo reboot
```

Then set its DHCP reservation to the address in the table in 1.3, add it to
`nodes.json` on the leader, and restart tinyFaaS there.

The machine-id step matters more than it looks: with identical machine-ids,
DHCP can hand every clone the same address, and you get workers that
intermittently vanish for no visible reason.

**Check:** `/nodes` lists all five, and a request wakes each in turn.

## Part 10 — Energy logger

On the energy host, with all five Voltage/Current Bricklets attached:

```bash
git clone https://github.com/xeasery/energy-measurements ~/energy-measurements
cd ~/energy-measurements && make build
```

**Key-based SSH to this host is required, not optional.** The run scripts
open the connection non-interactively; a password prompt has nowhere to go.
The energy host currently accepts passwords only, so from your Mac:

```bash
ssh-copy-id -p 60001 scalable@141.23.28.219
ssh -o BatchMode=yes -p 60001 scalable@141.23.28.219 true && echo OK
```

The same applies to `sudo`. The run scripts invoke it over that
non-interactive connection, and with a normal sudo configuration the prompt
has nowhere to go — the run either stalls or silently continues with no
energy data:

```bash
sudo visudo -f /etc/sudoers.d/energy-logger
```
```
pi ALL=(root) NOPASSWD: /home/pi/energy-measurements/energy-logger, /usr/bin/pkill
```

Scoping it to those two commands is deliberate — blanket `NOPASSWD: ALL` is a
much larger door than this needs.

**If the energy host is a laptop, stop it suspending.** Lid closed or idle
timeout → the logger dies → every invocation after that point silently gets
zero energy. A trace run is hours.

```bash
sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
# and in /etc/systemd/logind.conf:  HandleLidSwitch=ignore
```

Keep it on AC: on battery, CPU power management changes, and this is the
machine timing your power samples.

Test it manually once:

```bash
sudo env ENERGY_NODES="leader=26gZ,pi1=26mi,pi2=26vf,pi4=26iw" \
  ./energy-logger
```

`sudo env`, not `VAR=... sudo` — sudo resets the environment, so variables set
before it never reach the logger and it refuses to start with no bricklets
configured.

**Check** every node actually reports — a bricklet that never reports
contributes nothing to the total, which looks exactly like a node that was
deliberately powered off:

```bash
cut -d, -f2 log/energy_*.csv | sort -u        # every node listed
tail -5 log/energy_*.csv
```

A node at single-digit milliwatts is powered off; an idle Pi draws around 3 W.

For runs longer than about an hour, raise `ENERGY_PERIOD_MS` — the period is
per bricklet, so five nodes at the 10 ms default is 500 rows/second.

**Check:** `ssh pi@<energy-host> sudo -n true` succeeds silently.

---

## Part 11 — Run the experiment

**Run the client on the energy host, not on your Mac.** Two reasons, and the
second is the one that matters:

- The cluster is on `192.168.0.0/24`. A Mac on another network cannot reach
  it, and an SSH tunnel that drops midway through a multi-hour run truncates
  the result rather than failing it.
- The proxy's invocation timestamps and the power samples then come from a
  single clock. Clock skew between two machines misattributes energy to the
  wrong invocations, and nothing in the output would look wrong.

So everything below happens on the energy host (`scalable@141.23.28.219`),
which already has the bricklets attached.

### 11.1 — Prerequisites there

```bash
git clone https://github.com/xeasery/k6_client_BA ~/k6_client_BA
cd ~/k6_client_BA
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt
```

`k6`, `go` and `python3` must all be on PATH — Go builds the measurement
proxy on every run.

The leader needs **passwordless sudo** for the client's arm switching, which
runs `systemctl` and edits the drop-in over a non-interactive SSH connection:

```bash
ssh pi@192.168.0.199 "sudo -n systemctl status tinyfaas >/dev/null && echo sudo-ok"
```

**Check:** that prints `sudo-ok`. Silence means sudo wants a password and
every run will stop at its first step.

Note that Part 10's key-based SSH and `NOPASSWD` rules for the *energy* host
apply only to the older arrangement where the client drives the logger
remotely. Here the logger runs on this same machine and is started by hand,
so neither is needed for it.

### 11.2 — Start the logger, once, by hand

In its own terminal, left running across every arm of the session:

```bash
cd ~/energy-measurements
sudo env ENERGY_NODES="leader=26gZ,pi1=26mi,pi2=26vf,pi4=26iw" \
  ENERGY_PERIOD_MS=20 ./energy-logger
```

20 ms rather than the 10 ms default: four bricklets over a multi-hour session
is a lot of rows, and 20 ms still resolves an invocation.

**Check:** the newest CSV is growing, not just a header.

```bash
sleep 5; wc -l ~/energy-measurements/log/energy_*.csv
```

**Use `tmux`.** The session is hours long, and an SSH disconnect otherwise
kills the whole thing — SIGHUP reaches the entire process group. Run both the
logger and the arms inside it.

### 11.3 — Run the arms

```bash
cd ~/k6_client_BA
export ORCH_DIRECT=1 ORCH_HOST=192.168.0.199 ORCH_USER=pi
export LOCAL_ENERGY_DIR=~/energy-measurements/log
export ENERGY_NODES="leader=26gZ,pi1=26mi,pi2=26vf,pi4=26iw"
export RATE=1 TIME_UNIT=2m DURATION=20m

scripts/run_arm.sh baseline      # POWER_AWARE=false
scripts/run_arm.sh poweraware    # POWER_AWARE=true
```

`run_arm.sh` sets the mode in the leader's drop-in, restarts it, confirms
from the service log which mode the process actually entered, waits for the
workers to reach the state that mode implies, deploys `edge`, and runs the
benchmark. Every step is checked rather than assumed, because each of those
failures yields a complete run with ordinary-looking numbers instead of an
error.

**Keep `TIME_UNIT` well above `NODE_IDLE_TIMEOUT`.** At the 60 s default, a
`TIME_UNIT` of `1m` leaves an idle stretch shorter than the timeout, nothing
ever powers down, and both arms measure the same thing.

**Interleave the arms** rather than running all of one then all of the other.
Anything that drifts over a few hours — room temperature, a background
process — otherwise lands entirely on whichever arm ran later and reads as an
effect. The comparison's error propagation assumes the arms are independent.

```bash
for i in 1 2 3; do
    scripts/run_arm.sh baseline
    scripts/run_arm.sh poweraware
done
scripts/run_arm.sh baseline
scripts/run_arm.sh poweraware
```

Four per arm at 20 minutes each is roughly three hours including the wait for
the cluster to settle between arms.

**Check, on every run:** `energy coverage: OK` before `[energy] collected`.
A logger that dies partway leaves a trace that is real and non-empty and
covers only part of the window; integrating it under-reports energy in
proportion to what it missed. The run refuses such a trace and reports no
energy rather than a fraction of it, keeping it as `incomplete_*.csv` with an
`energy_coverage.json` saying which node fell short. That run's node-state
data is unaffected and still counts.

### 11.4 — Compare

```bash
.venv/bin/python tools/compare_runs.py \
    --baseline  results/processed/tinyfaas-cluster/low_load_baseline \
    --treatment results/processed/tinyfaas-cluster/low_load_poweraware \
    --output    results/plots/comparison.png \
    --json      results/plots/comparison.json
```

Pointing each side at the experiment directory picks up every run under it.
With more than one run per arm each metric becomes a mean ± sample standard
deviation, and the change carries the standard error of the two means.

Read `total_energy_j_raw`, or better `mean_power_w`. **Do not report
`total_energy_j`** (the idle-subtracted figure) as a comparison between arms:
the baseline arm's idle window is measured with every node awake and the
power-aware arm's with the workers asleep, so the two subtract different
baselines and the difference between them means nothing. Mean power is also
tighter than total joules, since total energy scales with a run's window
length and those vary slightly.

### 11.5 — What the first session measured

For reference, so a later run that disagrees wildly is recognisable as a
fault rather than a finding. Four runs per arm, 2026-09-01, 1 request per
2 minutes for 20 minutes:

| | always-on | power-aware | change |
| --- | --- | --- | --- |
| Mean power | 9.53 ± 0.03 W | 5.02 ± 0.18 W | **−47.3% ± 1.0%** |
| Node-seconds active | 4924 ± 5 | 1887 ± 17 | **−61.7% ± 0.2%** |
| Latency p50 | 172 ± 1 ms | 49301 ± 961 ms | ▲ |
| Wakes per run | 0 | 8.0, median 53 s each | |

Per-node idle draw was leader 3.84 W, workers ~1.85 W each.

Two things worth understanding before quoting these:

**Node-time falls further than energy, and that is structural.** The leader
is 40% of the cluster's idle power and never powers down, yet counts as one
of four nodes. Node-seconds therefore overstates the achievable energy
saving; the energy figure is the honest headline.

**The energy figure is independently predictable from the node-time.** With
`mean_active_nodes` at 1.51, expected power is 3.84 + 0.51 × 1.85 = 4.78 W
against 5.02 W measured, the residual being boot transients. Two routes to
the same number is the corroboration worth reporting.

**This operating point is deliberately adversarial.** A 2-minute arrival gap
against a 60 s idle timeout means a node sleeps after every single request
and the next one pays a full boot. The energy saving is near its ceiling and
so is the latency cost. Wider gaps trade less of one for less of the other.

---

## Quick reference

| | Leader | Worker | Energy host |
| --- | --- | --- | --- |
| Fixed IP | ✓ | ✓ | ✓ |
| NTP synchronised | ✓ | ✓ | ✓ |
| Docker | ✓ | ✓ | ✗ |
| `tinyfaas` user + binary | ✓ | ✓ | ✗ |
| systemd unit enabled | ✓ | ✓ | ✗ |
| `nodes.json` + env | ✓ | ✗ | ✗ |
| brickd | ✓ (relays) | ✗ | ✓ (VC bricklets) |
| Passwordless sudo | ✓ (for `run_arm.sh`) | ✗ | ✓ (remote arrangement only) |
| Runs the benchmark client | ✗ | ✗ | ✓ |
| Power via relay | **never** | ✓ | ✗ |

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `ssh: Could not resolve hostname pi1.local` | mDNS blocked; use the IP. |
| `Permission denied (publickey,password)` | Key not copied, or the username differs from your Mac's. |
| `docker: permission denied` | User not in the `docker` group, or you did not log out and back in. |
| Random reboots under load | Underpowered PSU. A Pi 4 needs a genuine 5V/3A supply. |
| Pi unreachable after a reboot | DHCP gave it a different address — see 1.3. |
| `cannot execute binary file` | Wrong architecture; check `uname -m` and rebuild. |
| Node marked `dead` right after power-on | Worker not answering `/health`: running upstream tinyFaaS, unit not enabled, or wrong `manager_address`. |
| `open relay <uid>: ...` on the leader | Wrong relay UID, relay unplugged, or brickd not running on the leader. |
| Nodes never power down, no errors | Leader cannot reach its brickd: `systemctl status brickd` on the leader. |
| `invalid relay channel 2` | Four workers numbered 0..3 on one relay. Channels are per-relay; use `relay_uid`. |
| Node wakes, then immediately sleeps again | Function deploy failed — almost always the uncached image build (Part 7). |
| No node-state timeline in results | Sampler could not reach `/nodes`. It is on **8081**, not 8080. |
| Energy CSV has only a header | Wrong UID, or no bricklet connected. |
| One node missing from the energy CSV | Its bricklet detached or its UID is wrong. It then contributes nothing to the total, which mimics a powered-off node — check the logger's warnings. |
| A node's energy looks like another's | Bricklet-to-node mapping swapped; re-derive it one node at a time (3.2). |
| Energy figures look implausible | Clock skew between the machine timing invocations and the one sampling power — cannot occur when the client runs on the energy host (Part 11), which is why it does. |
| Energy logger never starts during a run | sudo is prompting for a password over SSH — see Part 10. Does not apply when the logger is started by hand (11.2). |
| `energy coverage: INCOMPLETE` at the end of a run | The logger died partway. That run has no energy but its node-state data still counts; restart the logger before the next arm. |
| An energy figure marked `*` in the comparison | That arm has a run with no usable trace, excluded from the energy mean. `energy_coverage.json` in the run's raw directory names the node that fell short. |
| One arm's energy mean low with a large spread | A partial trace was averaged in. Check each run's `mean_power_w`; the sound ones agree to within a few hundredths of a watt. |
| Run stops on `ERROR: leader is not in <arm> mode` | The drop-in was edited but the service did not restart into it, or `POWER_AWARE` is absent from `cluster.conf`. |
| Worker never comes back after a power cut | `systemctl is-enabled tinyfaas` — installed but not enabled. |
| Workers stop booting after some days | SD card corruption from hard power cuts — see Part 0. |
