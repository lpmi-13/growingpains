# Growing Pains

One tiny shop. The traffic decides how it grows.

A small web shop starts on one VM: nginx, one Python API process, and PostgreSQL 18. A seeded crowd
of shoppers, sellers, and bots sends it real, state-changing traffic that grows steadily. An
autonomous **architect** watches only the shop's telemetry, works out what kind of pressure the shop
is under, and makes the change an experienced engineer would make: an autoscaled fleet, a read
replica, a cache, and more. Every change must keep the shop inside its SLOs, and every change is
explained in plain words by a **narrator**.

The design is in [`../plan-docs/GROWING_PAINS_PLAN.md`](../plan-docs/GROWING_PAINS_PLAN.md). This
repository implements it in the plan's build order. Where the build departs from the plan, the
reason is in [docs/plan-deviations.md](docs/plan-deviations.md). Measurements are in
[docs/calibration.md](docs/calibration.md).

## Plan ledger

The running record of where the build stands, so work can pause and resume at any point.
**Done** means implemented and exercised by tests or a real run. **Partial** means started, with
the remaining work listed. Update this ledger and add a dated line to the work log at the end of
every working session.

**Nothing has run on an iximiuz playground yet.** Everything so far runs locally: unit tests, a
Docker stack that stands in for `node-01` and `ops-01` at genesis, and the twin.

### Resume here

State at the end of the last session (2026-11-13):

- **Waiting on the owner:** approval to start a private premium playground session. Do not run
  `e2e/private_session.py start` or any `labctl playground start` until the owner says yes. It
  uploads roughly 3–4 GB of image layers (the node overlay goes to four VMs) and uses session time
  on the owner's account. Publishing images (`ghcr.io`, a public playground) also needs the
  owner's approval.
- **No playground session is running.** If one is ever left running, `labctl playground list`
  shows it and `labctl playground destroy <id>` removes it.
- **Nothing is committed to git.** The repository is initialised with no commits, so this ledger
  is the only history. Commit only when the owner asks.
- **The local stack is stopped, not removed.** `make dev-up` restarts it. Its architect volume
  keeps state and events from the observe-mode runs, so run `make dev-reset` (which also deletes
  the database volume) or remove the `growingpains_architect` volume before a clean run.
- **The built VM images are stale:** the crowd changed after the last `make images` (the capped
  session mean). Rebuild before booting them. Secrets in `rootfs/generated/` are generated once;
  `make new-credentials` replaces them, and then both images must be rebuilt.
- **Sanity check after resuming:** `make test` (all Go tests and 9 shop tests pass) and
  `go run ./cmd/gp-twin -sweep 20` (every seed should take autoscaled-fleet → read-replica → cache).

Next steps, in order:

1. With approval: `make images`, then `python3 e2e/private_session.py start`.
2. On first boot, check what could not be tested locally. Each is a guess until seen on a VM:
   - k3s: all four agents join `ops-01`, each with the labels and taints `node-role` writes;
     the air-gap tarballs import (`k3s ctr images ls` on a node).
   - CoreDNS and metrics-server schedule on `ops-01` despite its control-plane taint. Nothing
     else can take them, because every node starts tainted.
   - `node-01`: `gp-node-role` enables nginx, `postgresql@18-main`, and `gp-api`; the genesis API
     runs from `/opt/shop/rootfs` under `RootDirectory=`; the shop answers on port 80.
   - nginx on `node-01`'s host network reaches CoreDNS at 10.43.0.10, which the fleet's
     `resolve` upstream needs.
   - The agent's cgroup globs match (`system-postgresql.slice/postgresql@18-main.service`,
     `gp-api.service`).
   - Prometheus on `ops-01` scrapes `node-01:8000`, the agents, and API pods by Kubernetes
     discovery.
   - Pods reach PostgreSQL on `node-01` from the node's address, which is what `pg_hba` assumes.
   - The architect reaches every node over SSH through the dispatcher (`ssh root@node-01 status`
     with `/etc/gp/id_ed25519`).
3. Run the storyline with the architect enabled; fix; repeat until Move 1 is error-free 10 of 10
   times (stage 3's gate).
4. Then stage 4: Moves 2 and 3 on the playground, the run report, the dashboard's architecture
   map, and the determinism and control runs.

Working notes for whoever picks this up:

- Port 8080 on the development machine belongs to another project, so the local shop is on 18080.
- The shell here wraps `curl` and `grep` through `rtk`, which reformats their output. When piping
  JSON or exact matches into another program, use `rtk proxy curl ...` or `rtk proxy grep ...`.
- `make images` rebuilds the lab image from `cmd/` and `internal/`; a new embedded directory
  (like `contracts/` or `deploy/k8s/`) must also be copied in `build/lab.Dockerfile`, or the
  build fails.

### Build order (the plan's "Build order and decision gates")

| # | Stage | Status | Gate | Evidence and work remaining |
| --- | --- | --- | --- | --- |
| 1 | Spike and calibration | Partial | Measured capacity units allow the MVP storyline at 6 vCPU or less | Local calibration: one Python process passes 75% CPU at ~200 req/s; ~4 ms API CPU and ~3 ms database CPU per request ([calibration.md](docs/calibration.md)). The twin, fed these costs, peaks at ~2.5 API cores and ~2.4 database cores before the cache. **Remaining:** every spike-checklist item on real playground VMs. |
| 2 | Pain without a cure | Partial | The storyline breaches the SLOs at about the predicted times with no architect | **Met on the local stack** with the current storyline (seed 42): the API process passed 75% CPU at ~4:00, read p95 passed 250 ms at 5:00, and 1-minute availability fell under 99.5% at 6:00 and stayed near 1–2% to the end. The observing architect armed `api-compute` at 3:15 and recorded that it would choose the fleet. See [calibration.md](docs/calibration.md#no-architect-run-the-stage-2-gate). **Remaining:** the same run on a playground. |
| 3 | Architect core, twin, and Move 1 | Partial | Zero-error fleet migration in 10 of 10 runs | Done: architect core (signals with lint, pressures with sustain, hysteresis, and forecasts, contracts, ledger, diagnosis, persisted step machine with guards, rollback, and verification); twin (100 of 100 seeds take fleet → replica → cache, worst 5-minute availability ≥ 99.92%, 85 pass validation); `gp-architect` command with the runtime config server, event log, Grafana annotations, metrics, and API, running in observe mode against the local stack; playground actuator (Kubernetes server-side apply, node taints, readiness probes, nginx weighted upstreams, SSH dispatcher, replication SQL); k3s manifests; node scripts and units; VM images (`make images` builds `growingpains-node-rootfs` 3.1 GB and `growingpains-ops-rootfs` 4.0 GB); playground manifest renderer; private-session tool. **Remaining:** boot the images on a private playground session (nothing has run there yet), fix what that finds, then Move 1 for real, ten times. |
| 4 | Moves 2 and 3, plus narration (MVP) | Partial | The MVP success criteria | Contracts for replica, cache, and the stretch pooler drive the twin and the playground actuator. Narrator done: traffic, pressure, decision, and outcome notes from templates, ADR-style decision records, Grafana narration annotations, and a live SSE page with Start; tested by replaying twin runs. **Remaining:** run report, the Canvas architecture map, determinism and control runs on playgrounds. |
| 5 | Branching | Not started | | |
| 6 | Heavier moves | Not started | | |
| 7 | Sharding | Not started | Citus packaging, online distribution in about two minutes | Ubuntu 26.04 has no Citus package; the Citus repository is a spike question. |
| 8 | Extras | Not started | | |

### Components

| Component | Where | Status | Notes |
| --- | --- | --- | --- |
| Shop API (Python 3.15, Starlette, psycopg 3, Jinja2) | `shop/` | Done for genesis | Every endpoint, idempotency keys, read-your-writes tokens, runtime config polling, read pool with replicas, cache-aside with single-flight and a breaker, per-route CPU accounting. 9 tests, including end to end against PostgreSQL 18. |
| Schema and base catalog | `db/`, `internal/catalog`, `cmd/gp-seed` | Done | 50,000 products, deterministic; product IDs encode the seller. |
| Crowd | `internal/crowd`, `cmd/gp-crowd` | Done for the MVP storyline | Open-loop seeded Poisson arrivals, per-session streams, seller actors with a ledger, offered-load accounting. Personas beyond shopper, visitor, and seller are not built. |
| Storylines | `internal/storyline`, `internal/traffic` | "Word gets out" and the calibration ramp | Other families and the grammar come in stage 5. |
| Prober | `internal/prober`, `cmd/gp-prober` | Done | Journey canaries, read-your-writes, detail and listing staleness. |
| Node agent | `internal/agent`, `cmd/gp-agent` | Done | cgroup CPU against quotas, PostgreSQL statement classes, edge shape signals from nginx's log. |
| Judge | `deploy/prometheus/rules/judge.yml` | Done | SLIs, projected error budget, burn rates, offered-load rules. |
| Dashboard | `tools/render_dashboard.py` | Partial | Run bar, SLO tiles, traffic, latency, errors, pressure, capacity, data tier, narration list. **Missing:** the Canvas architecture map and the move table. |
| Architect | `internal/architect`, `internal/signals`, `contracts/`, `cmd/gp-architect` | Done, untested on VMs | Observe mode runs in the local stack. The playground actuator (`internal/actuate`, `internal/kube`, `deploy/k8s`) has unit tests but has never touched a real cluster. |
| Twin | `internal/twin`, `cmd/gp-twin` | Done for the MVP | Seed sweeps, validation, load breakdown (`-explain`). |
| Narrator | `internal/narrator`, `cmd/gp-narrator` | Done for the MVP | Run report and living diagram are not built. |
| Run report, growth tree | | Not started | |
| VM images, playground manifest, private sessions | `rootfs/`, `playground/`, `e2e/private_session.py`, `tools/generate_credentials.py` | Built, never booted | One node image for all four nodes (role by hostname), one `ops-01` image. Secrets go to `rootfs/generated/` (ignored by git). |

### Work log

Newest first. Each entry says what changed and what to pick up next.

- **2026-11-13 (night)** — Ran the stage-2 gate locally: with no architect the SLOs collapse
  from 5:00–6:00, and the observing architect would have chosen the fleet at 3:15. Wrote the
  `gp-architect` command, runtime config server, Grafana annotations, the narrator and its page,
  the playground actuator, k3s manifests, node dispatcher and actions, systemd units, the VM
  images, the playground manifest, and the private-session tool. Fixed the crowd's session
  length (sessions are capped, so the mean is 4.66 steps, not 5). Stopped the local stack.
  **Next:** see "Resume here": the first private playground session, once the owner approves.
- **2026-11-13 (later)** — Built the architect core and the twin, then used the twin to calibrate
  the MVP storyline: a logistic chapter 1, faster mix ramps, price-filtered browsing, expensive
  "top deals" pages, forecast arming, a fleet-only guard, a projected error budget, traffic-scaled
  verification, and a primary-weighted read pool. Recorded each change against the plan in
  docs/plan-deviations.md. **Next:** record the no-architect rerun; write `gp-architect` (config
  server, HTTP API, real actuators, annotations) and the k3s manifests; then VM images, the
  playground manifest, and a first private playground session.
- **2026-11-13** — Built the genesis shop, the deterministic catalog and seeder, the crowd,
  prober, node agent, judge rules, dashboard, and a local Docker stack. Calibrated locally over
  two ramps (docs/calibration.md), which changed the listing queries, added a price index, moved
  SQL tags, and cut search in the MVP mix. Added the architect's signal allowlist.
- **2026-11-12** — Repository started. Read the plan and the Break Team implementation, which this
  lab reuses for the playground manifest, image build, and private-session testing patterns.

## Run it locally

Needs Docker, Go 1.27 (fetched automatically by the `go` command), and `uv` for the shop's tests.

```sh
make test                      # Go tests, then the shop's tests (starts a PostgreSQL 18 container)
make dev-up                    # build and start the local genesis stack
make dev-start SEED=42         # press Start: the crowd begins "Word gets out"
make dev-status
```

The local stack's ports: the shop at <http://127.0.0.1:18080>, Grafana at
<http://127.0.0.1:3000> (dashboard "Growing Pains"), Prometheus at <http://127.0.0.1:9090>, the
architect at <http://127.0.0.1:7000/api/state>, the narrator at <http://127.0.0.1:18081>, and the
crowd's control API at <http://127.0.0.1:9101>. The local stack has no Kubernetes, so its architect
runs in observe mode: it arms pressures and records what it would do, but never moves. A run
there is the plan's "pain without a cure" and its control run.

Summarise any run with `python3 tools/run_summary.py --start <unix seconds of Start>`.

To measure capacity, run the calibration ramp instead of the storyline:

```sh
make dev-up STORYLINE=calibrate:60:420:60
make dev-start
make dev-report START=<unix seconds of Start> STEPS=7
```

## Build the playground images

```sh
make images                               # shop and lab images, k3s air-gap tarballs, both VM images
python3 e2e/private_session.py start      # boot them on a private session, nothing published
```

### The twin

```sh
go run ./cmd/gp-twin -seed 42            # one run: a timeline and every decision with its options
go run ./cmd/gp-twin -sweep 100          # paths, decision times, and seed validation
go run ./cmd/gp-twin -seed 42 -disabled  # control run: pressures arm, nothing moves
go run ./cmd/gp-twin -explain 3m,9m,13m  # the load broken down by operation at those times
```

## Layout

```text
shop/                 the Python API that grows (genesis code, templates, tests, Dockerfile)
db/                   schema and PostgreSQL settings
contracts/            move contracts and pressures (YAML), embedded in the architect and the twin
cmd/                  gp-seed, gp-crowd, gp-prober, gp-agent, gp-twin, gp-architect, gp-narrator
internal/architect    the slow control loop: pressures, diagnosis, ledger, step machine
internal/signals      the architect's allowlisted PromQL
internal/twin         the simulator
internal/crowd        the load generator; internal/traffic picks what each request asks for
internal/storyline    storylines and their chapters
internal/agent        the node exporter
internal/narrator     notes, decision records, and the narrator page
internal/actuate      the playground actuator; internal/kube is its Kubernetes client
internal/runtimecfg   the runtime config document the API polls
internal/catalog      the fixed base dataset
deploy/               nginx, Prometheus rules, Grafana provisioning and dashboard, k3s manifests
rootfs/               VM image Dockerfile and each role's files (node, ops)
playground/           playground manifest renderer
e2e/                  private playground sessions
dev/                  the local Docker stack
tools/                dashboard generator, credentials, calibration report, run summary
docs/                 calibration notes and plan deviations
```
