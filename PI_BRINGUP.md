# Raspberry Pi Bring-Up

Getting five Raspberry Pis from "OS installed, nothing else" to the point
where [SETUP.md](./SETUP.md) can take over.

SETUP.md assumes you can SSH into each Pi, that Docker runs, and that the
machines have stable addresses. This guide is how you get there.

Work on **one** Pi from start to finish first. Once it works, the other four
are the same commands.

---

## Phase 0 — What you need

**Hardware, per Pi:** the Pi, a power supply (Pi 4 needs a real 5V/3A USB-C
supply — an underpowered one causes random reboots that will look like
software bugs), a micro-SD card, and an Ethernet cable.

**Also:** 2× Industrial Dual Relay Bricklet, 1× Voltage/Current Bricklet 2.0,
a Master Brick (or HAT) to connect them, and ideally a USB SSD per worker
(see SETUP.md step 0.1).

**Use Ethernet, not WiFi.** Your experiment measures energy and latency. WiFi
adds power draw that varies with signal quality and latency that varies with
interference — both land directly in your results as noise you cannot
explain. Wire everything to one switch.

A note on naming: give the Pis hostnames now — `leader`, `edge-1` … `edge-4`.
You will be SSHing between five identical machines for weeks, and
`pi@192.168.1.107` tells you nothing.

---

## Phase 1 — First connection

You have two options. **Headless is strongly recommended** — you will be
re-imaging at some point, and it is far faster.

### Option A: Headless (recommended)

Use **Raspberry Pi Imager** on your Mac. Choose Raspberry Pi OS **Lite**
(64-bit) — you need no desktop, and a desktop wastes RAM and power on a
machine whose power draw you are measuring.

Before writing, open the settings (gear icon) and set:

- **Hostname**: `edge-1` (etc.)
- **Enable SSH**: yes, *with public-key authentication*
- **Username and password**: e.g. `pi` + a password
- **Locale/timezone**: your own

Setting the SSH key here saves you doing it later, and the run scripts need
key auth anyway (Phase 5).

> Modern Raspberry Pi OS has **no default user**. If you skip the username
> setting there is no account to log in with.

Write the card, put it in the Pi, connect Ethernet, power on, wait ~60 s.

### Option B: Monitor and keyboard

Boot with a monitor and USB keyboard, complete the setup wizard, then:

```bash
sudo raspi-config      # Interface Options → SSH → enable
```

### Connect

```bash
ssh pi@edge-1.local
```

If `.local` does not resolve (mDNS is often blocked on university networks),
find the address instead:

```bash
# your router's DHCP client list is the easiest
# or scan the subnet — adjust to your network:
nmap -sn 192.168.1.0/24
# Pi MAC addresses start with b8:27:eb, dc:a6:32, d8:3a:dd or e4:5f:01
arp -a | grep -iE "b8:27:eb|dc:a6:32|d8:3a:dd|e4:5f:01"
```

Then `ssh pi@192.168.1.<x>`.

**Check:** you have a shell prompt on the Pi.

---

## Phase 2 — Fixed addresses

The leader finds workers by IP from `nodes.json`. If an address changes, the
leader cannot reach that worker, marks it dead, and you will spend an evening
debugging tinyFaaS when the problem is DHCP.

**Best: a DHCP reservation per Pi on the router**, tying its MAC to a fixed
address. Then nothing on the Pi needs changing. Get the MAC with:

```bash
ip link show eth0 | awk '/link\/ether/ {print $2}'
```

If you cannot administer the router, set a static address on the Pi. Which
command depends on the OS version:

```bash
cat /etc/os-release | grep VERSION_CODENAME
```

**Bookworm or newer** (NetworkManager):

```bash
sudo nmcli con mod "Wired connection 1" \
  ipv4.addresses 192.168.1.101/24 \
  ipv4.gateway 192.168.1.1 \
  ipv4.dns "192.168.1.1 1.1.1.1" \
  ipv4.method manual
sudo nmcli con up "Wired connection 1"
```

**Bullseye or older** (dhcpcd) — append to `/etc/dhcpcd.conf`:

```
interface eth0
static ip_address=192.168.1.101/24
static routers=192.168.1.1
static domain_name_servers=192.168.1.1 1.1.1.1
```

Then `sudo reboot`.

**Check:** `ip -4 addr show eth0` shows the intended address, and it is still
the same after a reboot.

Write your table down now — you need it for `nodes.json`:

| Role | Hostname | IP | Relay | Channel |
| --- | --- | --- | --- | --- |
| leader | leader | 192.168.1.100 | — | — |
| worker | edge-1 | 192.168.1.101 | A | 0 |
| worker | edge-2 | 192.168.1.102 | A | 1 |
| worker | edge-3 | 192.168.1.103 | B | 0 |
| worker | edge-4 | 192.168.1.104 | B | 1 |

---

## Phase 3 — Clocks (do not skip this)

**This one silently corrupts your results.**

Energy is attributed to invocations by timestamp. The measurement window for
each invocation comes from the **proxy on your Mac**; the power samples come
from the **machine with the Voltage/Current Bricklet**. `process_responses.py`
integrates power between those two timestamps.

If those two clocks disagree by even a few seconds, every invocation is
integrated over the wrong slice of the power trace. Nothing errors. The
numbers just quietly describe something that did not happen.

On every Pi:

```bash
timedatectl
```

You want `System clock synchronized: yes` and an active NTP service. If not:

```bash
sudo timedatectl set-ntp true
sudo systemctl restart systemd-timesyncd
```

Set the same timezone everywhere (the timestamps carry offsets, so mixed
zones are survivable — but identical zones make logs comparable by eye):

```bash
sudo timedatectl set-timezone Europe/Berlin
```

**Check** — compare each Pi against your Mac:

```bash
# on the Mac
date -u +%s ; ssh pi@edge-1 date -u +%s
```

The two numbers should differ by at most 1. If they differ by more, fix it
before you measure anything.

---

## Phase 4 — Base system

On **every** Pi, leader and workers alike:

```bash
sudo apt update && sudo apt full-upgrade -y
sudo apt install -y curl ca-certificates
```

Reduce SD wear and background noise — both help a machine that gets power-cut
repeatedly and whose power draw you are measuring:

```bash
# swap on SD is slow, wears the card, and adds nothing on a 4-8GB Pi
sudo systemctl disable --now dphys-swapfile

# no desktop is installed on Lite, but make sure nothing graphical starts
sudo systemctl set-default multi-user.target
```

### Docker

Use Docker's own installer — the Debian package is often well behind:

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
```

**Check:**

```bash
docker run --rm hello-world
```

If that fails with a permissions error, you are not in the `docker` group yet
— that comes next, and needs a fresh login to take effect.

---

## Phase 5 — Accounts and SSH keys

### The tinyfaas service account

tinyFaaS runs as its own user and needs the Docker socket:

```bash
sudo useradd -r -m -d /opt/tinyfaas -s /usr/sbin/nologin tinyfaas
sudo usermod -aG docker tinyfaas
sudo mkdir -p /opt/tinyfaas
sudo chown -R tinyfaas:tinyfaas /opt/tinyfaas
```

### Key-based SSH from your Mac

The benchmark scripts open SSH connections non-interactively. A password
prompt mid-run either hangs the run or fails it.

```bash
# on your Mac, once
ssh-keygen -t ed25519 -C "bachelorarbeit"

# then per Pi
ssh-copy-id pi@192.168.1.101
```

**Check:** `ssh pi@192.168.1.101 true` returns immediately with no prompt.

### Passwordless sudo for the energy logger

Only on the machine that will run the energy logger. The run scripts start
and stop it over SSH with `sudo`, non-interactively — with a normal sudo
configuration that prompt has nowhere to go, and the run either stalls or
silently continues with no energy data.

```bash
sudo visudo -f /etc/sudoers.d/energy-logger
```

```
pi ALL=(root) NOPASSWD: /home/pi/energy-measurements/energy-logger, /usr/bin/pkill
```

Adjust the path and username. Scoping it to those two commands is
deliberate — blanket `NOPASSWD: ALL` is a much larger door than this needs.

**Check:** `ssh pi@<energy-host> sudo -n true` succeeds silently.

---

## Phase 6 — Storage for Docker (workers only)

Do this **before** enabling the read-only root, and follow
[SETUP.md step 0.1 and 0.2](./SETUP.md) exactly — it covers the persistent
mount, the `nofail` flag, the `RequiresMountsFor` drop-in on Docker, and
keeping the journal off the overlay.

Short version: the workers get a read-only root so that ~100 hard power cuts
per run cannot corrupt them, but Docker's data directory has to stay
persistent or your cached function image dies with every power cut.

---

## Phase 7 — Tinkerforge (leader only)

Only the leader talks to the relays.

```bash
sudo apt install -y brickd
sudo systemctl enable --now brickd
```

Connect the Master Brick by USB, then attach the two Industrial Dual Relay
Bricklets and the Voltage/Current Bricklet 2.0.

Read the UIDs — install Brick Viewer on your **Mac** (it is a GUI) and point
it at the leader's IP on port 4223, or run `brickv` on the Pi if you have a
desktop:

```bash
# on the leader, confirm brickd is listening
sudo ss -lntp | grep 4223
```

Record all three UIDs. You need the two relay UIDs for `nodes.json` and the
Voltage/Current UID for the energy logger.

### ⚠ Wiring the relays

The relays switch each worker's power supply on and off. **How you wire this
is an electrical safety question, not a software one.**

Switch the **low-voltage DC side** (between the PSU and the Pi) if at all
possible, rather than mains. If mains switching is unavoidable, have it
checked by your lab supervisor or the institute's technician before energising
it — TU Berlin will have rules about this, and mains wiring is not something
to improvise from a README.

Never wire the leader's own supply through a relay. It is the machine issuing
the power commands; cutting its own power ends the experiment. The code
refuses to power-cycle the node marked `"local": true`, but do not rely on
software to prevent a wiring mistake.

---

## Phase 8 — Copy the binary

One binary runs on all five machines; only the environment differs.

On your Mac:

```bash
cd Yuliya-Bachelorarbeit/tinyFaaS-ext
uname -m                      # on a Pi: aarch64 → arm64, armv7l → arm
make tinyfaas-linux-arm64     # ~162 MB, includes rproxy and all runtimes

for ip in 192.168.1.100 192.168.1.101 192.168.1.102 192.168.1.103 192.168.1.104; do
  scp tinyfaas-linux-arm64 pi@$ip:/tmp/
done
```

On each Pi:

```bash
sudo install -o tinyfaas -g tinyfaas -m 0755 \
  /tmp/tinyfaas-linux-arm64 /opt/tinyfaas/tinyfaas-mgmt
```

**Check:** `/opt/tinyfaas/tinyfaas-mgmt --help` runs (any output, including a
usage error, proves it executes — a wrong architecture gives
"cannot execute binary file").

---

## Phase 9 — Hand over to SETUP.md

Each Pi should now have: a fixed address, a synchronised clock, Docker, the
`tinyfaas` user, key-based SSH from your Mac, and the binary at
`/opt/tinyfaas/tinyfaas-mgmt`.

Continue with **[SETUP.md](./SETUP.md)** from step 2, which installs the
systemd unit, configures the leader, deploys the `edge` function, and proves
a wake/sleep cycle.

Reminder on ordering: bring up the **leader and one worker** and get a full
cycle working before touching the other three. Every failure at this stage is
identical across machines — find them once.

---

## Quick reference

Per machine, once everything is done:

| | Leader | Worker |
| --- | --- | --- |
| Fixed IP | ✓ | ✓ |
| NTP synchronised | ✓ | ✓ |
| Docker | ✓ | ✓ |
| `tinyfaas` user + binary | ✓ | ✓ |
| systemd unit enabled | ✓ | ✓ |
| `nodes.json` + env | ✓ | ✗ |
| brickd + bricklets | ✓ | ✗ |
| Persistent Docker disk | ✗ | ✓ |
| Read-only root | ✗ | ✓ |
| Power via relay | **never** | ✓ |

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `ssh: Could not resolve hostname edge-1.local` | mDNS blocked; use the IP. |
| `Permission denied (publickey,password)` | Key not copied, or the username differs from your Mac's. |
| `docker: permission denied` | User not in the `docker` group, or you did not log out and back in. |
| Random reboots under load | Underpowered PSU. A Pi 4 needs a genuine 5V/3A supply. |
| Pi unreachable after a reboot | DHCP gave it a different address — do Phase 2. |
| Energy figures look implausible | Clock skew between the Mac and the energy host — do Phase 3. |
| Energy logger never starts during a run | sudo is prompting for a password over SSH — do Phase 5. |
| `cannot execute binary file` | Wrong architecture; check `uname -m` and rebuild. |
