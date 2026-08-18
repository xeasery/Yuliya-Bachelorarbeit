# Improving Energy Efficiency of FaaS Serverless Platforms via Hardware Orchestration at the Edge

Bachelor's thesis, Yuliya Sasinskaya — TU Berlin / MCC.

Serverless platforms scale functions to zero, but the hardware underneath
stays powered to keep the OS, container engine and control plane alive. This
work extends [tinyFaaS](https://github.com/OpenFogStack/tinyFaaS) so a leader
node powers cluster nodes on and off according to demand, and measures what
that saves against a baseline without hardware orchestration.

## Repositories

The work spans three repositories:

| Repository | Contents |
| --- | --- |
| **this one** | `tinyFaaS-ext/` — the extended platform: cluster registry, power-aware scheduler, relay control, the `edge` evaluation function. |
| [`k6_client_BA`](https://github.com/xeasery/k6_client_BA) | Load generator and measurement pipeline: workload scenarios, per-invocation recording, energy correlation, comparison plots. |
| [`energy-measurements`](https://github.com/xeasery/energy-measurements) | Tinkerforge Voltage/Current logger producing the power trace. |

## Getting started

Read in this order:

1. **[PI_BRINGUP.md](./PI_BRINGUP.md)** — from bare Raspberry Pi OS to
   machines you can SSH into: addressing, clocks, Docker, accounts, keys,
   Tinkerforge.
2. **[SETUP.md](./SETUP.md)** — from there to a working cluster: the systemd
   unit, read-only root, `nodes.json`, the function image, and a proven
   wake/sleep cycle.
3. **[tinyFaaS-ext/README.md](./tinyFaaS-ext/README.md)** — cluster mode,
   configuration variables, and the baseline switch.

## The experiment

The same workload runs twice, differing only in whether the leader powers
nodes down:

```bash
POWER_AWARE=false ...   # always-on baseline
POWER_AWARE=true  ...   # power-aware
```

Both halves of the trade-off are reported: energy and node-time saved, against
the wake latency paid for it.
