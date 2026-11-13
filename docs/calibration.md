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

## Still to measure on the playground

The plan's spike checklist stands. In particular: the same ramp on a real 2-vCPU `node-01` with
`CPUQuota=100%` for the API and `CPUQuota=150%` for PostgreSQL; `ops-01` holding the crowd at
1,200 req/s; and the systemd-to-fleet handover at 250 req/s.
