# Calibration notes

Measurements that set the shop's costs and the storyline's numbers. The plan's spike runs on
real playground VMs; these first numbers come from the **local Docker stack** (`dev/compose.yaml`),
with the same CPU caps but a shared 8-core laptop that was also running other containers. Treat
them as leads to confirm on the playground, not as the playground's numbers.

## Local stack, 12–13 November 2026

Genesis on one machine: nginx 1.28, one Python 3.15 API process capped at 1 core
(`--cpus 1.0`), PostgreSQL 18.7 capped at 1.5 cores and 1.5 GiB, the base catalog of 50,000
products. The crowd played the `calibrate:60:420:60` storyline (genesis mix, one minute per step),
and `tools/calibration_report.py` read each step's last 30 seconds.

### Ramp 2 (after the fixes below)

| Offered req/s | API CPU / cap | API CPU per request | DB CPU / cap | Queue p95 | Read p95 | Errors |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 59 | 0.34 | 6.1 ms | 0.21 | 8 ms | 43 ms | 0% |
| 113 | 0.50 | 4.7 ms | 0.33 | 8 ms | 43 ms | 0% |
| 174 | 0.64 | 3.8 ms | 0.43 | 8 ms | 42 ms | 0% |
| 236 | 0.98 | 4.2 ms | 0.67 | 15 ms | 693 ms | 0% |
| 287 | 0.99 | 3.8 ms | 0.61 | 426 ms | 1,971 ms | 67% |
| 340 | 0.98 | 3.1 ms | 0.63 | 2,077 ms | 2,988 ms | 92% |

- **One Python process passes 75% of its core at about 200 req/s**, against the plan's 190, and
  collapses near 280 req/s. The database stays under 50% until then, so `api-compute` is the first
  pressure, as the storyline needs. Database time is 99% reads at the genesis mix.
- **API CPU is about 4 ms per request** on this mix, as the plan assumed (plan: "a read costs
  about 4 ms of CPU in Python").
- **Database CPU is about 3.9 ms per request** at the genesis mix (40% listings). That is heavy
  enough to arm `db-read` late in chapter 1 rather than in chapter 2. The changes below should
  bring it down; the full-storyline run will show where it lands.

### What the measurements changed

| Finding | Change |
| --- | --- |
| PostgreSQL 18's `pg_stat_statements` drops a leading comment, so `/*gp:...*/` tags were lost | Tags go after the first keyword: `SELECT /*gp:read:product*/ ...` |
| Aggregating reviews for a whole category made every listing cost ~21 ms of database time | Newest and price sorts page first and aggregate 24 rows (0.5–2 ms); only "sort by rating" aggregates the whole category (~10–15 ms) |
| Facets grouped every product in the category from the heap (~2 ms, 23% of database time) | Genesis has an index on `(category_id, price_cents)`, which a startup would add for its listing page; facets are an index-only scan (~1 ms) |
| Naive `ILIKE` search scans 50,000 descriptions: 100–330 ms a call, half the database's time at 2% of steps | "Word gets out" sends search on 0.2% of shopper steps. Search is the seed of pain for the search storylines, not the MVP |
| Python handlers cost 0.5–2.5 ms, too light for the plan's 4 ms | Jinja2 server-side templates and related-product ranking over 120 candidates with text similarity: realistic work that brings the mix to ~4 ms |
| Category × sort × page is a small key space, so listing views repeated (repeat ratio ~0.6) even in the long tail | Listings take an optional price range, and the browsing chapter narrows 70% of listing views with one, which is how faceted browsing spreads keys |
| Largest category held ~9% of the catalog | Category sizes follow a Zipf exponent of 0.3: 486 to 2,895 products, mean 1,316 |

### Per-statement database time (ramp 2, genesis mix)

| Statement | ms per call | Share of database time |
| --- | ---: | ---: |
| `listing_rating` | 14.1 | 30% |
| `listing_facets` (before the index) | 1.9 | 23% |
| `search` | 192 | 21% |
| `related` | 1.0 | 9% |
| `listing_price_asc` / `price_desc` | 2.7 | 7% |
| `listing_newest` | 0.6 | 3% |

Statement execution time covers only about half of the database's cgroup CPU. The rest is
per-query protocol work, commits, WAL, and background processes, so capacity is measured from
cgroup CPU, never from `pg_stat_statements` alone.

## No-architect run (the stage-2 gate)

The full "Word gets out" storyline, seed 42, on the local stack, with the architect in observe
mode (13 November 2026, `tools/run_summary.py`):

| t | Offered req/s | Achieved (1 min) | API CPU / cap | DB CPU / cap | Read p95 | 1-min availability |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 3:00 | 176 | 113 | 0.57 | 0.27 | 37 ms | 100% |
| 4:00 | 254 | 193 | 0.78 | 0.40 | 46 ms | 100% |
| 5:00 | 295 | 245 | 0.98 | 0.49 | 297 ms | 100% |
| 6:00 | 331 | 245 | 0.99 | 0.46 | 1,912 ms | 36.4% |
| 7:00 | 373 | 306 | 0.99 | 0.60 | 2,672 ms | 1.7% |
| 12:00 | 614 | 481 | 0.71 | 0.50 | 4,515 ms | 1.7% |
| 17:00 | 760 | 584 | 0.82 | 0.63 | 4,509 ms | 1.2% |

The architect, watching but not acting, put `api-compute` on pending at 2:40, armed it at 3:15,
and recorded that it would choose the autoscaled fleet (scaling up on `node-01` lost on the
ledger). At 5:30 it recorded that the spent error budget now allows only protective moves.

"Achieved" trails "offered" for two reasons: a one-minute rate lags a growing target, and
sessions are capped at 12 steps, so the mean was 4.66 requests, not the 5 the crowd assumed.
The crowd now uses the capped mean.

## First playground runs, 13 November 2026

A private premium session (`e2e/private_session.py`): `ops-01` 2 vCPU / 4 GiB, four nodes at
2 vCPU / 3 GiB. Summaries from `tools/run_summary.py` run on `ops-01`.

**Network.** `node-01`'s link carries about **20 MB/s**: an uncapped `pg_basebackup` of the
333 MB database to `node-04` ran at 20.4 MB/s, and `--max-rate=8M` held 8.3 MB/s. At
~450 req/s the shop itself sends ~7.5 MB/s from `node-01` (responses to the crowd and query
results to the pods), so an uncapped base backup starves it.

**Seed 102507 (the first run with working credentials, addresses, and agent).**

| t | Offered req/s | Achieved | API CPU / cap | DB CPU / cap | Read p95 | 1-min availability |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 3:00 | 180 | 122 | 0.23 | 0.30 | 49 ms | 100% |
| 5:00 | 319 | 289 | 0.49 | 0.70 | 49 ms | 100% |
| 9:00 | 434 | 422 | 0.41 | 0.74 | 46 ms | 100% |
| 10:00 | 468 | 450 | 0.44 | 0.70 | 943 ms | 77.7% |
| 12:00 | 528 | 503 | 0.41 | 0.81 | 61 ms | 100% |
| 17:00 | 626 | 618 | 0.44 | 0.93 | 79 ms | 100% |

- **Move 1** (fleet): decided at 2:35, steps done by 4:01, verified at 5:00. Zero errors in
  35,575 requests across the move. Pods were ready within 5 s of the apply (the twin assumes
  15 s). The HPA reached 5 pods by 4:45 and 6 by 11:00.
- **Move 2** (replica): decided at 9:15, late because the agent's PostgreSQL DSN was broken
  until 8:20 (fixed live). The uncapped base backup (244 MB in ~15 s) caused the only breach:
  ~3,500 timeouts in 20 s. The backup is now capped at 6 MB/s (`pg-replica-create`).
- **Move 3** (cache): refused at 13:20 because the breach had spent the error budget. It ran
  after the storyline ended, with no traffic, so only its mechanics were tested (Valkey ready,
  config converged at 10/50/100%); verification failed on the hit ratio, as it should with no
  reads.
- **Rollback**, seen twice before the fixes: both rolled back from genesis within 10 s, and
  the second one, from the barrier with two pods running, restored the edge, deleted the
  fleet, and re-tainted the nodes with zero errors.

**Clocks.** Each VM synced to internet NTP servers on its own, and they sat 10–20 ms apart
(measured when the nodes first synced to `ops-01`: `node-01` +30.6 ms, `node-02` +18.6,
`node-03` +7.4, `node-04` +7.5). Queue time is a pod's clock minus nginx's `X-Request-Start`
from `node-01`, so after the fleet took traffic its p95 read a flat ~23 ms with pods at 50% CPU,
and the fleet's 10 ms check failed in three runs that had no errors. Replay lag on a replica has
the same exposure. The images now run chrony everywhere, with `ops-01` as the nodes' only source.
`systemd-timesyncd` pointed at `ops-01` (tried first, live) was still 4–18 ms apart after
several minutes.

## Rebuilt images on a fresh session, 14 November 2026

Session `6af8e071620f7b20244a587f`, booted from `make images` with every fix and no hand
patches, same VM sizes as above.

**Clocks.** With chrony and `ops-01` as the only source, every node sat within 6 µs of
`ops-01` a minute after boot (`chronyc tracking`), against 10–20 ms under per-VM internet NTP.
`ops-01` itself serves from `local stratum 10`.

**API cost.** The Python API costs about **1.9 ms of CPU per request** on a playground node
(`api.cpu_per_request` at genesis; the first session measured ~1.7 ms, 0.49 of a core at
289 req/s). The twin uses the local stack's costs, **4.27 ms** per request at 3:00 for seed
102507. So the twin puts the API at 0.57 of its ceiling at 135 req/s, while the playground
measured 0.24 at 132 req/s. Both playground sessions agree on the API's cost. CPU steal was
about zero on every VM.

The cost per request also falls as load rises (seed 1003: 1.74 ms at 219 req/s, 1.33 ms at
371 req/s), so the API's CPU grows more slowly than its traffic.

**Decision times with a full core.** On the first session, genesis queue p95 was ~9 ms at
~100 req/s and reached 25 ms around 2:30, which armed `api-compute` at 2:20–2:36 in every gate
run. On this session the same load gave 2–4 ms, so queue time never armed it. The two-minute
CPU forecast did, while traffic was still steep:

| Seed | Decided at | Move took | Requests in the move | Errors | Verdict |
| --- | ---: | ---: | ---: | ---: | --- |
| 102507 | 4:46 | 175 s | 60,586 | 0 | verified |
| 727274 | 4:25 | 170 s | 61,262 | 0 | verified |
| 1001 | 3:51 | 170 s | 62,418 | 0 | verified |
| 1002 | 4:26 | 175 s | 63,600 | 0 | verified |
| 1003 | — | — | — | — | no move: API CPU flat at ~0.50 from 4:45 to 8:15 while traffic rose 313 → 373 req/s |

Every move was clean, but Move 1 depended on a linear forecast of a curve that flattens, and
seed 1003 never armed. `gp-twin -api-cost 0.43` reproduces it: fleet decisions at 4:25–4:55
at ~0.55 CPU.

**The fix: one capacity unit.** The twin counts an API instance as one local core. The
genesis unit is now `CPUQuota=40%` on the playground, the measured ratio (0.40–0.43) rounded
down for the falling cost per request. In the twin (100 seeds, `-api-cost 1.075` for 40%)
every seed still takes fleet → replica → cache, the fleet is decided at 2:15–2:55, and the
worst 5-minute availability is 99.73%. Pods keep their 1-core limit, so after Move 1 the fleet
has more headroom than the twin assumes.

**The gate with the cap** (same session, `node-01` on the rebuilt image, a fresh database):
10 of 10 completed and verified, 0 errors in 501,301 requests. Decisions came at 2:41–3:06,
median 2:50. In seed 102507 `api-compute` went pending at ~2:00 on the forecast (CPU 0.50) and
armed at 2:30 (0.65); the real CPU passed 0.75 about 15 s later, and genesis peaked at 0.87 of
its cap during the handover with queue p95 at 9 ms.

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

**Database, not yet acted on.** On this session the primary was also cheaper than the twin
predicts: `db.cpu_ratio` 0.35 at 373 req/s (seed 1003, before any move), where the twin has
about 0.75. On the first session it matched the twin. Moves 2 and 3 have not run since the cap.

## Full storylines with the cap, 15 November 2026

Session `6af904b0620f7b202454351c`, booted from the 23:35 images (40% genesis cap) with no hand
fixes; same VM sizes. Runs with `e2e/storyline_run.py`, five sessions each through the platform.

| Run | Moves | Fleet decided | Requests | Errors | First breach |
| --- | --- | ---: | ---: | ---: | --- |
| 727274, acting | fleet only | 1:48 | 441,930 | 0 | read p95 at 3:00 (597 ms) |
| 1003, acting | fleet only; `db-read` armed at 14:31 with no move that fit | 2:06 | 415,553 | 0 | read p95 at 3:00 (340 ms) |
| 727274, observe (control) | none; would have taken the fleet at 1:51 | — | 423,492 | 394,432 | both SLOs at 3:00 |

The twin (`-api-cost 1.075`) has the fleet at 2:30 and 2:45, the replica at 6:20 and 7:00, and
the cache at 11:30 and 12:00. In its control run the errors start at about 4:00 and stay
proportional to the overload (25–44%); the real shop failed a minute earlier and completely.

**The database is cheaper than the twin, and not by a fixed factor.** Database cgroup CPU per
front-door request, and the twin's database ratio against the measured one:

| t | 727274 req/s | DB ms/req | Twin's DB ratio | Measured (150% cap) | 1003 DB ms/req |
| --- | ---: | ---: | ---: | ---: | ---: |
| 3:00 | 150 | 1.91 | 0.39 | 0.22 | 2.28 |
| 6:00 | 342 | 1.35 | 0.71 | 0.32 | 1.54 |
| 8:00 | 437 | 1.20 | 0.89 (no replica yet) | 0.34 | 1.44 |
| 12:00 | 612 | 1.44 | 0.87 (replica, no cache) | 0.58 | 1.45 |
| 17:00 | 638 | 1.47 | — | 0.64 | 1.72 |

Fitting `gp-twin -db-cost` to seed 727274 gives about 0.44 in chapter 2 (where the replica is
decided) and 0.33 in chapter 3 (where the cache is), so one scale factor, or one cap, cannot
match both. Per statement, against the local stack (`ms per call`, 5:00–10:00):
`listing_rating` 2.7–4.3 (local 14.1), `listing_facets` 0.39–0.73, `listing_newest` 0.65–1.18,
`listing_price_*` 0.25–0.34 (local 2.7), `related` 0.34–0.41 (local 1.0), `search` 46–66
(local 192). The database's cgroup CPU was 1.07–1.13 times statement time at moderate load,
against the twin's `DBOverhead` of 1.7.

**The twin's storyline needs the database within about ±10% of its model.** Sweeping 50 seeds
with `-api-cost 1.075`: at `-db-cost` 1.0 and 1.1 every seed takes all three moves (worst
5-minute availability 99.73% and 99.28%); at 0.9, 8 seeds miss the cache; at 0.8, 30; at 1.2,
11 miss it and the worst window falls to 83.6%.

**`node-01` saturates before PostgreSQL reaches its cap.** In seed 1003, 12:00–17:00, statement
time was 678 s against 336 s of database CPU: queries waited for `node-01`'s two vCPUs, which
PostgreSQL (up to 1.27 cores) shares with nginx at ~740 req/s. Read p95 reached 82 ms at 16:00
with `db.cpu_ratio` at 0.83. The plan's rule that a node's caps stay below its CPU does not hold
for `node-01` with a 150% database and an uncapped nginx.

**The genesis API cost more than on the gate's session.** About 5 ms of CPU per request at
30 req/s, 4.3 ms at 70, 3.3–3.6 ms at 125–150, and 1.4–1.7 ms in the fleet's pods later.
Genesis reached 0.85 and 0.79 of its 40% cap at 2:00, against the twin's 0.51 and 0.42 (1.7–1.9
times; the twin's `-api-cost 1.075` is fitted to the previous session), and against about 0.50
in the gate's seed 102507. So `api-compute` armed 40–60 s earlier than in the gate and the twin. Traffic doubles in the
minute after the decision, and the shift completes 80 s after it, so the legacy process queued:
read p95 breached 250 ms in the minute to 3:00 in both acting runs, with no errors. CPU steal
on `node-01` was negligible (185 jiffies since boot). Between sessions, then, the database has
varied by about 2.5 times (the first session against the second and third) and the genesis API
by about 2 times (the second against the third). The plan's spike question 13 asks exactly this.

**The same seed varied by a third on the same session.** A fourth run, seed 727274 again (meant
as a 60% database cap, below), decided the fleet at 1:53 with 0 errors in 441,428 requests. Its
database used 1.15–1.31 cores from 12:00 to 17:00 against 0.88–0.94 in the first run at the
same load (1.9–2.1 ms per request against 1.4–1.5), so `db-read` armed at 12:03 where the first
run never armed it. Rows accumulate between runs (about 2,300 products a run, and their
reviews), which may explain part of it; this was not measured. No move fitted: the replica's
shape needs `reads.repeat_ratio < 0.4` and it was 0.72, and the cache needed 0.25 cores where the
ledger had 0.2 free. In the twin, the replica always comes first, and the cache then fits.

**A database cap could not be tried: the image's cap overrode it.** `systemctl set-property
--runtime ... CPUQuota=60%` set `cpu.max` to 60%, but the reset's `systemctl enable` reloaded
systemd, and the image's drop-in `caps.conf` sorts after set-property's `50-CPUQuota.conf`, so
150% came back (`DropInPaths` listed both; the agent reported a 1.5-core limit all run). The
architect's `pg-cap` action (the database scale-up move) had the same fault: its raise held
only until the next reload. The drop-in is now `10-caps.conf`, not yet booted.

## The database re-fitted to the playground, 15 November 2026

The owner chose to re-fit the twin's database costs to the playground (over a fixed cap scaled
from the local stack, or measuring each session at genesis). The re-fit uses only the data above:
`pg_stat_statements` windows (5:00–8:00, 7:00–10:00, 12:00–17:00) from seeds 727274 and 1003, and
the database's cgroup CPU every minute in three acting runs. No session was used.

**The traffic model already matched; the statements did not.** The twin's listing sorts against
the playground's calls: chapter 2 rating 20%, newest 30%, price 50% (measured 19–20%, 30–35%,
46–50%); flash sale 48%, 22%, 31% (measured 47%, 22%, 31%). Listing views, detail views, and
searches per second were within about 5%. So the gap was per-statement cost, which is not one
factor:

| Statement | Local stack | Playground, ms per call | Twin before | Twin now |
| --- | ---: | ---: | ---: | ---: |
| `listing_rating` | 14.1 | 2.7–4.3 (chapter 2) | 0.0088 ms per product in range | 0.0044 |
| `listing_price_*` | 2.7 | 0.25–0.34 | 0.55 | 0.29 |
| `listing_newest` | 0.6 | 0.65–1.18 | 0.9 | 0.85 |
| `listing_facets` | 1.9 (before its index) | 0.34–0.73 | 0.5 | 0.5 |
| Detail view (`related` 0.31–0.41, reviews, the rest) | ~1 | 0.51–0.63 with writes | 1.05 | 0.45 |
| `search` | 192 | 46–66 | 50 | 50 |
| `home_featured` | | 0.05–0.07 | 0.12 | 0.06 |
| Writes | | not separable from detail | | halved |
| cgroup CPU | | | 1.7 × statement time | 0.06 cores at rest + 1.1 × statement time |

The rest (0.06 cores) is the database's own background and the agent's queries: it is what all
three runs used in their first minute at ~30 req/s. Database costs in the twin are now in
playground cores (`internal/twin/costs.go`); API costs stay in local-stack units, as before.

Measured cgroup CPU against the re-fitted twin (cores, and the ratio), with no move but the fleet:

| t | 727274 | 1003 | 727274 again |
| --- | --- | --- | --- |
| 3:00 | 0.29 vs 0.23 (1.29) | 0.28 vs 0.19 (1.50) | 0.32 vs 0.22 (1.43) |
| 5:00 | 0.41 vs 0.40 (1.03) | 0.51 vs 0.36 (1.43) | 0.48 vs 0.40 (1.21) |
| 7:00 | 0.48 vs 0.57 (0.85) | 0.53 vs 0.50 (1.06) | 0.56 vs 0.57 (0.99) |
| 9:00 | 0.58 vs 0.71 (0.82) | 0.56 vs 0.60 (0.93) | 0.65 vs 0.71 (0.91) |
| 11:00 | 0.69 vs 0.91 (0.76) | 0.69 vs 0.78 (0.89) | 0.86 vs 0.91 (0.94) |
| 13:00 | 0.85 vs 0.97 (0.88) | 0.91 vs 0.93 (0.98) | 1.20 vs 0.97 (1.23) |
| 15:00 | 0.94 vs 1.00 (0.94) | 1.17 vs 1.16 (1.01) | 1.30 vs 1.00 (1.30) |
| 17:00 | 0.94 vs 1.11 (0.85) | 1.27 vs 1.20 (1.06) | 1.31 vs 1.10 (1.19) |

- **Chapters 2 and 3:** the session's first run is ~0.75–0.95 of the twin, the second ~0.9–1.05,
  the fourth ~0.9–1.3. The later runs cost more in the flash sale above all.
- **Chapter 1 runs 1.3–1.5 times the twin in every run, unexplained.** In seed 1003 the excess
  holds from 2:00 to 5:00 at rates from 65 to 272 req/s and ends when chapter 2's mix arrives,
  so it follows the mix rather than load, but no statement window covered chapter 1, and none
  of the obvious candidates (unfiltered listings, detail views, per-statement overhead) is big
  enough. The next session should take a statement window at 2:00–5:00.
- **A likely cause of the drift between runs:** shoppers review the product they last viewed,
  so in the flash sale reviews pile onto the hot products, 12 of which are in the sale category.
  That category is the same for every seed, and the rating sort aggregates every review of every
  product in it. The reset keeps earlier runs' rows, so each run's flash sale ranks more
  reviews. This is not measured.

**The re-fitted twin reproduces the playground.** At the images' 150%, seed 727274 takes only the
fleet and `db-read` never arms, as on the playground; seed 1003 takes only the fleet and
`db-read` arms at 14:35 (the playground: 14:31) with the same no-move reason: the replica's
shape fails on repeat ratio, the cache needs 0.25 cores where 0.2 are free.

**The cap.** `gp-twin -db-cap` sets a what-if quota for each database instance, in playground
cores. Valid seeds out of 30 against the database's cost relative to the re-fit (`-db-cost`),
with the plan's flash sale (700 req/s), and the database scale-up kept out of the way:

| Cap | 0.8 | 0.9 | 1.0 | 1.1 | 1.2 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 60% | 16 | 30 | 20 | 6 | 0 |
| 65% | 5 | 18 | 30 | 19 | 6 |
| 70% | 0 | 6 | 23 | 30 | 19 |

("Valid" here is the MVP's criteria: three moves migrated by 15:00 and every 5-minute window at
or above 99.5%.) Too cheap, and the flash sale never reaches 70% of the primary's and the
replica's combined capacity, so the cache comes late or not at all. Too dear, and `db-read` arms
in chapter 1, where reads repeat at 0.43–0.48, between the replica's shape (< 0.4) and the
cache's (≥ 0.6); the replica can start only after the fleet's verification and cooldown (~6:00),
and the primary saturates at 6:30–7:30, while it migrates. Those errors spend the budget, and the
cache is then refused.

What widened the window, and what did not (20–30 seeds per cell):

| Change | Effect |
| --- | --- |
| Flash sale to 800 req/s (±15% by seed) | The low side: at 65%, −10% went from 18 of 30 to 30 of 30, −20% from 5 to 17. Kept. |
| Chapter 2's mix shift over 180 s instead of 90 s | No consistent change (within a few seeds either way, with either flash sale). Dropped. |
| Replica holds of 10 s instead of 20 s | The high side, +10% cost: 9 → 15 of 20. Dropped: it halves the step holds that watch replication and errors. |

**The result: one db.medium is a 65% quota on the playground**, with the flash sale at 800 req/s.
Valid seeds out of 30 by the same criteria:

| Database cost vs. the re-fit | 0.7 | 0.8 | 0.9 | 1.0 | 1.1 | 1.2 | 1.3 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Seeds | 1 | 17 | 30 | 30 | 19 | 6 | 0 |

At nominal cost, 100 of 100 seeds take fleet → replica → cache with the worst 5-minute window at
99.74%: fleet at 2:15–2:55, replica 5:50–7:10, cache 10:45–13:05 (median 11:35). The twin's
stricter seed rules pass 58 of 100: 33 caches are decided while the flash sale's repeat ratio is
still 0.6–0.7, and 13 replicas at 0.3–0.4. In this catalog nothing else fits at those moments
(`node-01` has 0.2 cores free, the second replica no node), so a boundary costs seconds, not a
different path; seeds for demonstrations come from the valid ones (1003 is; 727274 now is not).
A 70% cap passes 80 of 100 of those rules but only 14–15 of 20 at −10% cost, which is where the
session's first run sat.

**The margin is about ±10%, not the ±30% hoped for.** Between arming at 70% of capacity and
saturating at 100%, a fixed storyline has about ±18% to work with, less the time moves take.
Wider needs one of: the reset restoring the data (which removes the drift between runs, if the
reviews are its cause), or measuring each session's database at genesis and setting the caps
from it.

**Consequences in the images and contracts:**
- The primary's unit (`10-caps.conf`) and the replica's (`gp-pg-replica.service`) run at
  `CPUQuota=65%`; `internal/twin.PlaygroundDBCores` is the same number, and
  `internal/twin/rootfs_test.go` fails if they drift apart.
- The ledger and the contracts keep the plan's db.medium (1.5 cores), as they keep the genesis
  API at one core under its 40% cap: these are the storyline's capacity units. So `node-01` has
  0.2 cores free after Move 1 and "scale up first" loses, as in the plan's decision record. The
  scale-up move raises the quota to 87%, which is 2.0 of those cores. The relief the architect
  quotes for database moves is in those units too.
- At 65%, `node-01` no longer outruns its two vCPUs (PostgreSQL ≤ 0.65 plus nginx and the agent).
- The fleet's and API scale-up's shape was `db.cpu_ratio < 0.5`. With chapter 1 at 1.3–1.5
  times the twin, the database reaches 0.45–0.55 by 3:00, and a fleet decided late would have
  found its shape failing. Both now use 0.7, where `db-read` itself arms. No acting run in the
  twin comes near either value (0.32–0.38 at the fleet's decision).

## The re-fit on a fresh session, 15 November 2026 (evening)

Session `6afa3744d86d8571fbca0ce2`, booted from the 22:41 images (database units at
`CPUQuota=65%`, the flash sale at 800 req/s) with no hand fixes: five nodes Ready with the
right taints, 14 of 14 targets up, the agent reporting the primary's limit as 0.65 and the
API's as 0.4. Two runs of seed 1003 with `e2e/storyline_run.py`, five sessions each, plus two
to check first boot and three to query and inspect. The session was destroyed afterwards.

**The acting run took all three moves, at the twin's times:**

| Move | Playground | Twin | Took | Errors in the move |
| --- | ---: | ---: | ---: | ---: |
| `autoscaled-fleet` | 2:57 | 2:45 | 170 s | 0 of 43,174 |
| `read-replica` | 6:52 | 6:45 | 220 s | 0 of 90,639 |
| `cache` | 11:57 | 11:55 | 130 s | 8 of 85,151 |

All three were verified. The whole run had 27 errors in 440,276 requests (99.994%), every one a
search timeout (the crowd's read timeout is 2 s). Read p95 was under 100 ms in every minute
but 12:00 (156 ms), 13:00 (362 ms, over the 250 ms target), and 16:00 (131 ms); the handover's
worst was 93 ms at 4:00. `db-read` armed again at 16:02 and nothing fitted (the cache was up, `node-01` has no
CPU for a database scale-up, and no node has 1.5 cores for a second replica).

**It does not yet meet MVP criterion 2.** Search (1.8 req/s) was the only journey under 99.9%
in any 5-minute window: 99.02% at 12:00, then 97.1%, 97.7%, 97.4%, 96.3%, and 95.6% at 17:00,
against 99.5%. Latency missed its target in 1 of 17 minutes (94%, against 95%).

**Chapter 1's excess did not come back.** The database's CPU over 2:00–5:00 was 0.31 cores at
202 req/s, against the twin's 0.35 at 210 req/s (about 0.93). Minute by minute, in cores (the
twin's at the same seed):

| t | 2:00 | 3:00 | 4:00 | 5:00 | 6:00 | 7:00 | 8:00 | 11:00 | 12:00 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Primary | 0.09 (0.16) | 0.18 (0.24) | 0.30 (0.34) | 0.47 (0.39) | 0.40 (0.46) | 0.45 (0.55) | 0.47 (0.57) | 0.30 (0.36) | 0.39 (0.38) |
| Replica | | | | | | | | 0.46 (0.59) | 0.59 (0.62) |

(The twin's columns here are after the routing fix below; before it, the replica's were 0.50
and 0.53.) So this session sat at about 0.8–0.9 of the re-fit before the cache (0.6 at 2:00,
where the twin's 0.06 cores at rest dominate, and 1.2 at 5:00), where the previous session's
chapter 1 ran at 1.3–1.5. Costs vary between sessions; this one also started with a database
no earlier run had written to.

**Statement time is not CPU under a 65% cap.** Over 2:00–5:00 statements took 0.61 s per second
of wall time against 0.31 cores of the unit's CPU, and a call's time grew with the load: search
223 ms (146 ms in the control run's lighter window, 46–66 ms at 150% on the last session), the
rating sort 19.4 ms (11.0), newest 2.3 ms (1.7). With `CPUQuotaPeriodSec=10ms` a long statement
spans many periods and waits out each one's throttle, so the windows measure latency. Calls per
second did match the twin in chapter 1 (newest 49 against 51, rating 9.5 against 9.8, price 21
against 22, facets 80 against 82, related 61 against 59), except search at 0.9 a second against
0.4.

**After the cache, the replica runs at 1.4–1.6 times the twin (as it was), and the twin's
read routing was wrong.** Cores from 14:00 to 17:00, playground (twin before the fix):

| t | 14:00 | 15:00 | 16:00 | 17:00 |
| --- | ---: | ---: | ---: | ---: |
| Primary | 0.32 (0.32) | 0.34 (0.36) | 0.37 (0.34) | 0.38 (0.35) |
| Replica | 0.46 (0.34) | 0.53 (0.38) | 0.59 (0.37) | 0.53 (0.37) |

The shop's cache lookups over 14:00–17:00: listings 268.8 hits, 93.3 misses, and 82.6 bypasses a
second (a hit share of 0.60; the twin's 0.59–0.60); product pages 56.8, 102.2, and 70.4 (0.25;
the twin's 0.46–0.47). Bypasses are the reads with a read-your-writes token (22%), which never use
the cache. In the shop they take the read pool like any other read and go back to the primary
only when the replica has not replayed their write: 14.7 a second of the ~110 that picked the
replica (`gp_read_routing_total{reason="token"}`). The twin sent every token read to the
primary. Once the cache is up those reads are 47% of the listings that reach the database, so
the twin missed much of the replica's load. Fixed (`tokenNotReplayed = 0.13` in
`internal/twin/system.go`), the twin's replica reads 0.38–0.43 cores after the cache (the
playground: 0.46–0.59) and its primary 0.27–0.31 (0.32–0.38). The remaining gap of about 20–35%
in the database's total is unexplained. Product-page hits, at half the twin's, account for only
~0.025 cores of it. The twin also samples the primary's ratio and the cache's hits by route
now, in its `-json` output.

With the routing fixed, 100 of 100 seeds still take fleet → replica → cache at the same times,
but six have a 5-minute window under 99.5% (worst 99.18%; before, 99.74%), and the margin on
the expensive side halves. MVP-valid seeds out of 30 (three moves migrated by 15:00, every
5-minute window at or above 99.5%):

| Database cost vs. the re-fit | 0.8 | 0.9 | 1.0 | 1.1 | 1.2 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Before the routing fix | 17 | 30 | 30 | 19 | 6 |
| After | 17 | 30 | 29 | 11 | 2 |

The twin's availability is not per journey, so it cannot see search failing on its own: search is
a 46–66 ms statement on a replica at 0.8–0.9 of its quota, and its slowest calls passed the
crowd's 2 s timeout.

**The control run collapsed completely, and nginx is why.** Seed 1003 with the architect
observing: both SLOs breached at 5:00 (the twin: errors from about 4:00), and every journey's
5-minute availability fell to 0–6% by 17:00 (353,449 errors in 406,686 requests). The observing
architect would have taken the fleet at 3:08. From 13:00 the genesis API used 0.005 cores and the
database 0.006: the process was up (no restarts, 80 MB, no OOM kill, and it answered directly in
1.5 ms afterwards), but nothing reached it. `node-01`'s nginx runs Ubuntu's default
`worker_connections 768`, 1,536 connections across two workers, counting its upstream
connections. Once responses took seconds, client connections held every slot, so nginx could not
open upstream ones; it logged "worker_connections are not enough" 7,078 times by t+11 and then
nothing, likely because it had stopped accepting. The twin's control run degrades in proportion
to the overload (15–84% errors) because it has no such limit. The API's pool also ran dry twice
("couldn't get a connection after 2.00 sec", at 8:04 and 11:10). Since 2026-11-17 the node image
sets `worker_connections 4096` and `worker_rlimit_nofile 16384`; not yet run.

## The plateau measured uncapped, 17 November 2026

Session `6afc375b4bb0c11ddef73787`, booted from the 2026-11-17 images (nginx at
`worker_connections 4096`) with no hand fixes: five nodes Ready with the right taints, 14 of 14
targets up, chrony serving all four nodes, the agent reporting the API's limit as 0.4 and the
primary's as 0.65, and a `node-01` nginx worker allowed 16,384 open files. Two runs of seed 1003
(`e2e/storyline_run.py`, six and five sessions), two to check first boot, and two for a quota
benchmark (`e2e/quota_bench.py`; the first stopped on a wrong cgroup path): 15 sessions besides
the start. Destroyed afterwards.

**The acting run took all three moves at the twin's times again**, with the replica's quota
lifted at 15:00 (`--lift-replica-at 15:00`; `cpu.max` read `max 100000` a second after):

| Move | Playground | Twin | Took | Errors in the move |
| --- | ---: | ---: | ---: | ---: |
| `autoscaled-fleet` | 2:48 | 2:45 | 175 s | 0 of 43,592 |
| `read-replica` | 6:48 | 6:45 | 215 s | 3 of 88,272 |
| `cache` | 11:48 | 11:55 | 135 s | 14 of 88,340 |

All three verified; 24 errors in 439,939 requests, every one a timeout. Search failed as on the
last session while the replica was capped: the worst journey's 1-minute availability was 86.5% at
12:00 and 13:00 (read p95 463 and 564 ms, during the cache's rollout) and 98.9% at 15:00. **Once
the quota was lifted, search stopped failing:** p95 23 and 18 ms at 16:00 and 17:00, and every
journey at 100%.

**Uncapped, the replica needs 0.54 cores on the plateau, less than its 0.65 quota.** By window
(cores, the share of the window throttled summed over the node's two CPUs, and statement time):

| Window | Replica | Primary |
| --- | --- | --- |
| 12:00–15:00, capped | 0.516 cores, throttled 0.72, statements 2.99 s/s | 0.369, throttled 0.29, 0.95 s/s |
| 15:00–17:00, replica uncapped | 0.541 cores, not throttled, statements 0.48 s/s | 0.400, throttled 0.30, 0.74 s/s |

Uncapped, statement time is CPU (0.48 s/s against 0.54 cores, 0.06 of it at rest). Capped, the
same work took six times its CPU in statement time. The primary, at 62% of its quota, still took
2.7 times the replica's time for a rating sort (14.5 ms against 5.4 ms) and four times for a
search (275 ms against 67 ms).

**A benchmark isolated it: under a quota, long statements wait for the quota, whatever its
period.** `e2e/quota_bench.py` on the idle `node-01` (kernel 6.1, two vCPUs, a 5 ms bandwidth
slice): the shop's rating sort on a random category (8.4 ms of CPU per call uncapped) and a full
search scan (146 ms) at fixed Poisson rates, 45 s per case. Latency in ms, as a client sees it:

| Load (of a 0.65 quota) | Quota | Periods throttled | Rating p50 / p95 / p99 | Search mean / p95 / p99 |
| --- | --- | ---: | --- | --- |
| 0.40 cores (62%) | 65%, 10 ms period | 71% | 16 / 101 / 236 | 519 / 1,029 / 1,387 |
| | 65%, 100 ms period | 35% | 8.6 / 71 / 189 | 392 / 743 / 876 |
| | none | | 8.1 / 16 / 21 | 151 / 163 / 183 |
| 0.52 cores (80%) | 65%, 10 ms period | 76% | 25 / 216 / 388 | 782 / 1,374 / 1,973 |
| | 65%, 100 ms period | 53% | 11 / 164 / 289 | 698 / 1,729 / 2,634 |
| | none | | 8.1 / 17 / 28 | 155 / 177 / 212 |

A quota makes the database one 0.65-core server that every statement shares: a lone 146 ms search
takes at least 225 ms, and at 80% busy about 1/(1 − 0.8) times that, past the crowd's 2 s timeout
in the tail. Without a quota the same load is 26% of two vCPUs. The 100 ms period halves the
short statements' medians but not the tails, and does nothing for search. (A case with
`cpu.max.burst` did not run: removing the quota made systemd drop the unit's cpu controller, so
the file was gone. The tool now runs it before the uncapped case.) So search failed on the
plateau because the replica runs at 0.8 of a fractional quota, not because the twin under-counts
its CPU.

**The twin is low on the plateau, but not in one place.** This session's database matched the
twin before the cache (the primary at 1.09 of it over 2:00–5:00 and 1.01 over 5:00–8:00), and ran
about 1.3 times it on the plateau (replica 0.54 against 0.41–0.43 cores, primary 0.40 against
0.29–0.31). The plateau's read time, each part at the uncapped replica's ms per call:

| Part | Playground, 15:00–17:00 | Twin at 16:00 | Ratio |
| --- | --- | --- | --- |
| Listings reaching the database | 179/s at 2.86 ms (rating 34% of them, 5.37 ms each) | 187/s at 2.09 ms (rating 48%) | count 0.96, cost 1.37 |
| Product pages reaching the database | 169/s at 0.60 ms | 122/s at 0.45 ms | count 1.39, cost 1.33 |
| Search | 2.0/s at 67 ms | 1.7/s at 50 ms | count 1.2, cost 1.33 |

- As many listings reach the database as the twin says (last session's measured listing hit
  share was 0.60, the twin's 0.59), but each costs more. Weighting the twin's hits by each
  listing's cost (hits are mostly the sale category's few pages) changed its replica by 2%, so
  that is not it. The rating sort's cost does grow: by the time of the benchmark, the sale
  category (486 products) held 9,601 reviews, 4,818 of them written in the last three hours, most
  by the acting run's flash sale (the control run served few requests). The twin prices a rating
  sort by products, not reviews.
- Product pages hit the cache less than the twin says, on both sessions (about 0.25–0.27 of
  product reads against 0.47). Their bypass share (about 0.31 last session) is above the twin's single
  token share (0.18): a seller's re-read always carries a token. Not modelled yet.
- The benchmark's uncapped rating sort, over categories drawn uniformly, cost 8.4 ms of CPU per
  call against the twin's 6.4 (0.0044 ms × 1,316 products × 1.1), with the reviews this session
  had added.

The twin's costs are unchanged. Re-fitting them from 15:00–17:00 would fold this session's level
and the run's added reviews into every chapter, where the twin already matched; the parts above
are structural (reviews per category, product-page bypass), and the search failures come from
the quota, which the twin's latency model does not see.

### The twin's search availability check, 17 November 2026

The twin now keeps a rolling five-minute error ratio for each crowd journey, the same scope as
the judge's availability rule. Its search-specific timeout estimate is deliberately limited to
the sale's replica. We inferred two *conditional* failure points from the live search minutes:
about 1.7% at 76% of the replica's quota (1.1% of all search requests before the lift, with
roughly 60% routed there), and 20% near 90% of the quota (13.5% of all search requests during
the cache rollout). It interpolates exponentially, gives no extra risk at or below 70%, and
caps the estimate at 20%. The fixed-rate quota benchmark independently put search's p99 near
the crowd's two-second timeout at 80% busy; it does not establish the exact failure curve for
the live query mix. This is a backtest fitted to one run, not an independent prediction.

With seed 1003 and `-api-cost 1.075`, the twin's one-minute search availability reaches 88.1%
at 12:00 as the cache arms; the live run had 86.5% at 12:00 and 13:00. The twin rises to
94.4% by 13:00, so it recovers too early. The twin's worst rolling five-minute
search availability is 95.24%; the earlier fresh session's was 95.6%. At a fixed *measured*
0.54-core demand, the estimate gives about 6% conditional search timeouts under the 0.65-core
quota, and none at 0.87 cores or uncapped. The latest run did stop failing once uncapped. The
ordinary twin's post-cache CPU is only 0.41–0.43 cores; until review growth and detail-cache
bypass are represented, its timeline clears search's risk too early and understates the late
plateau. A cap change in the twin from replica creation also changes the architect's timing,
which a fixed-load comparison cannot show.

Twenty-seed what-ifs (`gp-twin -sweep 20 -api-cost 1.075` plus the listed flags):

| Change | Three moves completed | Worst five-minute search availability, median | Fully valid seeds |
| --- | ---: | ---: | ---: |
| Current | 20 | 94.98% | 0 |
| `-replica-cap 0.87` | 20 | 99.09% | 0; five caches finish past 15:00 |
| `-replica-cap 1` | 13 | 100% | 5; seven caches unfinished |
| `-search-cost 0.2` | 20 | 99.99% | 14; six shape-margin misses |

The 0.87-core quota alone misses the SLO because it postpones the cache while traffic keeps
growing. A fivefold cheaper search is only a sensitivity case: no index or search engine has yet
been measured to provide it, and a new backend would need its own capacity model. The model's
current tail does not apply to the primary, where the live run had no corresponding failures at
its earlier high CPU ratios.

### Search indexes and the first indexed VM run, 17 November 2026

The search route's `ILIKE '%term%'` on both product title and description did a full product
scan when a term matched nothing. On a clean 50,000-product PostgreSQL 18 catalog,
`EXPLAIN ANALYZE` measured 227 ms for `%zqxj%` without indexes and 0.127 ms with `pg_trgm` GIN
indexes on both fields. Common `%wool%` remains a fast sequential scan at about 3 ms. Under a
0.65-core Docker quota, a 45 s fixed-rate mix of 10 rating sorts/s and 1.2 no-match searches/s
collapsed without indexes: rating p99 17.2 s, search p99 48.3 s, and 28/30 searches exceeded
the crowd's 2 s timeout. With the indexes, the offered rate was sustained: rating p99 416 ms,
search p99 85 ms, and 0/56 searches exceeded 2 s. At 20 rating sorts/s, even the indexed mix
had 5/52 searches over 2 s, so the indexes do not guarantee the SLO under arbitrary load.

The indexes are now present at genesis. The twin's `DBSearch` cost changed from 50 to 10 ms;
the search timeout model scales its unindexed risk by 0.2. A 20-seed sweep at the playground's
API cap kept fleet → replica → cache in every seed and the worst predicted journey above 99.5%.
The first indexed private VM run (session `6afc674163321e6eb4e7e97a`, seed 1003, default
0.65-core replica quota) took all three moves, all verified: fleet decided 2:58, replica 6:53,
cache 12:08; 0 errors in 439,990 requests, and 0 in each move's change window. Search no longer
timed out. Its one-minute read p95 reached 483 ms at 13:00 while the cache was rolling out;
other sampled minutes stayed under 250 ms. The MVP latency-window gate therefore still needs
an earlier cache transition and a new live run. The index removed the search bottleneck while
leaving the replica's CPU pressure large enough to trigger the cache.

The updated image set with a 20 s post-replica pressure window and 5 s cache rollout holds
was applied to the same private session. The clean-cluster reset verified its SHA-256 checksum
and exactly 50,000 products. With no Start press, the crowd started seed 1003 on its own at
15:36:49 UTC after shop readiness. The architect completed and verified fleet, replica, and
cache: decisions at 2:45, 6:40, and 11:50; durations 175 s, 230 s, and 100 s. The automatic
run report showed 447,811 client requests and zero failures. Querying the judge at the run's
own timestamps found worst rolling five-minute availability 100%, all 17 one-minute latency
windows within target, zero read-your-writes violations, and zero acknowledged writes lost.
This is the first live run to meet the full SLO part of the MVP gate without an operator. The
ten-run repeatability gate is in progress on the same snapshot.

**The control run still collapses, now at the API.** Seed 1003 observing: read p95 breached at
4:00 (282 ms) and availability at 5:00 (40%); the observing architect would have taken the fleet
at 2:58. From 6:00 to 17:00 every journey's 1-minute availability was 0%: 370,180 errors in
410,174 requests (226,088 timeouts, 144,092 5xx). nginx's fix held: no "worker_connections are
not enough", and the genesis API stayed at its full 0.4 cores to the end (last session it
idled from 13:00). nginx's error log has 191,614 "upstream timed out while connecting": the API's
accept queue (backlog 2,048) was full, and the requests it did serve had waited longer than the
crowd's 2 s. The twin's control run serves its capacity's worth and fails the rest (15–84%
errors); with no admission control the real shop's goodput falls to nothing. Stage 2's gate
(breaches at about the predicted times) still holds.

## Final MVP timing and database headroom, 19 November 2026

The 2026-11-18 ten-run seed-1003 gate passed on the indexed images, but later automatic-start
checks exposed two narrower latency margins. When the replica verified at 10:45, its 60 s
cooldown kept the cache from being chosen until 11:45; read p95 reached 449 ms at 12:00. A
15 s post-replica cooldown moved the cache decision to 11:20 on the next run and read p95 at
12:00 was 161 ms. That run chose the fleet at 2:55, leaving the 0.4-core legacy API under
traffic until 4:15. At 4:00 its handler p95 was 873 ms while the two new pods were about 25 ms;
client read p95 was 780 ms. Lowering the two-minute API forecast threshold from 80% to 60%
chose the fleet at 2:25–2:30 in the next fresh-VM runs and shifted it by 3:45–3:50; read p95
at 4:00 fell to 23–26 ms.

The first fresh-VM trial with that earlier fleet still missed the final plateau minute. The
cache lifted only the replica to one core; seven browse/search requests timed out among 442,031,
and read p95 reached 284 ms at 17:00. During 15:00–17:00 both database nodes had physical CPU
spare, but the primary's 0.65-core quota throttled it for 34% of wall time at 0.42 core average,
and the replica's one-core quota throttled it for 13% at 0.56 core average. Primary and replica
rating sorts took 42 and 17 ms per call respectively.

The cache move now unlocks the 1.5 cores already reserved for **each** database after it has
diagnosed repeated reads. On the rebuilt-image automatic-start run (`6afed80713e12b000c9f62a6`,
seed 1003), it chose fleet → replica → cache at 2:30, 6:30, and 11:15, raised the primary and
replica caps at 11:25 and 11:30, and verified all moves by 13:10. The judge counted 442,212
requests, zero failures, 17/17 compliant latency minutes, 100% worst full in-run five-minute
availability, and no correctness violations. Read p95 at 17:00 was 15 ms; plateau throttling
fell to about 0% on the primary and 1% on the replica. A same-seed gate on these images passed
the first eight runs but run 9 chose the cache at 11:23 instead of run 8's 11:17, lifted the
replica at 11:38, and had 271 ms read p95 at 12:00. It still completed all three moves with
zero change-window errors and 100% worst five-minute availability. The cache's post-replica
repeat threshold is now 0.3, and the replica cap lifts before the primary cap. An unattended
automatic-start trial on that revised image chose fleet → replica → cache at 2:25, 6:25, and
11:10, with both database caps raised by 11:25 and all moves verified by 13:05. It counted
442,220 requests, zero errors, 17/17 compliant latency minutes, 100% worst full in-run
five-minute availability, and no correctness violations. Read p95 at 12:00 was 25 ms (against
271 ms in the previous gate failure) and 13 ms at 17:00. The settled report, Grafana move rows,
and both 1.5-core quotas passed inspection. A fresh ten-run gate on that revision remains.

The fresh seed-1003 gate then passed all ten runs on `6aff2e7078d2128757a1c9ca`:
4,422,239 requests, two connection errors outside every move window, no move-window errors,
17/17 compliant latency minutes in every run, 100% worst full five-minute journey
availability, and decision spreads of 15/15/15 seconds. The revised cache transition also
cleared the old ninth-run failure: that run's read p95 at 12:00 was 72 ms. The final report,
Canvas, move table, and both 1.5-core database quotas passed inspection.

A separate seed-727274 live run exposed an earlier problem. It chose fleet → replica → cache
at 2:23, 6:18, and 10:48, and verified all three by 12:38. Among 466,779 requests were two
search timeouts at 9:00 and 11:00. Search's worst full five-minute availability was 99.397%,
below 99.5%; read p95 was 340 ms at 11:00, leaving 16/17 compliant latency minutes. During
10:00–11:00 the replica used 0.585 cores but was throttled for 86% of wall time at its
0.65-core quota. Rating sorts on it took 34.8 ms per call; from 11:00–12:00, after the cache
raised the quota to 1.5 cores, it used 0.516 cores, was throttled for about 1%, and those
sorts took 4.3 ms. Both VMs had physical CPU spare. The replica also spent 29% of 9:00–10:00
throttled, when the first search timeout occurred. The cache's 10:58 quota lift could not
protect earlier reads.

The next progression unlocks both databases' already-reserved 1.5 cores during the replica
move, before any read routing, and arms a separate cache pressure on measured repeated reads
at a meaningful request rate. The cache no longer needs a second quota bottleneck to appear.
`make test` passes and the 100-seed twin sweep retains fleet → replica → cache for every seed,
with 100% modeled worst five-minute journey availability.

A rebuilt-image seed-727274 live run then passed: 466,829 requests, zero errors, 17/17
compliant latency windows, 100% worst full in-run five-minute journey availability, and no
correctness violations. Fleet, replica, and cache were chosen at 2:23, 6:23, and 10:53 and
all verified by 12:33. The primary quota rose at 6:38 and the replica quota at 7:33, before
reads were routed at 9:13. Read p95 at 11:00 was 12 ms instead of the earlier 340 ms;
neither the 9:00 nor 11:00 search timeout recurred. The settled report, Grafana move records,
and both 1.5-core quotas passed inspection. A spurious `read-reuse` no-move note after cache
activation led to an explicit `cache == false` arm and `cache == true` disarm; a unit test
checks that the pressure clears. On a fresh session with that final ops image, an automatic-start
seed-1003 run passed: 442,191 requests, one error, 17/17 compliant latency windows, 100% worst
full five-minute availability, and no correctness violations. Fleet, replica, and cache were
chosen at 1:50, 6:15, and 11:10 and all verified by 12:50. The 12:00 and 17:00 read p95s
were both 20 ms. `read-reuse` cleared after the cache rolled out. The settled report,
Grafana records, and both 1.5-core quotas passed inspection.

## Fleet timing on automatic repeats (20 November 2026)

A manual-start repeat gate passed four complete runs but had a 99 s fleet-decision spread on
the fifth: the first four decided near 0:30 while the fifth decided at 2:06. That harness
waited only 60 s after reset, shorter than the architect's two-minute forecast history. An
attempt to hold genesis for 180 s collided with the crowd's own three-minute automatic Start,
so the repeat gate now follows automatic Start directly.

The first unattended repeat on the prior ops image chose the fleet around 2:15. The 3:00
read p95 reached 1,094 ms as the legacy API still served part of the ramp, even though
availability was 100%. The gate was stopped at that failure. The pressure now also uses
sustained measured traffic above 30 req/s with at least 25% API CPU, gated by a 20 req/s
traffic floor to avoid choosing a fleet from idle telemetry. `make test` passes, and a
100-seed twin sweep retains fleet → replica → cache for all seeds: fleet decisions 1:35–2:00,
100% modeled worst five-minute journey availability.

On fresh private VMs (`6b003880139ebf9a02c2cae3`), the revised ops image passed the first
automatic-start seed-1003 run: fleet at 2:00, replica at 6:20, cache at 11:10, all three
verified by 12:55. The former 3:00 failure window had 22 ms read p95 and 100% availability.
Across 442,242 requests there was one isolated error, 17/17 compliant latency windows,
100% worst full five-minute availability, and no correctness violations. The finished report,
Canvas, move records, and both 1.5-core database quotas passed inspection.

The first automatic repeat on that image selected the replica at 8:10, 110 s later than the
standalone run on the same VMs. Its primary database was cooler, so the CPU-based `db-read`
signal did not sustain until 7:25. The gate was stopped: even with healthy latency at 8:00,
the late replica left less time to place the cache before the flash-sale plateau. A second
measured lead signal now arms `db-read` after 45 s above 280 req/s when reads are the majority
and no replica exists. The twin's old rule that a replica must precede a 0.3 repeat ratio was
replaced with the relevant read-majority condition. `make test` and a 100-seed twin sweep
passed; all 100 seeds took the three moves and met the validator, with replica decisions
between 5:15 and 6:15 and 100% modeled worst five-minute availability.

The rebuilt ops image passed a new automatic-start seed-1003 live run: fleet at 2:05,
replica at 6:25, cache at 11:15, all verified by 12:55. The judge counted 442,243 requests,
zero errors, 17/17 compliant latency windows, 100% worst full five-minute journey
availability, and zero correctness violations. The 3:00 and 12:00 read p95s were 21 and
12 ms. The finished report, Canvas, move records, and both 1.5-core database quotas passed
inspection. The final ten-run automatic-start gate passed on the same rebuilt image:
10/10 fleet → replica → cache paths, 10/10 full MVP SLO passes, 4,422,391 requests with
zero errors, 170/170 compliant latency minutes, 100% worst full five-minute journey
availability, zero correctness and move-window errors, and 5/5/5-second decision spreads.
The final run's report, Canvas, move records, and both 1.5-core quotas passed inspection.

A separate seed-727274 trial on the final image, started explicitly after a clean reset,
passed with 466,837 requests, zero errors, 17/17 compliant latency minutes, 100% worst full
five-minute availability, and no correctness violations. Fleet, replica, and cache were
chosen at 1:58, 5:53, and 10:53 and all verified by 12:33. The previously troublesome 11:00
read p95 was 14 ms. The finished report, Canvas, move records, and both 1.5-core quotas
passed inspection.

On that final image, a seed-1003 control held every move by 90 s. The fleet was chosen at
3:38, the replica at 9:03, and the cache at 14:43 (verified after the 15-minute target).
Its 5:00 p95 reached 982 ms, so only 16/17 latency minutes complied and the MVP gate failed
as the timing control should. It served 442,198 requests with zero errors; the contrast is
latency caused by late adaptation, not a failed deployment.

The final-image seed-1003 architect-disabled control made no moves. API pressure would have
chosen a fleet at 1:58, but the unchanged genesis service reached 913 ms p95 at 4:00 and
1,979 ms p95 with 14.5% one-minute availability at 5:00. Availability fell to 0% from
6:00 onward. Across 425,105 requests, 395,169 failed; the judge found 0% worst full
five-minute availability and only 3/17 compliant latency minutes. Together the controls
show the workload needs adaptation and that its timing matters on the final image. The
private session was destroyed after the checks.

## Still to measure on the playground

The plan's spike checklist stands. In particular: the same ramp on a real 2-vCPU `node-01` with
`CPUQuota=100%` for the API and `CPUQuota=150%` for PostgreSQL; `ops-01` holding the crowd at
1,200 req/s; and the systemd-to-fleet handover at 250 req/s.
