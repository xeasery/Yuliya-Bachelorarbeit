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
              │edge-1 │  │edge-2 │       │edge-3 │ │edge-4 │
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

### The decision: hard power cuts and SD cards

The controller powers a node off by opening the relay. There is no
`shutdown -h now` — it is a hard cut, every time. Across an Azure trace run
that is on the order of a hundred unclean power cuts per node, and SD card
corruption is the expected outcome, usually partway through an experiment.

The fix is a **read-only root filesystem on the workers**, with persistent
storage for Docker. It is set up across Parts 2 and 8: understand it now,
because it affects what hardware you want, but do not enable it until the
worker is otherwise finished.

The alternative — a graceful shutdown before each cut — adds ~15 s to every
power-off, and that delay lands directly in the latency and energy figures
being measured. It would distort the result rather than just cost time.

---

## Part 1 — Bring up each machine

Do this on all five. It is identical for leader and workers.

### 1.1 Image the card

Use **Raspberry Pi Imager** and choose Raspberry Pi OS **Lite** (64-bit) — no
desktop, which would waste RAM and power on a machine whose power draw you
are measuring.

Before writing, open the gear icon and set:

- **Hostname**: `leader`, `edge-1` … `edge-4`. You will be SSHing between five
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
ssh pi@edge-1.local
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

| Role | Hostname | IP | MAC | Relay | Ch | VC Bricklet |
| --- | --- | --- | --- | --- | --- | --- |
| leader | leader | 192.168.0.199 | `2c:cf:67:e2:b3:95` | — | — | `26gZ` |
| worker | edge-1 | 192.168.0.204 | `d8:3a:dd:25:51:46` | `2brr` | 0 | `26vg` |
| worker | edge-2 | 192.168.0.207 | `d8:3a:dd:25:51:79` | `2brr` | 1 | `26mi` |
| worker | edge-3 | 192.168.0.133 | `d8:3a:dd:25:45:73` | `2bro` | 0 | `26iw` |
| worker | edge-4 | 192.168.0.115 | `2c:cf:67:0e:94:cc` | `2bro` | 1 | `26vf` |

Bricklets are mapped as `edge-N` = your physical `piN`. **The IP column is the
part still to confirm**: the addresses were assigned to `edge-1`…`edge-4` in
an arbitrary order, so if your `pi1` is not 192.168.0.204, the rows need
reordering. Everything else in this table — relay, channel, bricklet — follows
the `edge-N` name, so one wrong IP row silently attributes a node's energy and
power-switching to a different machine.

Confirm it before measuring anything: power on one worker at a time and see
which address answers.

```bash
ping -c1 192.168.0.204     # is this the machine you call pi1?
```

Then set the hostnames to match and label the physical machines.

**Check the models are identical:**

```bash
cat /proc/device-tree/model
```

Two MAC prefixes appear above (`d8:3a:dd` and `2c:cf:67`), which can mean two
different Pi generations. A mixed cluster is not fatal, but it is something to
know before you measure: models differ in idle power and in how fast they run
the function, while the scheduler treats nodes as interchangeable. If they do
differ, say so in the thesis and keep an eye on the per-node power figures,
which will not be comparable across models.

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
date -u +%s ; ssh pi@edge-1 date -u +%s
```

At most 1 apart. Fix this before measuring anything.

### 1.5 Base system and Docker

```bash
sudo apt update && sudo apt full-upgrade -y

# swap on SD is slow, wears the card, and adds nothing on a 4-8GB Pi
sudo systemctl disable --now dphys-swapfile
sudo systemctl set-default multi-user.target

# Docker's own installer — the Debian package is often well behind
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
```

**Check:** `docker run --rm hello-world`

### 1.6 The tinyfaas account

```bash
sudo useradd -r -m -d /opt/tinyfaas -s /usr/sbin/nologin tinyfaas
sudo usermod -aG docker tinyfaas
sudo mkdir -p /opt/tinyfaas
sudo chown -R tinyfaas:tinyfaas /opt/tinyfaas
```

### 1.7 SSH keys from your Mac

The benchmark scripts open SSH connections non-interactively. A password
prompt mid-run either hangs the run or fails it.

```bash
ssh-keygen -t ed25519 -C "bachelorarbeit"     # on your Mac, once
ssh-copy-id pi@192.168.0.101                  # per machine
```

**Check:** `ssh pi@192.168.0.101 true` returns immediately, no prompt.

---

## Part 2 — Persistent storage for Docker (workers only)

The read-only root comes later, but its storage has to be in place first.

`raspi-config`'s overlay puts the *entire* root in RAM and discards every
write at boot — including `/var/lib/docker`. The pre-built function image
would then be lost on every power cut, and every wake would re-download ~24 MB
of wheels before the node could serve. That makes wakes far slower and defeats
the caching the design depends on.

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

`nofail` matters: without it, a worker whose SSD did not enumerate in time
refuses to finish booting, and a node that will not boot unattended breaks the
whole design.

But `nofail` alone creates a subtler problem. If the disk is slow to appear,
boot carries on *without* it, Docker starts against the empty overlay
directory, and the cached image is simply not there — so the node re-downloads
it on every wake and nobody notices, because everything still works, only
slower. Make Docker wait for the mount:

```bash
sudo systemctl edit docker.service
```
```ini
[Unit]
RequiresMountsFor=/var/lib/docker
```

Now a missing disk stops Docker, which stops tinyFaaS, which means the node
never answers `/health` and the leader reports it dead. That is a loud failure
you will notice rather than a quiet one that inflates every wake in your
results. The machine still boots and is reachable over SSH.

Keep the journal off the overlay too, so a worker's logs survive exactly the
power cut that makes you want to read them:

```bash
sudo mkdir -p /mnt/dockerdata/journal
sudo sed -i 's|^#\?Storage=.*|Storage=persistent|' /etc/systemd/journald.conf
echo '/mnt/dockerdata/journal /var/log/journal none bind,nofail 0 0' \
  | sudo tee -a /etc/fstab
```

**Check:** `docker info | grep "Docker Root Dir"` shows `/var/lib/docker` on
the new filesystem.

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
leader=26gZ  edge-1=26vg  edge-2=26mi  edge-3=26iw  edge-4=26vf
```

It still depends on `edge-N` naming the machine you call `piN`, which is the
open question in 1.3. A swapped pair attributes each node's energy to the
other, and nothing downstream would look wrong — it is the one error here that
is undetectable after the fact, so confirm the IP-to-machine mapping first.

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

for ip in <leader> <edge-1> <edge-2> <edge-3> <edge-4>; do
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
ENERGY_NODES="leader=26gZ,edge-1=26vg,edge-2=26mi,edge-3=26iw,edge-4=26vf" \
ENERGY_ADDR=<thinkpad-ip>:4223 \
  make verify-wiring
```

```
node       address  bricklet   verdict
──────────────────────────────────────────────────────────
edge-1     ✓        ✓          ok
edge-2     ✓        ✗          bricklet mismatch: this machine is
                               measured by the one mapped to "edge-4"
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
| edge-1 | relay A | 0 |
| edge-2 | relay A | 1 |
| edge-3 | relay B | 0 |
| edge-4 | relay B | 1 |

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
```
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

## Part 8 — Prove a wake cycle, then lock the worker down

### 8.1 The smoke test

```bash
# on the leader
journalctl -u tinyfaas -f

# from your Mac
base64 -w0 k6_client_BA/assets/input.jpg \
  | curl -s -D- --data-binary @- http://<leader>:8000/edge | head -20
```

Expect in the log: `activating node edge-1`, a relay power-on, readiness
polling, function deploy, `node edge-1 is active`. Expect in the response
headers: `X-tinyFaaS-Node: edge-1`.

Then leave it idle for longer than `NODE_IDLE_TIMEOUT` (60 s default) and
confirm `controller: scaling down node edge-1` appears and `/nodes` shows it
sleeping again.

**This is the checkpoint.** Everything before it is setup; everything after
assumes it works.

### 8.2 Enable the read-only root (workers)

The worker's configuration is now complete, so the overlay can go on. Nothing
written after this persists.

```bash
sudo raspi-config      # Performance Options → Overlay File System → enable
                       # answer yes to write-protecting the boot partition too
sudo reboot
```

**Check** — this pair is the whole test, that the root discards writes while
Docker's cache survives:

```bash
findmnt / | head -2          # overlay, not /dev/mmcblk0p2
sudo touch /root/canary && sudo reboot
ls /root/canary              # must be gone
docker images                # the edge image must still be here
```

To change anything afterwards:

```bash
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...make the change...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

---

## Part 9 — Replicate to the remaining workers

Repeat Parts 1, 2, 4, 5, 7 and 8.2 on `edge-2`, `edge-3` and `edge-4`, then
add them to `nodes.json` and restart tinyFaaS on the leader.

**Check:** `/nodes` lists all five, and a request wakes each in turn.

---

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
ENERGY_NODES="leader=26gZ,edge-1=26vg,edge-2=26mi,edge-3=26iw,edge-4=26vf" \
  sudo ./energy-logger
```

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

From `k6_client_BA` on your Mac:

```bash
python3 -m venv .venv && .venv/bin/pip install -r requirements.txt   # once

export ORCH_USER=<user> ORCH_HOST=<leader-ip> ORCH_SSH_PORT=22
export REMOTE_ENERGY_HOST=<energy host> REMOTE_ENERGY_USER=<user>
export ENERGY_NODES="leader=26gZ,edge-1=26vg,edge-2=26mi,edge-3=26iw,edge-4=26vf"

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
| Persistent Docker disk | ✗ | ✓ | ✗ |
| Read-only root | ✗ | ✓ | ✗ |
| Passwordless sudo | ✗ | ✗ | ✓ |
| Power via relay | **never** | ✓ | ✗ |

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `ssh: Could not resolve hostname edge-1.local` | mDNS blocked; use the IP. |
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
| Energy figures look implausible | Clock skew between the Mac and the energy host — see 1.4. |
| Energy logger never starts during a run | sudo is prompting for a password over SSH — see Part 10. |
| Worker never comes back after a power cut | `systemctl is-enabled tinyfaas` — installed but not enabled. |
| `edge` image gone after a power cut, wakes slow | `/var/lib/docker` is inside the overlay; needs persistent storage (Part 2). |
| A worker hangs at boot | Persistent disk missing from `/etc/fstab` without `nofail`. |
| Config change vanished after reboot | The overlay is active; see 8.2. |
| Workers stop booting after some days | SD card corruption from hard power cuts — see Part 0. |
