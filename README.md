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

- **[SETUP.md](./SETUP.md)** — the whole build, top to bottom: from freshly
  imaged SD cards through addressing, clocks, Docker, relays, per-node energy
  measurement, and a proven wake/sleep cycle, to running the experiment.
- **[tinyFaaS-ext/README.md](./tinyFaaS-ext/README.md)** — cluster mode,
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


For Thinkpad energy measurements:
cd ~/energy-measurements && sudo env ENERGY_NODES="leader=26gZ,pi1=26vg,pi2=26mi,pi3=26iw,pi4=26vf" ./energy-logger
