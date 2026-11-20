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

**The MVP gate passed on private playground VMs on 2026-11-20.** Ten automatic-start runs of
seed 1003 all took fleet → replica → cache without intervention, completed and verified all
three moves by 15 minutes, and passed the full SLO checks. Across 4,422,391 requests there
were zero errors, 170/170 compliant latency minutes, 100% worst full five-minute journey
availability, and zero correctness violations. Decision spreads were 5/5/5 seconds. The
finished report, Grafana views, move records, and both database quotas passed inspection.
Stage 3's earlier Move 1 gate also passed on freshly built images: 10/10 error-free runs.

### Resume here

State after the 2026-11-20 final MVP checks:

- **The owner authorized pursuing all avenues to a working, fully autonomous MVP and changing
  the proposed service progression** (2026-11-17). This covered the private full-storyline and
  repeatability work. Each private session uploads roughly 3–4 GB of image layers
  and uses session time on the owner's account, so destroy it when done. Publishing images to
  `ghcr.io` or starting a public playground has not been requested.
- **Playground sessions:** `labctl playground list` shows any that are running and
  `labctl playground destroy <id>` removes one. The id of any session left running is recorded
  here. **No Growing Pains private session is running.** `6b003880139ebf9a02c2cae3`
  passed the final automatic-start ten-run gate, seed-727274 cross-seed trial, and both
  final-image controls, then was destroyed on 2026-11-20.
  `6aff8273139ebf9a02a25e0d` passed an automatic-start run, then was destroyed by its
  expiry while the revised ops image was applied on 2026-11-20.
  `6aff2e7078d2128757a1c9ca` completed the earlier gate and cross-seed trials and was
  destroyed on 2026-11-19. `6afed80713e12b000c9f62a6` passed an unattended trial and
  was destroyed on 2026-11-19. `6afe257dfd7ce1aae088a0bd` expired after
  the delayed-control and two automatic-start diagnostic runs. `6afda0b54d0c76e1364e0c0c` completed the ten-run gate and
  expired automatically before the controls. `6afc674163321e6eb4e7e97a` expired automatically
  by 2026-11-18 12:41 UTC after its first repeatability trial failed and is absent from the
  list.
  `6afc375b4bb0c11ddef73787` (step 5's measurement and control runs
  and a quota benchmark, started by the owner from the 10:39 images) was destroyed on
  2026-11-17 after its runs; `6afa3744d86d8571fbca0ce2` (step 4's runs) on 2026-11-15
  (evening); `6af904b0620f7b202454351c` (full storylines and the control run) on 2026-11-15
  after its runs; `6af8e071620f7b20244a587f` (the second gate) earlier that day;
  `6af79014918a23974508d931` (first boot and the first gate) on 2026-11-14.
- **The live VM images were rebuilt 2026-11-17 with the search indexes, clean database
  snapshot, automatic Start, and faster cache transition.** They booted without hand fixes;
  five nodes were Ready, and the reset verified the snapshot checksum and 50,000 products.
  The first repeatability trial missed search's availability and one latency minute: three
  timeouts in 440,583 requests, all outside moves. The rebuilt images add replica CPU headroom
  at the cache decision and the expanded report, move metric, Grafana Canvas, and completed-run
  behavior. The first trial on them saw the replica decide at 8:43 and read p95 hit 706 ms at
  10:00 while its backup ran; lowering the primary read-pressure threshold to 60% brought the
  next trial's replica decision to 6:42. That trial completed all moves by 13:42 with one
  search timeout in 439,920 requests, but read p95 reached 370 ms in the minute ending 12:00,
  before the cache raised the replica quota at 12:02. The cache now becomes eligible once
  repeat ratio reaches 0.4 after a replica exists, and post-replica evidence is 5 s. The first
  rebuilt-image trial chose cache at 11:42, raised the quota at 11:52, and passed all 17
  latency minutes with zero errors in 439,332 requests. All ten repeated trials passed, with
  zero errors across 4,400,592 requests, 17/17 latency minutes in each run, and 100% worst
  full in-run five-minute availability. The rolling five-minute
  gate now measures only full in-run windows, excluding reset/genesis samples. `e2e/` is copied at run time.
  A 90-second delayed control exposed an error-budget deadlock: once availability fell to 92% at
  4:00, the architect rejected the fleet because its architecture class was not marked
  protective; availability then fell near zero. The three reversible MVP moves now explicitly
  remain eligible at low budget. The corrected 90 s delayed control chose the fleet with only
  1.37% of its error budget left, verified the fleet and replica, and failed the SLO as expected:
  first latency breach at 4:00, first availability breach at 5:00, worst full five-minute
  availability 50.87%, and 6/17 compliant latency minutes. On 2026-11-19 the first
  automatic-start run passed all SLO windows, but a repeat missed the 12:00 read-latency window
  because the replica's 60 s post-move cooldown delayed the cache. The replica now uses a 15 s
  cooldown after verification; a rollback still waits 60 s. The next repeat missed the 4:00
  latency window while the legacy API remained under too much traffic. The API forecast now
  arms at 60% two minutes ahead, and the first fresh-VM trial shifted the fleet by 3:45 and
  cleared that minute. It missed the final 17:00 latency window: seven late browse/search
  timeouts, 284 ms read p95, and substantial database quota throttling despite spare physical
  CPU. The cache move now raises both database caps to their already-reserved 1.5 cores.
  **The new automatic-start trial of those caps passed on `6afed80713e12b000c9f62a6`:
  442,212 requests, zero errors, 17/17 latency windows, and 100% worst five-minute availability.
  The report, dashboard, and 1.5-core quotas on both databases were checked. A same-seed gate
  then passed eight runs and failed run 9 at the 12:00 read-latency window (271 ms) when the
  cache quota lift landed six seconds later. The cache's post-replica repeat threshold is now
  0.3 and the replica cap lifts first. **The revised-image automatic-start trial passed:**
  442,220 requests, zero errors, 17/17 latency windows, 100% worst five-minute availability,
  and all moves verified by 13:05. It chose cache at 11:10, raised the replica quota at 11:20,
  and read p95 was 25 ms at 12:00. Its report, dashboard move records, and both 1.5-core quotas
  passed inspection. On `6aff2e7078d2128757a1c9ca`, the revised ten-run seed-1003 gate
  passed: 4,422,239 requests, two connection errors outside all move windows, 17/17 latency
  minutes in each run, 100% worst full five-minute journey availability, and decision spreads
  of 15/15/15 seconds. A separate seed-727274 run then found two search timeouts and 340 ms
  read p95 at 11:00 before the cache's quota lift, leaving 99.397% worst search availability
  and 16/17 compliant minutes. The replica spent 86% of the 10:00–11:00 minute throttled at
  its 0.65-core quota. The next revision raises both databases to their already-reserved 1.5
  cores as part of the replica move, before routing reads, and gives the cache a measured
  read-repetition pressure. Local tests pass and 100 of 100 twin seeds retain the path. A
  seed-727274 live trial on the revised progression passed with 466,829 requests, zero errors,
  17/17 latency windows, 100% worst five-minute availability, and 12 ms read p95 at 11:00;
  all moves were verified by 12:33. The report, dashboard, and both 1.5-core quotas passed.
  A final ops-image rebuild clears `read-reuse` pressure once the cache is active. Its fresh-VM
  automatic-start run passed: 442,191 requests, one isolated error, 17/17 latency windows,
  100% worst full five-minute availability, no correctness violations, and all moves verified
  by 12:50. The report, Grafana, and both 1.5-core quotas passed. A ten-run gate's first four
  manual-start repeats passed, but run 5 chose the fleet 99 s later after only a 60 s calm
  reset, beyond the gate's 60 s decision-spread rule. A 180 s manual hold collided with the
  crowd's automatic three-minute Start. The gate now follows that automatic Start itself, which
  gives the architect a clean forecast history and directly repeats the hands-free path.
  An unattended gate run then chose the fleet around 2:15 and had 1,094 ms read p95 at 3:00,
  so it was stopped. The fleet pressure now also arms on sustained measured traffic above
  30 req/s with 25% current API CPU, with a 20 req/s floor that blocks idle telemetry. Local
  tests pass; a 100-seed twin sweep retained all three moves with fleet decisions between
  1:35 and 2:00. A fresh private session `6b003880139ebf9a02c2cae3` passed the revised-image
  automatic-start run: 442,242 requests, one isolated error, 17/17 latency windows, 100% worst
  five-minute availability, zero correctness violations, and all moves verified. The 3:00 read
  p95 was 22 ms. The finished report, Grafana, and both 1.5-core quotas passed inspection.
  Its first automatic repeat chose the replica at 8:10, 110 s later than the standalone run
  on that image. The primary remained within SLO after its quota lift, but this timing erodes
  the cache margin. A new `db-read` lead signal arms after 45 s of a read-majority workload
  above 280 req/s before a replica exists. `make test` and a 100-seed twin sweep pass; all
  seeds take the three moves, with replica decisions from 5:15 to 6:15 and 100/100 valid.
  A rebuilt ops image passed a new automatic-start seed-1003 run: 442,243 requests, zero
  errors, 17/17 latency windows, 100% worst full five-minute availability, and no correctness
  violations. Fleet, replica, and cache were chosen at 2:05, 6:25, and 11:15 and all verified
  by 12:55. The finished report, Grafana, and both 1.5-core quotas passed inspection. The
  final ten-run automatic-start gate passed: 10/10 identical paths and full MVP SLO passes,
  4,422,391 requests with zero errors, 170/170 compliant latency minutes, 100% worst full
  five-minute availability, zero correctness and move-window errors, and 5/5/5-second
  decision spreads. The final report, Grafana, and both quotas passed inspection. A separate
  seed-727274 live trial on this image passed: 466,837 requests, zero errors, 17/17 latency
  minutes, 100% worst full five-minute availability, and all moves verified by 12:33. Its
  report, dashboard, and both quotas passed inspection. On the final image, a 90 s delayed
  control failed latency (16/17 minutes; 982 ms p95 at 5:00), and an architect-disabled
  control failed availability and latency (0% worst five-minute availability; 3/17 minutes).
  Both controls used seed 1003. The private session was destroyed.**
  Completed-run Prometheus history
  now survives private ops image updates, and the
  narrator reads request totals at run end plus one minute so its report survives restarts.
  Secrets in `rootfs/generated/` are generated once
  (`agent-dsn` and `playground.json` there were edited in place to keep the same secrets);
  `make new-credentials` replaces them, and then both images must be rebuilt.
- **Git:** the owner made the initial commit (`ff3289a`) on 2026-11-13. Nothing since is
  committed. Commit only when the owner asks.
- **The local stack is stopped, not removed.** `make dev-up` restarts it. Its architect volume
  keeps state and events from the observe-mode runs, so run `make dev-reset` (which also deletes
  the database volume) or remove the `growingpains_architect` volume before a clean run.
- **Sanity check after resuming:** `make test` (all Go tests and 9 shop tests pass) and
  `go run ./cmd/gp-twin -sweep 20 -api-cost 1.075` (every seed should take autoscaled-fleet →
  read-replica → cache; `-api-cost 1.075` is the playground's 40% genesis cap). The journey
  model predicts every path and keeps each seed's worst journey above 99.5% availability.

Tools for a private session:

- `python3 e2e/private_session.py start` boots the built images; `update <id> ops|node|<vm>`
  re-applies one image.
- `python3 e2e/reset_genesis.py <id> [--observe]` shrinks the session back to genesis in about
  20 s (restores the checksummed clean database, undoes the cache, fleet, replica, pooler, CPU
  cap, taints, and edge; clears run state).
  `--observe` starts the architect observe-only, for the control run.
- `python3 e2e/move1_gate.py <id> --seeds ...` runs stage 3's gate: reset, a calm minute, Start,
  wait for the fleet move, and count every request from decision to verdict. One JSON line per
  run.
- `python3 e2e/storyline_run.py <id> --seed N [--observe]` runs the whole 17-minute storyline:
  the events as they happen, a row a minute, `run_summary.py`'s table, the errors in each
  move's window, and the statement windows (database time by statement, CPU and throttling by
  unit). `--lift-replica-at 15:00` removes the replica's CPU quota at 15:00, so the 15:00–17:00
  window measures CPU rather than throttling; the next reset puts the quota back.
- `python3 e2e/inspect_run.py <id>` scores an unattended completed run before reset, including
  every one-minute latency window and the worst rolling five-minute availability.
- `python3 e2e/unattended_run.py <id>` resets to genesis, observes the crowd's automatic Start
  without calling Start, and verifies the full MVP path and SLO gate. `inspect_presentation.py`
  checks the finished report, architecture view, dashboard move-error counts, and replica quota.
  Automatic Start uses seed 1003 from `gp-crowd.service`; for another seed, use
  `storyline_run.py --seed N` to issue Start explicitly before the automatic timer.
- `python3 e2e/determinism_gate.py <id> --seed 1003 --runs 10 --pause 0` repeats the same seed
  from the clean snapshot, checks all three moves, the SLO gate, per-move errors, and the
  decision-time spread, and saves full logs under `e2e/.logs/`.
- `python3 e2e/quota_bench.py <id>` measures what the database's CPU quota does to latency on
  an idle session (one session): the shop's rating sort and search from pgbench at fixed rates,
  under the images' quota, a 100 ms period, `cpu.max.burst`, and no quota. It stops nginx and
  the API on `node-01` while it runs and restores everything after.
- **Connections:** every `labctl ssh` is a new session through the platform. A run is watched from
  inside `ops-01` by `e2e/ops_runner.py`, started with `systemd-run` and followed over one session
  (`e2e/lab.py`, which counts them), so a run costs five sessions: four for the reset and one to
  follow. A replica lift costs one more. Until 2026-11-15 the gate opened a session for every 15 s
  poll, about 32 a run and 320 for a ten-run gate (the issue Break Team fixed for its
  port-forward). Inside `ops-01`, the runner keeps one connection open to each local service
  (Prometheus took 275 queries over one in a test).

What first boot and the first runs found (all fixed in the repository unless marked open):

- **Pass on first boot:** k3s agents join with the labels and taints `node-role` writes; the
  air-gap tarballs import; CoreDNS, metrics-server, and local-path run on `ops-01`; `node-01`
  runs nginx, PostgreSQL, and the genesis API from `/opt/shop/rootfs`; nginx's host network
  resolves through CoreDNS; the agent's cgroup paths match; Prometheus scrapes every target and
  discovers API pods; pods reach PostgreSQL on `node-01`; the architect's key reaches the
  dispatcher.
- `private_session.py` failed: BuildKit kept the base image only in its build cache. It now pulls
  it.
- k3s has no `token-auth-file` option of its own and skipped that key, so the architect's and
  Prometheus's tokens got 401. Now passed as `kube-apiserver-arg`.
- Root on `ops-01` had no kubeconfig, so `kubectl` reached the narrator on `localhost:8080`.
- The architect probed the legacy API at nginx's address (`127.0.0.1:8000`), so the fleet barrier
  timed out. It now uses `legacy_addr` (`node-01:8000`).
- The agent's DSN had spaces inside `GP_AGENT_ARGS`, which systemd splits, so it never reached
  PostgreSQL and `db-read` could never arm. It is now a URL.
- A step's hold counted from the tick's start, before a blocking action, so the 20 s catch-up
  barrier passed 3 s after a 17 s base backup. Fixed, with
  `TestHoldCountsFromAfterABlockingAction`.
- An uncapped base backup took all of `node-01`'s ~20 MB/s link for 15 s, and ~3,500 requests
  timed out. Now `--max-rate=6M` (57 s for ~330 MB); the next run's replica move had zero errors.
  The twin's `replicaReady` is now 55 s.
- The fleet's queue-time relief was judged across 3.5× traffic growth and called
  "under-delivered". Queue time is now `report_only`; the check decides
  (`TestFleetQueueTimeIsReportedNotJudged`).
- Node clocks were 10–20 ms apart (each synced to internet NTP on its own). Queue time crosses
  hosts (a pod's clock minus nginx's timestamp on `node-01`), so the fleet's 10 ms queue check
  failed in three error-free runs on skew alone. The images now run chrony on every VM, with
  `ops-01` the nodes' only source. **Booted 2026-11-14:** nodes within 6 µs of `ops-01`.
- **The genesis API had a full playground core, which is ~2.4 local cores** (found
  2026-11-14 on the rebuilt images). The API costs 1.8 ms of CPU per request on a playground
  vCPU against 4.3 ms on the local stack that the twin and storyline are calibrated on. With
  `CPUQuota=100%` genesis peaked near 50% of its ceiling in chapter 1, so `api-compute` armed
  only on its forecast, at 3:50–4:45, and seed 1003 never armed (no fleet move in 9 minutes).
  The old session decided early only because its genesis queue time was 9 ms rather than 2 ms.
  The genesis unit is now `CPUQuota=40%` (one local core's worth); pods keep their 1-core
  limit. See [docs/plan-deviations.md](docs/plan-deviations.md) and
  [docs/calibration.md](docs/calibration.md#rebuilt-images-on-a-fresh-session-14-november-2026).
- `private_session.py update` unpacked the node image's seeded database over a cluster that
  had run, and PostgreSQL could not start ("could not locate a valid checkpoint record"). It now
  stops PostgreSQL and lays the image's cluster down whole, so **an update resets that node's
  database to first boot**.
- **Resolved for the MVP (stage 4):** in seed 727274 the replica reached 98% CPU from about 10:30 while reads did
  not yet repeat enough for the cache's shape (`reads.repeat_ratio >= 0.6`, met at 11:30). Ten
  search requests timed out, and search's 1-minute availability dipped to 89.9% at 12:00; the
  whole run was 99.998% (441,451 requests). The twin did not predict it. **Again on 2026-11-15
  (evening), seed 1003:** the replica at 0.7–0.9 of its quota from 12:00, after the cache too,
  and search's 5-minute availability at 95.6–99.0% from 12:00. Part of the cause was the twin's
  read routing (fixed). **Measured 2026-11-17 (step 5): the quota causes it.** Uncapped, the
  replica needs 0.54 cores on the plateau, less than its 0.65 quota, and search stopped
  failing the moment the quota was lifted. Under a quota every statement shares one 0.65-core
  server, so at 80% busy a 67–146 ms search waits seconds (a benchmark: search p99 2.0 s with
  the 10 ms period, 2.6 s with 100 ms, 0.2 s uncapped). Search indexes in the seeded database
  and the cache move's lift to the databases' reserved 1.5-core ceilings now protect the
  plateau; see "Next steps", 6 and 7 for the measured results and final repeatability gate.
- `node-01`'s nginx kept Ubuntu's `worker_connections 768`; in the 2026-11-15 control run slow
  responses filled every slot and the API idled from 13:00. The node image sets 4096, and the
  2026-11-17 control run had no "worker_connections are not enough" and the API busy to the
  end. **Open (not blocking):** the control run still goes to 0% availability from 6:00,
  because the API's accept queue fills and it serves only requests whose clients have given up
  (191,614 upstream connect timeouts in nginx's log). The twin's control run degrades in
  proportion (15–84% errors); it has no goodput collapse.

Next steps, in order:

1. ~~**Stage 3's gate:** Move 1 error-free in 10 of 10 runs, on freshly built images.~~ **Met
   2026-11-15** on the rebuilt images with the 40% genesis cap: 10 of 10 completed and
   verified, zero errors in 501,301 requests across the ten moves. `api-compute` armed on
   real pressure (genesis peaked at 0.83–0.87 of its cap during the handover, queue p95 ~9 ms),
   and decisions came at 2:41–3:06, median 2:50 (the twin: 2:15–3:00).

   | Seed | Decided at | Move took | Requests in the move | Errors | Verdict |
   | --- | ---: | ---: | ---: | ---: | --- |
   | 102507 | 2:51 | 170 s | 45,709 | 0 | verified |
   | 727274 | 2:50 | 170 s | 48,720 | 0 | verified |
   | 1001 | 2:45 | 170 s | 53,754 | 0 | verified |
   | 1002 | 2:41 | 170 s | 51,297 | 0 | verified |
   | 1003 | 3:06 | 175 s | 46,503 | 0 | verified |
   | 1004 | 2:51 | 170 s | 46,441 | 0 | verified |
   | 1005 | 3:01 | 175 s | 47,334 | 0 | verified |
   | 1006 | 2:44 | 175 s | 57,843 | 0 | verified |
   | 1007 | 2:46 | 175 s | 49,607 | 0 | verified |
   | 1008 | 2:55 | 175 s | 54,093 | 0 | verified |

   Before the cap, on the same images with a full core, four seeds moved late (3:51–4:46,
   all verified, no errors) and seed 1003 never armed; see
   [calibration.md](docs/calibration.md#rebuilt-images-on-a-fresh-session-14-november-2026).
   The first gate, on the hand-patched session (2026-11-14), was also 10 of 10 error-free
   (461,482 requests), but three runs failed their queue check on clock skew and one was
   judged under-delivered:

   | Seed | Decided at | Move took | Requests in the move | Errors | Verdict |
   | --- | ---: | ---: | ---: | ---: | --- |
   | 102507 | 2:35 | 145 s | 35,575 | 0 | verified |
   | 727274 | 2:20 | 170 s | 43,693 | 0 | under-delivered (since fixed: `report_only`) |
   | 1001 | 2:26 | 170 s | 50,476 | 0 | verified |
   | 1002 | 2:26 | 170 s | 48,888 | 0 | queue check failed on clock skew |
   | 1003 | 2:36 | 175 s | 41,655 | 0 | queue check failed on clock skew |
   | 1004 | 2:35 | 175 s | 44,793 | 0 | queue check failed on clock skew |
   | 1005 | 2:35 | 175 s | 43,309 | 0 | verified (nodes now follow `ops-01`'s clock) |
   | 1006 | 2:31 | 170 s | 53,898 | 0 | verified |
   | 1007 | 2:31 | 175 s | 47,896 | 0 | verified |
   | 1008 | 2:36 | 175 s | 51,299 | 0 | verified |

   Every run in both gates shared one session, reset between runs by `e2e/reset_genesis.py`.
2. ~~The owner reviews the 40% genesis cap.~~ **Kept** (owner, 2026-11-15), over raising the
   storyline's rates 2.4 times (beyond `node-01`'s ~20 MB/s link) or a Move 1 that depends on a
   forecast. Recorded in [plan-deviations.md](docs/plan-deviations.md), "Capacity and the
   ledger". Pods keep their 1-core limit.
3. ~~A full storyline on the new calibration.~~ **Run 2026-11-15, and it does not take moves 2
   and 3.** Four runs on `6af904b0620f7b202454351c`, all with the database at 150%
   ([calibration.md](docs/calibration.md#full-storylines-with-the-cap-15-november-2026)):

   | Run | Moves | Fleet decided | Requests | Errors | First breach |
   | --- | --- | ---: | ---: | ---: | --- |
   | 727274 | fleet only | 1:48 | 441,930 | 0 | read p95 at 3:00 (597 ms) |
   | 1003 | fleet only; `db-read` armed 14:31, no move fitted | 2:06 | 415,553 | 0 | read p95 at 3:00 (340 ms) |
   | 727274, observe (control) | none; would have taken the fleet at 1:51 | — | 423,492 | 394,432 | both SLOs at 3:00 |
   | 727274 again | fleet only; `db-read` armed 12:03, no move fitted | 1:53 | 441,428 | 0 | read p95 at 3:00 |

   What they show:
   - **The database is ~2.5 times cheaper than the twin** in the chapters that matter (fitted
     `-db-cost` 0.44 at the replica's decision, 0.33 at the cache's), so `db-read` arms late or
     never. Its cgroup CPU is ~1.1 times statement time, not the twin's 1.7. The twin's
     storyline only holds within about ±10% of its database model.
   - **Costs vary between sessions and runs.** The database by ~2.5 times between sessions
     (the first matched the twin), the genesis API by ~1.7–1.9 times (this session against
     the gate's), and the same seed's database by a third between two runs on one session.
   - **Move 1 now comes at 1:48–2:06**, earlier than the twin and the gate, and the legacy
     process queues while traffic doubles during the 80 s shift: read p95 breached 250 ms in
     the minute to 3:00 in every acting run, with no errors.
   - **`node-01` saturates** (statement time 2× the database's CPU from 12:00 in seed 1003): its
     two vCPUs hold a 150% PostgreSQL plus an uncapped nginx at ~740 req/s.
   - **When `db-read` armed after the reads began repeating, nothing fitted:** the replica's
     shape wants `repeat_ratio < 0.4` (it was 0.72), and the cache needed 0.25 cores where the
     ledger had 0.2.
   - **Fixed:** the image's PostgreSQL cap overrode `set-property` after any reload, which
     broke the scale-up move's `pg-cap` and the 60% cap tried here. Now `10-caps.conf`.

   **Decided (owner, 2026-11-15): re-fit the twin's database costs to the playground, then
   pick the cap with the twin and widen the margins.** Done the same day, from this session's
   data, with no new session
   ([calibration.md](docs/calibration.md#the-database-re-fitted-to-the-playground-15-november-2026)):
   - **Re-fit per statement.** The twin's traffic already matched (listing sorts, calls per
     second); the statements did not, and not by one factor: rating sorts, price sorts, and
     detail views cost about half the local stack's, newest, facets, and search about the
     same. cgroup CPU is 0.06 cores at rest plus 1.1 × statement time. The re-fitted twin
     reproduces the playground at 150%: fleet only, and in seed 1003 `db-read` armed at 14:35
     (playground 14:31) with the same no-move reason.
   - **The cap: one db.medium is `CPUQuota=65%`** for the primary and the replica (a deviation,
     like the API's 40%). The ledger and contracts keep 1.5 cores, so `node-01` stays full
     after Move 1 and "scale up first" loses as in the plan; the scale-up move raises to 87%.
     `internal/twin/rootfs_test.go` keeps the images and `twin.PlaygroundDBCores` in step.
   - **The flash sale peaks at 800 req/s** (plan: 700; a deviation): at 700 the cache came late
     or never once the replica doubled read capacity.
   - **The fleet's database shape is now `db.cpu_ratio < 0.7`** (was 0.5): chapter 1's
     database ran 1.3–1.5 times the re-fit on the playground, enough to fail 0.5 by 3:00.
   - **Twin:** 100 of 100 seeds take fleet → replica → cache (fleet 2:15–2:55, replica
     5:50–7:10, cache 10:45–13:05), worst 5-minute availability 99.74%; 58 pass the twin's
     boundary rules (33 caches decided at repeat 0.6–0.7, harmless in this catalog).
   - **Margin: about ±10% of the re-fitted cost, not ±30%.** 30 of 30 seeds at 0.9 and 1.0, 19
     at 1.1, 17 at 0.8, 6 at 1.2. The session's first run sat at ~0.75–0.95 of the re-fit in
     chapters 2–3, its later runs higher: rows from earlier runs (likely reviews piling onto
     the sale category every seed shares) are not reset. Unexplained: chapter 1 at 1.3–1.5
     times the re-fit, and the first session's database at ~2.3 times this one's.

   Still open: Move 1's latency breach (an earlier `api-compute`, a faster shift, or a gentler
   first chapter).
4. ~~Test the re-fit on a fresh private session.~~ **Done 2026-11-15 (evening)** on
   `6afa3744d86d8571fbca0ce2`, seed 1003, 15 sessions through the platform besides the start
   ([calibration.md](docs/calibration.md#the-re-fit-on-a-fresh-session-15-november-2026-evening)):

   | Run | Moves (decided; twin) | Requests | Errors | Below target |
   | --- | --- | ---: | ---: | --- |
   | acting | fleet 2:57 (2:45), replica 6:52 (6:45), cache 11:57 (11:55), all verified | 440,276 | 27, all search timeouts | search's 5-minute availability 95.6–99.0% from 12:00; read p95 362 ms at 13:00 |
   | observe (control) | none; would have taken the fleet at 3:08 | 406,686 | 353,449 | both SLOs at 5:00, then a complete collapse |

   - **The re-fit holds before the cache:** the database ran at about 0.8–0.9 of the twin,
     with no chapter-1 excess this time (0.31 cores over 2:00–5:00 against the twin's 0.35),
     and each move came within 12 s of the twin's time.
   - **After the cache the replica ran at 1.4–1.6 times the twin**, at 0.7–0.9 of its quota,
     and search (a 46–66 ms statement, 1.8 req/s) timed out at the crowd's 2 s. MVP criterion
     2 (99.5% per journey in every 5-minute window) is not met.
   - **The twin's read routing was wrong, now fixed:** it sent every token-carrying read to the
     primary, but the shop sends them through the read pool and back to the primary only when
     the replica hasn't replayed their write (13% of them). Those reads skip the cache, so after
     it they are 47% of the listings that reach the database. With the fix, the playground's
     replica runs 1.2–1.4 times the twin's; 100 of 100 seeds keep the same path and times, but
     MVP-valid seeds at +10% cost fell from 19 to 11 of 30 (at nominal, 29 of 30).
   - **The control run collapsed because of nginx, not the API:** `worker_connections 768`
     (Ubuntu's default) let slow client connections fill every slot, so from 13:00 the API
     idled at 0.005 cores while every request failed.
   - Statement time is not CPU under a 65% cap (2× the unit's CPU in chapter 1): the windows
     measure latency. Calls per second matched the twin, except search (0.9 a second against 0.4).
5. ~~Measure the plateau after the cache on a fresh private session, and check nginx's new
   limit with a control run.~~ **Done 2026-11-17** on `6afc375b4bb0c11ddef73787`, seed 1003, 15
   sessions through the platform besides the start
   ([calibration.md](docs/calibration.md#the-plateau-measured-uncapped-17-november-2026)):

   | Run | Moves (decided; twin) | Requests | Errors | Below target |
   | --- | --- | ---: | ---: | --- |
   | acting, replica uncapped from 15:00 | fleet 2:48 (2:45), replica 6:48 (6:45), cache 11:48 (11:55), all verified | 439,939 | 24, all timeouts | search's 1-minute availability 86.5% at 12:00 and 13:00, 98.9% at 15:00; 100% after the lift |
   | observe (control) | none; would have taken the fleet at 2:58 | 410,174 | 370,180 | read p95 at 4:00, availability at 5:00, 0% from 6:00 |

   - **The replica's quota, not its CPU, fails search.** Uncapped, the replica used 0.54
     cores on the plateau (its quota is 0.65); capped, its statements took six times its CPU.
     The primary, at 62% of its quota, ran rating sorts at 2.7 times the uncapped replica's time.
   - **The period is not the fix.** `e2e/quota_bench.py` on the idle `node-01`: at 80% of the
     quota, a search averages 0.7–0.8 s and its p99 is 2.0 s with a 10 ms period, 2.6 s with
     100 ms, 0.2 s uncapped. A quota makes one 0.65-core server that every statement shares;
     a 100 ms period halves short statements' medians but not their tails.
   - **The twin was not re-fitted.** The database matched the twin before the cache (1.0–1.1)
     and ran about 1.3 times it on the plateau, spread across listing misses (1.37 times the
     cost each), product pages (1.4 times as many reach the database, at 1.33 times the cost),
     and search (1.2 times the calls at 1.33 times the cost). Raising costs would undo the
     match before the cache. Two causes are structural and not yet modelled: rating sorts get
     dearer as the flash sale adds reviews (the sale category's reviews doubled in one run),
     and product pages bypass the cache more than the twin's single token share allows.
   - **nginx's new limit holds;** the control run now collapses at the API's accept queue
     (see above).

   What was prepared for it (images built 2026-11-17 10:39):
   - **nginx:** the node image sets `worker_connections 4096` (Ubuntu's default is 768) and
     `worker_rlimit_nofile 16384`, since a proxied request holds two descriptors and the unit's
     soft limit is 1,024. The build fails if the edit to Ubuntu's `nginx.conf` doesn't apply.
   - **The plateau:** `e2e/storyline_run.py --lift-replica-at 15:00` removes the replica's CPU
     quota at 15:00 (`systemctl set-property --runtime gp-pg-replica.service CPUQuota=` on
     `node-04`). The session to `node-04` opens 45 s early and waits on that node's clock, which
     chrony keeps with `ops-01`'s, where the run's start is stamped. `ops_runner.py` now splits
     the plateau into 12:00–15:00 and 15:00–17:00 windows and reports each unit's throttled share
     of a window. `reset_genesis.py` removes a lift and fails if the replica's quota is still
     lifted. After the lift the agent reports the replica's limit as the machine's CPUs, so the
     architect's database ratios fall: its decisions after 15:00 don't count. Tested against
     fakes only.

   The plan's steps 1–4 ran as written. Step 5, re-fitting the twin's statement costs from the
   15:00–17:00 window, was not done, for the reasons above. The raw output is in `e2e/.logs/`
   (git-ignored): `2026-11-17-storyline-1003-lift.log`, `2026-11-17-storyline-1003-observe.log`,
   and `2026-11-17-quota-bench.log`.
6. ~~Choose how the plateau keeps search inside its SLO.~~ **Resolved by indexing search at
   genesis (2026-11-17).** The alternatives below record the earlier decision and measurements.
   The twin now
   reports rolling five-minute availability by journey and estimates the replica's search
   timeout risk below its CPU quota, using the 17 November live run and the quota benchmark.
   Seed 1003's predicted worst search availability is 95.24% (the earlier live run was 95.6%).
   Its one-minute search availability falls to 88.1% at 12:00, near the latest run's 86.5%
   there; the live run also had 86.5% at 13:00, when the twin has already begun to recover.
   At the **measured** post-cache demand of 0.54 cores, the model says a
   0.65-core replica is risky and an uncapped replica is not, as the live lift showed. The
   ordinary twin still under-counts database CPU after the cache by about 30%, so its normal
   timeline stops predicting search failures too early; the fixed-load check uses the measured
   0.54 cores. This is a one-run empirical estimate, not independent validation.

   What the
   measurement changed: search fails because the database runs at 0.7–0.8 of a fractional
   quota, which every statement shares, not because the twin under-counts CPU. And the
   storyline arms the cache only when the database reaches 0.7 of its quota, so search also
   suffers around the cache's decision (12:00–13:00), and the plateau keeps rising 44% after
   it. The options:
   - ~~Model it in the twin first (no session).~~ Done. The estimate only applies to the sale's
     replica: the earlier primary had a different query mix and no observed search failures.
     `gp-twin -replica-cap` compares replica quotas, and `-search-cost` compares hypothetical
     search-statement reductions. The architect still sees only its existing telemetry.
   - Give the replica more headroom than one db.medium (`node-04` is otherwise idle): at 0.87,
     the scale-up level, the plateau's 0.54 cores is 62% busy, where the benchmark's search
     stays under 1.4 s at p99. This is a deviation from the plan's capacity units, and with
     less pressure on the replica the cache may arm later.
   - Use whole vCPUs as the database's capacity unit instead of a fractional quota, so a
     statement runs at full speed and waits only behind others. One playground vCPU is 1.5
     times today's 0.65, so the storyline's database load would have to grow to match (for
     example, more reviews per product in the base catalog, which make rating sorts dearer), and
     the twin would have to price rating sorts by reviews. That means a re-calibration.
   - Keep search off a busy database or give it its own budget (an index, or the stage 5 search
     moves brought forward).
   - Lowering the flash-sale peak doesn't help: at 700 or 750 req/s, `db-read` arms before reads
     repeat enough for the cache (twin, 30 seeds). A 100 ms quota period doesn't help either (the
     benchmark).
   - Separately, and not blocking: the control run's goodput collapse (the API's accept queue)
     could be modelled in the twin, or the genesis shop could shed load (a concurrency limit);
     either is a choice about what stage 2 should show.

   Twin what-ifs at the 40% genesis cap (`-api-cost 1.075`), 20 seeds each:

   | What-if | Paths with all three moves | Worst search journey, median of seeds | Seeds meeting all validation rules |
   | --- | ---: | ---: | ---: |
   | Current 0.65-core replica | 20/20 | 94.98% | 0/20 |
   | Replica at 0.87 cores from creation | 20/20 | 99.09% | 0/20; five caches migrate after 15:00 |
   | Replica at 1.00 core from creation | 13/20 | 100% | 5/20; seven caches do not complete during the run |
   | Search statements five times cheaper (hypothetical) | 20/20 | 99.99% | 14/20; six fail shape margins |

   A larger replica quota delays the database pressure that arms the cache, so 0.87 alone was
   insufficient. A clean local quota benchmark established that `pg_trgm` indexes remove the
   no-match scan's CPU. The first indexed VM run completed all three moves with zero errors,
   but one read-latency minute still breached during the cache rollout. The later unattended
   run with a shorter post-replica evidence window and 5 s cache holds completed all moves by
   13:30 and passed 17/17 latency minutes, 100% worst journey availability, and both correctness
   counters at zero. See [calibration.md](docs/calibration.md#search-indexes-and-the-first-indexed-vm-run-17-november-2026).
7. **Stage 4 repeatability and presentation — met 2026-11-20.** A checksummed clean database snapshot now makes
   `reset_genesis.py` restore the exact seeded rows, CPU caps, and architecture. A 2026-11-18
   same-seed gate passed ten of ten runs, but later automatic-start trials exposed narrower
   fleet and cache timing margins and late database throttling. The architect now forecasts
   fleet pressure earlier, uses a 15 s post-replica cooldown, and lifts both database caps to
   their reserved 1.5 cores when it chooses the cache. The first rebuilt-image automatic-start
   trial passed all MVP SLOs with zero errors in 442,212 requests. The narrator's end report,
   live architecture map, Grafana Canvas, component timeline, and move-error table were checked
   on the private VMs; the crowd reports `Finished` and keeps that state after a restart.
   A ten-run gate with the 0.4 cache shape passed eight runs and missed one 12:00 latency
   window at 271 ms on run 9. The shape is now 0.3 after a replica exists and the replica cap
   lifts first. A revised-image automatic-start trial passed with zero errors in 442,220 requests,
   17/17 latency windows, and 25 ms read p95 at 12:00; the report and dashboard passed inspection.
   A same-seed ten-run gate passed on the 0.3 cache shape, with 4,422,239 requests, 10/10
   complete paths, 10/10 SLO passes, 17/17 latency windows per run, and 15/15/15 s decision
   spreads. A separate seed-727274 run exposed two search timeouts and 340 ms read p95 before
   the cache raised the replica's CPU quota. The replica move now raises both databases to their
   reserved 1.5 cores before routing reads, and the cache uses a sustained `read-reuse` pressure.
   A live seed-727274 run on that progression passed with zero errors in 466,829 requests,
   17/17 compliant latency windows, and 12 ms read p95 at 11:00. On the final rebuilt ops
   image, a fresh seed-1003 automatic-start run passed, followed by a ten-run automatic-start
   gate: 10/10 matching paths and full MVP SLO passes, zero errors in 4,422,391 requests,
   170/170 compliant latency minutes, 100% worst full five-minute journey availability, zero
   correctness violations and move-window errors, and 5/5/5-second decision spreads. The
   final run's report, Grafana views, move records, and both 1.5-core quotas passed inspection.
   A separate live trial of seed 727274 on the final image passed: 466,837 requests, zero
   errors, 17/17 compliant latency minutes, 100% worst full five-minute availability, all
   moves verified by 12:33, and the finished report and quotas inspected. On the same image,
   a 90 s delayed control missed the latency target (16/17 compliant minutes), while an
   architect-disabled control missed both availability and latency (0% worst full five-minute
   availability, 3/17 compliant minutes). The private session was destroyed.

Working notes for whoever picks this up:

- Port 8080 on the development machine belongs to another project, so the local shop is on 18080.
- On a playground node, never run `nginx -t -c <another config>` as root: a config with no
  `user` directive makes nginx chown its temp directories (`/var/lib/nginx/*`) to `nobody`, and
  the shop then fails large POSTs with 500s. `chown www-data` on them repairs it.
- To change one binary or file on a running session, `labctl cp --machine <vm> --user root
  <file> <session>:<path>` is far quicker than `private_session.py update` (which re-uploads
  the whole overlay, replaces a node's database with the image's, and reboots).
- `gp-twin -api-cost <f>` multiplies every API cost: `-api-cost 0.43` is a playground vCPU
  with a full core for the API, which is what the first gate on the rebuilt images ran.
  `-db-cost <f>` does the same for every database statement, whose costs are now fitted to a
  playground core; `-db-cap <cores>` sets a what-if quota for each database instance (the
  images' is 0.65), leaving the ledger in db.medium units.
- To cap a unit by hand on a session, mind systemd's drop-in order: files apply by filename
  across directories, so `set-property` (which writes `50-*.conf`) loses to any image drop-in
  that sorts after it, as soon as something reloads systemd. Check `systemctl show -p
  DropInPaths,CPUQuotaPerSecUSec <unit>` and the agent's `gp_unit_cpu_limit_cores`.
- The shell here wraps `curl` and `grep` through `rtk`, which reformats their output. When piping
  JSON or exact matches into another program, use `rtk proxy curl ...` or `rtk proxy grep ...`.
- `make images` rebuilds the lab image from `cmd/` and `internal/`; a new embedded directory
  (like `contracts/` or `deploy/k8s/`) must also be copied in `build/lab.Dockerfile`, or the
  build fails.

### Build order (the plan's "Build order and decision gates")

| # | Stage | Status | Gate | Evidence and work remaining |
| --- | --- | --- | --- | --- |
| 1 | Spike and calibration | Partial | Measured capacity units allow the MVP storyline at 6 vCPU or less | Local calibration: one Python process passes 75% CPU at ~200 req/s; ~4 ms API CPU and ~3 ms database CPU per request ([calibration.md](docs/calibration.md)). The twin, fed these costs, peaks at ~2.5 API cores and ~2.4 database cores before the cache. Playground (2026-11-13): `node-01`'s link carries ~20 MB/s, which an uncapped base backup fills; API pods are ready ~5 s after apply. See [calibration.md](docs/calibration.md#first-playground-runs-13-november-2026). Playground (2026-11-14): the API costs ~1.8 ms of CPU per request on a playground vCPU (4.3 ms locally), so the genesis process is capped at 40% of a core to stay one capacity unit ([calibration.md](docs/calibration.md#rebuilt-images-on-a-fresh-session-14-november-2026)). Playground (2026-11-15): the database costs ~0.33–0.44 of the twin's model, and costs vary ~2× between sessions ([calibration.md](docs/calibration.md#full-storylines-with-the-cap-15-november-2026)); the twin's database is now re-fitted per statement to the playground, and one db.medium is a 65% quota there ([calibration.md](docs/calibration.md#the-database-re-fitted-to-the-playground-15-november-2026)). Tested that night on a fresh session: about 0.8–0.9 of the twin before the cache, the replica 1.2–1.4 times the twin after it, once the twin's token routing was fixed ([calibration.md](docs/calibration.md#the-re-fit-on-a-fresh-session-15-november-2026-evening)). Playground (2026-11-17): uncapped, the replica needs 0.54 cores on the plateau, under its 0.65 quota; a quota makes the database one shared 0.65-core server, so long statements queue at 0.7–0.8 of it whatever the period ([calibration.md](docs/calibration.md#the-plateau-measured-uncapped-17-november-2026)). Search indexes and the earlier cache transition now let one fully unattended run meet the MVP SLOs on VMs. **Remaining:** the rest of the spike checklist on VMs (capacity ramp on a real `node-01`, `ops-01` holding the crowd at 1,200 req/s). |
| 2 | Pain without a cure | Done (gate met) | The storyline breaches the SLOs at about the predicted times with no architect | **Met on the local stack** with the current storyline (seed 42): the API process passed 75% CPU at ~4:00, read p95 passed 250 ms at 5:00, and 1-minute availability fell under 99.5% at 6:00 and stayed near 1–2% to the end. The observing architect armed `api-compute` at 3:15 and recorded that it would choose the fleet. See [calibration.md](docs/calibration.md#no-architect-run-the-stage-2-gate). **Met on a playground** 2026-11-15 (seed 727274, `-observe`): both SLOs breached at 3:00 and availability fell to ~0% from 4:00; the observing architect would have taken the fleet at 1:51. Again that night on the 65% images (seed 1003): both SLOs at 5:00, then a complete collapse, which nginx's `worker_connections 768` causes. The node image now sets 4096; on 2026-11-17 (seed 1003) both SLOs breached at 4:00–5:00 with nginx's slots never short, and availability still fell to 0% from 6:00, now at the API's accept queue (goodput collapse; the twin degrades in proportion). |
| 3 | Architect core, twin, and Move 1 | Done (gate met) | Zero-error fleet migration in 10 of 10 runs | Done: architect core (signals with lint, pressures with sustain, hysteresis, and forecasts, contracts, ledger, diagnosis, persisted step machine with guards, rollback, and verification); twin (100 of 100 seeds took fleet → replica → cache before journey-level availability was added; 58 passed the earlier aggregate validation); `gp-architect` command with the runtime config server, event log, Grafana annotations, metrics, and API, running in observe mode against the local stack; playground actuator (Kubernetes server-side apply, node taints, readiness probes, nginx weighted upstreams, SSH dispatcher, replication SQL); k3s manifests; node scripts and units; VM images (`make images` builds `growingpains-node-rootfs` 3.1 GB and `growingpains-ops-rootfs` 4.0 GB); playground manifest renderer; private-session, reset, gate, and full-storyline tools. Booted on a private session 2026-11-13; first boot's faults are fixed (see "Resume here"). **Gate met 2026-11-14** on a hand-patched session (10 of 10 error-free, 461,482 requests), plus two rollbacks before the fixes that restored genesis with no errors, and **again 2026-11-15 on freshly built images**: 10 of 10 completed and verified, 0 errors in 501,301 requests, with the 40% genesis cap. |
| 4 | Moves 2 and 3, plus narration (MVP) | Done (gate met 2026-11-20) | The MVP success criteria | Final private-VM automatic-start seed-1003 gate: 10/10 fleet → replica → cache paths, all moves verified by 15 minutes, 10/10 full SLO passes, 4,422,391 requests, zero errors, 170/170 compliant latency minutes, 100% worst full five-minute availability, zero correctness and move-window errors, and 5/5/5-second decision spreads. Cross-seed 727274 passed with 466,837 requests, zero errors, and 17/17 compliant minutes. Final-image delayed and architect-disabled controls failed SLOs as intended (16/17 and 3/17 compliant minutes; disabled control 0% worst availability). The finished report, Grafana Canvas and timeline, move-error table, and both 1.5-core database quotas passed inspection. Narration signal values are checked in unit tests. Private session destroyed. |
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
| Dashboard | `tools/render_dashboard.py` | Done for the MVP | Run bar, SLO tiles, traffic, latency, errors, pressure, capacity, data tier, narration list. The Canvas map, component timeline, and three-row move table loaded on the private Grafana; the move table's error counts matched the report. |
| Architect | `internal/architect`, `internal/signals`, `contracts/`, `cmd/gp-architect` | Done, run on VMs | Observe mode runs in the local stack. The playground actuator (`internal/actuate`, `internal/kube`, `deploy/k8s`) has run all three MVP moves and two rollbacks on a private playground. |
| Twin | `internal/twin`, `cmd/gp-twin` | Partial for the MVP | Seed sweeps, validation, load breakdown with listing sorts (`-explain`), cost and cap what-ifs, and rolling availability by journey. Database costs fitted to a playground core (2026-11-15); token reads routed through the read pool as the shop does, and `-json` samples carry the primary's ratio, cache hits by route, and one-minute search availability. **Missing:** the growth in database CPU after the cache from reviews and detail-cache bypass; independent validation of the search timeout estimate. |
| Narrator | `internal/narrator`, `cmd/gp-narrator` | Done for the MVP | Live SSE page with an architecture map, traffic and decision notes, records, and an automatic end report. The rebuilt VM run served matching predicted and actual paths, settled request totals, SLO results, and all three move records; the report regenerated correctly after an ops reboot. |
| Run report, growth tree | `internal/narrator/report.go` | Report built; growth tree later | The report reveals the storyline after the run and compares paths and SLOs. A shareable growth-tree graphic belongs to a later presentation stage. |
| VM images, playground manifest, private sessions | `rootfs/`, `playground/`, `e2e/`, `tools/generate_credentials.py` | Booted from built images with no hand fixes (2026-11-14, 2026-11-15, and the 22:41 build with the 65% database caps that night). Rebuilt 2026-11-17 with nginx's 4096 connections and booted cleanly that day; match the repository | One node image for all four nodes (role by hostname), one `ops-01` image. Secrets go to `rootfs/generated/` (ignored by git). |

### Work log

Newest first. Each entry says what changed and what to pick up next.

- **2026-11-20 (MVP gate met; private session closed)** — On the final rebuilt ops image,
  the seed-1003 ten-run automatic-start gate passed: 10/10 matching fleet → replica → cache
  paths, 10/10 full SLO passes, 4,422,391 requests with zero errors, 170/170 compliant
  latency minutes, 100% worst full five-minute availability, zero correctness and
  move-window errors, and 5/5/5-second decision spreads. A separate seed-727274 live run
  passed with 466,837 requests, zero errors, 17/17 latency minutes, and all moves verified
  by 12:33. The final report, Grafana Canvas and move records, and both database quotas
  passed inspection on both completed runs. On the same image, a 90 s delayed control
  failed latency (16/17 minutes; 982 ms p95 at 5:00), while the architect-disabled control
  failed availability and latency (395,169 errors in 425,105 requests, 0% worst full
  five-minute availability, 3/17 latency minutes). The private session
  `6b003880139ebf9a02c2cae3` was destroyed and no Growing Pains session remains live.
  **Next:** stage 5 branching and the remaining spike measurements are outside the MVP.
  No commit.

- **2026-11-20 (replica timing margin, earlier trial)** — The first automatic repeat on
  `6b003880139ebf9a02c2cae3` chose the replica at 8:10, compared with 6:20 in the
  standalone run on the same image. A cooler primary delayed the CPU forecast and left less
  time for the cache, so the gate was stopped. The `db-read` pressure now also arms on 45 s
  of sustained traffic above 280 req/s when reads are the majority and no replica exists.
  `make test` passes. The 100-seed twin sweep took fleet → replica → cache in every seed,
  with replica decisions at 5:15–6:15, 100% worst modeled five-minute availability, and
  100/100 valid seeds under the revised read-share rule. The rebuilt ops image is being
  applied to the private session. Its fresh automatic-start seed-1003 trial passed with
  442,243 requests, zero errors, 17/17 latency windows, 100% worst full five-minute
  availability, and no correctness violations. Fleet, replica, and cache decisions were at
  2:05, 6:25, and 11:15; all verified by 12:55. The finished report, Grafana move records,
  and both 1.5-core quotas passed. The final ten-run automatic-start gate later passed;
  see the newer entry above.
  No commit.

- **2026-11-19 (cross-seed reliability and revised progression, in progress)** — The 0.3
  cache-shape image passed an unattended automatic-start run and a fresh ten-run seed-1003 VM
  gate on `6aff2e7078d2128757a1c9ca`: 10/10 fleet → replica → cache paths, 10/10 full MVP
  SLO passes, 4,422,239 requests, two connection errors outside every move window, zero
  change-window errors, 17/17 compliant latency minutes per run, 100% worst full five-minute
  journey availability, and decision spreads of 15/15/15 seconds. The final report, Canvas,
  move table, and both database quotas passed inspection. A separate seed-727274 VM run on
  the same image exposed two search timeouts and a 340 ms read p95 at 11:00: search's worst
  five-minute availability was 99.397%, and only 16/17 latency minutes complied. The replica
  was throttled for 86% of 10:00–11:00; the cache's quota lift at 10:58 came too late. The
  progression now unlocks both databases' reserved 1.5 cores during the replica move, before
  read routing, and chooses the cache from sustained repeat-read traffic rather than waiting
  for another quota bottleneck. `make test` passes and a 100-seed twin sweep retains the
  fleet → replica → cache path with 100% modeled worst journey availability; 61 seeds meet the
  twin's remaining shape-margin rule. A live seed-727274 trial on the revised progression
  passed: 466,829 requests, zero errors, 17/17 compliant latency windows, 100% worst full
  five-minute availability, and all three moves verified by 12:33. Read p95 at 11:00 fell
  from 340 to 12 ms. The report, dashboard, and both 1.5-core quotas passed. The final ops
  image clears `read-reuse` pressure after cache activation; a unit test checks this. The
  private-session helper now waits for all five Ready nodes after reboot. A fresh private
  session `6aff8273139ebf9a02a25e0d` booted the final images and passed the automatic-start
  seed-1003 trial: 442,191 requests, one isolated error, 17/17 latency windows, 100% worst
  full five-minute availability, zero correctness violations, and all moves verified by 12:50.
  The report, Grafana, and both quotas passed inspection. The first four same-seed repeats
  passed, but run 5's fleet decision came at 2:06 versus 0:27–0:42 in the first four; its
  4:00 read p95 was still 51 ms and availability 100%. The one-minute reset calm period could
  leave prior telemetry in the architect's two-minute forecast. A three-minute manual hold
  collided with the crowd's own automatic Start, so the gate now follows that Start on every
  repeat. Its first run exposed a true fleet timing miss: a decision around 2:15 left the
  legacy API serving too much of the 3:00 minute, and read p95 reached 1,094 ms. The
  architect's fleet arm now includes sustained live traffic above 30 req/s at 25% API CPU,
  with a 20 req/s floor to prevent idle telemetry from choosing a move. `make test` passes;
  a 100-seed twin sweep retained all three moves, with fleet decisions from 1:35 to 2:00
  and 100% modeled worst five-minute availability. The rebuilt ops image is being applied
  for a fresh automatic-start trial and final gate. The new session
  `6b003880139ebf9a02c2cae3` passed that trial with 442,242 requests, one isolated error,
  17/17 compliant latency windows, 100% worst full five-minute availability, and no correctness
  violations. The 3:00 read p95 fell to 22 ms, and all three moves verified. The finished
  report, Canvas, move records, and both 1.5-core quotas passed inspection. A ten-run
  automatic-start gate is running. No commit.

- **2026-11-19 (fully unattended timing and plateau checks)** — The first automatic-start run on
  `6afe257dfd7ce1aae088a0bd` finished fleet → replica → cache with 17/17 latency minutes,
  100% worst five-minute availability, zero correctness violations, and one error in 442,100
  settled requests. The end report and Grafana Canvas, timeline, and three-row move table were
  checked. A second autonomous run missed the 12:00 latency window (449 ms) because the 60 s
  cooldown after replica verification delayed cache eligibility; a 15 s contract-specific
  cooldown brought the cache decision to 11:20 and the quota lift to 11:30 on the next run.
  That run instead missed 4:00 (780 ms) while the legacy API had 873 ms handler p95, so the API
  forecast now arms at 60% and shifted the fleet by 3:45 on a fresh session. The fresh run
  cleared 4:00 and 12:00 but missed 17:00 (284 ms read p95, seven late timeouts in 442,031
  requests) while both databases had spare physical CPU but were quota-throttled. The cache
  move now lifts both database caps to their full 1.5-core reservations; rebuilt images are
  installed on `6afed80713e12b000c9f62a6`. Its automatic-start trial passed with 442,212
  requests, zero errors, 17/17 latency minutes, 100% worst five-minute availability, and no
  correctness violations. The expanded report, Grafana move rows, and both 1.5-core quotas
  passed inspection. A ten-run same-seed gate passed runs 1–8, then stopped at run 9 when the
  12:00 read p95 reached 271 ms. All three moves had zero change-window errors. The cache's
  rising-repeat shape is now 0.3 after a replica exists, and its replica cap lifts before the
  primary cap. An automatic-start trial on that image chose cache at 11:10, lifted the replica
  cap at 11:20, and passed with 442,220 requests, zero errors, 17/17 compliant latency windows,
  100% worst full in-run five-minute availability, and all moves verified by 13:05. The 12:00
  read p95 was 25 ms; the end report, Grafana move records, and both 1.5-core quotas passed
  inspection. The private session was destroyed, and `6aff2e7078d2128757a1c9ca` is running
  the final gate. The
  narrator's finished report now uses fixed historical totals and the private updater preserves
  Prometheus data across image updates. `make test` and the 100-seed twin sweep pass; the full
  VM gate and session cleanup remain. No commit.

- **2026-11-18 (ten-run gate passed; delayed recovery under test)** — The indexed, earlier-cache
  images passed all ten seed-1003 VM trials on `6afda0b54d0c76e1364e0c0c`: the same verified
  fleet → replica → cache path in 10/10, 4,400,592 requests and zero errors, 17/17 latency
  minutes compliant in every run, 100% worst full in-run five-minute availability, no
  read-your-writes violation or acknowledged write lost, and decision spreads 19/20/30 s.
  The gate session expired after finishing. A fresh session `6afe257dfd7ce1aae088a0bd`
  booted from rebuilt node and ops images. Its first 90 s delayed control breached availability
  at 4:00 (92%), then reached near-zero availability because the low-budget guard disallowed
  every architecture move. The three reversible MVP contracts now declare
  `budget_protective: true`; tests and a 100-seed twin sweep pass. The rebuilt ops image's
  corrected delayed control chose the fleet at 1.37% budget remaining and verified fleet and
  replica. As intended, the 90 s holds caused the first latency breach at 4:00 and availability
  breach at 5:00; the worst full five-minute availability was 50.87%, and only 6/17 latency
  minutes complied. The final unattended run is in progress; presentation check and session
  cleanup remain. No commit.

- **2026-11-18 (first fully gated trial on fresh images)** — A fresh private session
  `6afda0b54d0c76e1364e0c0c` booted from the rebuilt images and passed the checksummed
  genesis reset. With the earlier cache shape, seed 1003 chose fleet at 3:02, replica at 6:57,
  and cache at 11:42; all were verified by 13:32. The quota buffer took effect at 11:52.
  The judge counted 439,332 requests, zero errors, 100% worst full in-run five-minute
  availability, 17/17 compliant latency minutes, and no read-your-writes violation or lost
  write. The ten-run same-seed gate advanced automatically to run 2. The remaining nine runs,
  delayed control, unattended run, end-report and dashboard check, and session cleanup remain.
  No commit.

- **2026-11-18 (earlier cache, in progress)** — The first trial with the primary's 60% read
  trigger chose fleet at 2:47, replica at 6:42, and cache at 11:52; all completed and verified
  by 13:42 with zero change-window errors. It had one search timeout in 439,920 requests and
  99.66% worst full in-run five-minute availability, but read p95 reached 370 ms in the minute
  ending 12:00, just before the cache move raised the replica quota at 12:02. The old private
  session expired and is no longer listed. Locally, the cache shape accepts a rising 0.4 repeat
  ratio after a replica exists (still 0.6 without one), and post-replica read pressure needs 5 s
  of evidence. Seed 1003's twin now chooses cache at 11:25 and completes it at 12:55; unit and
  shop tests pass. The rebuilt images and fresh private VM trial remain next. No commit.

- **2026-11-17 (rebuilt-image gate, in progress)** — Installed the indexed images with the
  cache move's replica CPU buffer on all five private VMs. The checksummed reset passed, and
  the Canvas rendered correctly in a browser at genesis (grey dormant components) and during
  the fleet migration (amber new pods) after limiting its Prometheus query to fresh samples.
  Its first full-run trial was stopped at 10:00: the replica had decided at 8:43, and read p95
  reached 706 ms while its base backup ran. The primary's read-pressure threshold is now 60%
  locally; the seed-1003 twin moves the replica at 6:20 and still takes fleet → replica → cache.
  Rebuild and install the ops image, then restart the ten-run gate. The private session
  `6afc674163321e6eb4e7e97a` subsequently expired; the replacement is recorded above.
  No commit.

- **2026-11-17 (indexed MVP, in progress)** — Brought `pg_trgm` GIN indexes on product title
  and description into the genesis database after a local quota benchmark found the no-match
  search scan was the cause of the replica's timeouts. The first indexed VM run took all three
  moves with zero errors in 439,990 requests; one read-latency minute still breached while the
  cache rolled out. Shortened `db-read`'s evidence window only after a replica exists and made
  cache rollout holds 5 s. The updated images and checksummed database snapshot booted and reset
  cleanly. With no Start press, seed 1003 started itself and completed fleet, replica, and cache
  at 2:45, 6:40, and 11:50; the judge found 447,811 requests, zero errors, 100% worst rolling
  availability, 17/17 latency minutes within target, and zero correctness violations. The
  narrator served its end report with matching predicted and observed paths. A ten-run
  same-seed gate began on `6afc674163321e6eb4e7e97a`; its first run took the expected path
  with zero errors in every move window, but three search timeouts outside the moves left one
  latency minute over target and one full in-run five-minute search window at 99.41%, below
  99.5%. It was stopped after that failure. The run report and gate now exclude reset/genesis
  data from the five-minute metric, and the cache move lifts the replica to one core before
  filling the cache. The session lifetime is 12 h and it must be destroyed when done. Also
  expanded the report's SLO detail, filtered one-scrape
  zero-rate narration artifacts, and generated a Grafana Canvas map, component timeline, and
  move table locally; the rebuilt images need a live check. A finished crowd run now stops new
  traffic and preserves its completed state across restart. No commit was made.

- **2026-11-17 (later)** — Added per-journey rolling availability to the twin and an empirical
  search timeout curve for the sale's replica under a fractional CPU quota. Seed 1003's twin
  now predicts a search breach near the cache move; its worst five-minute search availability
  is 95.24%, close to the earlier live run's 95.6%. At the measured 0.54-core post-cache load,
  the curve predicts risk under the 0.65 cap and none uncapped. Swept 20 seeds: 0.87 cores from
  replica creation delays the cache and does not save search; a hypothetical fivefold cheaper
  search statement preserves all three moves and meets availability in every seed, though six
  miss shape-margin validation. `make test` passes. No session used. **Next:** the owner decides
  the production approach in step 6; the twin still needs the observed post-cache CPU growth
  modelled from reviews and detail-cache bypass before it can predict plateau minutes directly.

- **2026-11-17 (afternoon)** — Resumed from the ledger: `make test` passes and the 20-seed twin
  sweep takes fleet → replica → cache on every seed. The owner started
  `6afc375b4bb0c11ddef73787` from the 10:39 images; first boot passed with no hand fixes, and
  nginx runs 4096 connections with 16,384 files. Seed 1003 acting, with the replica's quota
  lifted at 15:00, took all three moves at the twin's times (24 errors in 439,939 requests).
  Search failed while the replica was capped and stopped the moment it was lifted: uncapped,
  the replica needs 0.54 cores, under its 0.65 quota. A new benchmark (`e2e/quota_bench.py`)
  showed why: a quota makes one shared 0.65-core server, so at 80% busy a search's p99 is 2.0 s
  with the 10 ms period and 2.6 s with 100 ms, against 0.2 s uncapped. The control run had no
  nginx slot shortage but still fell to 0% from 6:00, at the API's accept queue. The plateau's
  database ran about 1.3 times the twin, spread across listing misses, product-page bypasses,
  and search; the twin's costs were left as they are (they matched before the cache), and
  weighting cache hits by cost made no difference (reverted). Recorded in calibration.md.
  Session destroyed; 15 sessions through the platform besides the start. **Next:** the owner
  decides step 6 (how the plateau keeps search inside its SLO); recommended: model per-journey
  availability under a quota in the twin first.

- **2026-11-17** — The owner chose both of step 4's recommendations: measure the plateau after
  the cache first, and raise nginx's `worker_connections`. The node image now sets 4096 (and
  `worker_rlimit_nofile 16384`), checked with `nginx -t` in the old image under a 1,024-file
  limit. `e2e/storyline_run.py --lift-replica-at 15:00` removes the replica's CPU quota at
  15:00 for a measurement run (one more session, timed on `node-04`'s clock), `ops_runner.py`
  splits the plateau into 12:00–15:00 and 15:00–17:00 and reports throttling by unit, and
  `reset_genesis.py` undoes a lift. Tested against fakes, plus `set-property CPUQuota=` on a
  local unit. `make test` passes; images rebuilt (10:39). No session used. **Next:** step 5,
  the measurement session, which the owner starts.

- **2026-11-15 (evening)** — Resumed from the ledger: `make test` passes and the 20-seed twin
  sweep (`-api-cost 1.075`) takes fleet → replica → cache on every seed. Rebuilt both images
  (22:41) and checked the 65% caps inside the node image. `e2e/ops_runner.py` now ends a
  storyline with statement windows (2:00–5:00, 5:00–8:00, 7:00–10:00, 12:00–17:00): database
  time and calls by statement, CPU by unit and by node, all from Prometheus on `ops-01`, so they
  cost no extra sessions (checked against a fake Prometheus). Auto mode blocked
  `private_session.py start`, so the owner started `6afa3744d86d8571fbca0ce2` with `!`. First
  boot passed with no hand fixes. Seed 1003 acting took fleet → replica → cache at 2:57, 6:52,
  and 11:57 (twin 2:45, 6:45, 11:55), all verified, 27 errors in 440,276 requests, all search
  timeouts after the cache, so search's 5-minute availability fell to 95.6% (criterion 2 not
  met). Chapter 1's excess was gone (0.8–0.9 of the twin). The control run breached at 5:00
  and collapsed completely: nginx's default `worker_connections 768` let slow clients hold every
  slot, so the API idled. Found and fixed the twin's read routing (token reads go through the
  pool, not all to the primary); with it the twin keeps 100 of 100 paths, but the +10% cost
  margin falls from 19 to 11 of 30. Tried flash-sale peaks of 700 and 750 in the twin: no wider
  margin. Session destroyed; 15 sessions through the platform besides the start. **Next:** the owner
  decides step 5 (the plateau after the cache, and nginx's connections).

- **2026-11-15 (afternoon)** — The owner chose to re-fit the twin's database costs to the
  playground. Re-fitted per statement from the morning session's `pg_stat_statements` windows
  and minute-by-minute cgroup CPU (rating and price sorts and detail views about half the local
  cost; newest, facets, and search about the same; 0.06 cores at rest plus 1.1 × statement
  time). At 150% the re-fitted twin reproduces the playground's fleet-only runs, including seed
  1003's no-move at 14:35. Swept caps and cost swings: one db.medium is now `CPUQuota=65%` on
  the playground (ledger still 1.5 cores, scale-up to 87%), the flash sale peaks at 800 req/s,
  and the API moves' database shape is 0.7. 100 of 100 seeds take all three moves; the margin
  is about ±10% of the re-fit, not ±30%. Added `gp-twin -db-cap`, a listing-sort breakdown in
  `-explain`, and a test that the images' quotas match the twin. Recorded in calibration.md and
  plan-deviations.md. No session used. **Next:** `make images`, then one acting run (seed 1003)
  and the control run on a fresh session, with a chapter-1 statement window; decide on a
  restoring reset or per-session caps from what it shows.

- **2026-11-15 (morning)** — The owner kept the 40% genesis cap. Audited the harness's
  connections, as Break Team once fixed: the gate opened a `labctl ssh` session for every 15 s
  poll (~32 a run, ~320 a gate). Now `e2e/ops_runner.py` watches each run from inside `ops-01`
  (started with `systemd-run`, followed over one session by `e2e/lab.py`, which counts
  sessions), the reset uses four sessions instead of six, and local queries keep one connection
  open. A run costs five sessions; tested against fakes and on the playground. Added
  `e2e/storyline_run.py` and `gp-twin -db-cost`. On a fresh private session
  (`6af904b0620f7b202454351c`, owner approved, now destroyed): two acting seeds and a repeat
  took the fleet only, with 0 errors, because the database costs ~0.4 of the twin's; the
  control run breached both SLOs at 3:00 (**stage 2's gate met on VMs**). Move 1 came at
  1:48–2:06 and read p95 breached during the handover in every acting run. Costs varied ~2×
  between sessions and by a third between runs. A 60% database cap could not be tried: the
  image's `caps.conf` overrode `set-property` after any reload, which also broke the scale-up
  move's `pg-cap`; renamed `10-caps.conf` (images need a rebuild). **Next:** the owner decides
  how to calibrate the database (see "Resume here", step 3), then `make images` and a session
  to test it.

- **2026-11-14/15 (night)** — Booted the rebuilt images on a fresh private session
  (`6af8e071620f7b20244a587f`, owner approved): first boot passed with no hand fixes, and chrony
  holds every node within 6 µs of `ops-01`. The first gate there found the genesis API had a
  full playground core, ~2.4 times what the storyline is calibrated for: four seeds moved late
  on the forecast alone and seed 1003 never armed. Capped the genesis unit at 40% (a deviation),
  added `gp-twin -api-cost`, and fixed `private_session.py update`, which had corrupted
  `node-01`'s database by unpacking the image's cluster over a live one. **Stage 3's gate met on
  built images:** 10 of 10 completed and verified, 0 errors in 501,301 requests. Session
  destroyed. **Next:** the owner reviews the cap; then a full storyline on the new calibration,
  and stage 4.

- **2026-11-13 (late night)** — First private playground session (`6af79014918a23974508d931`),
  with the owner's approval. First boot passed its checklist except for eight faults, all now
  fixed: the base-image pull, k3s's token file, root's kubeconfig, the legacy probe address, the
  agent's DSN, step holds after blocking actions, the uncapped base backup, and the fleet's
  queue-time verdict; and node clocks 10–20 ms apart (chrony on every VM now). Added
  `e2e/reset_genesis.py` and `e2e/move1_gate.py`. **Stage 3's gate met:** Move 1 error-free in
  10 of 10 runs. In seed 727274 all three moves completed and verified. **Next:** boot the
  rebuilt images on a fresh session (confirm chrony first), then stage 4's open items.

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
go run ./cmd/gp-twin -sweep 50 -api-cost 1.075 -db-cost 0.9   # what-ifs: a host's API and database costs
go run ./cmd/gp-twin -sweep 50 -api-cost 1.075 -db-cap 0.7     # what-if: each database instance's quota
go run ./cmd/gp-twin -sweep 20 -api-cost 1.075 -replica-cap 0.87 # what-if: only the replica's quota
go run ./cmd/gp-twin -sweep 20 -api-cost 1.075 -search-cost 0.2  # hypothetical fivefold cheaper search
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
