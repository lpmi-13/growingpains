# Where the build departs from the plan, and why

The plan is a discussion draft. Building it surfaced places where its numbers or mechanisms
conflict with each other, or where a measurement changed a proposal. Each entry says what the
plan says, what the build does, and the evidence. Update this file whenever the build takes a
position the plan did not.

## Traffic and calibration

| Plan | Build | Why |
| --- | --- | --- |
| Chapter 1 grows "20 → 320 req/s, roughly exponential", with the process past 75% "near 190 req/s, around 3:00" | Chapter 1 follows a logistic (word-of-mouth) S-curve reaching ~190–200 req/s at 3:00 | A pure exponential from 20 to 320 over five minutes reaches 190 only at ~4:03, and doubles every ~75 s at the end, faster than any move can finish. The plan's own timings describe growth that is fast early and slower late. |
| Listings: `GET /products?category&sort&page` | Listings also take an optional price range (`min_price`, `max_price`) | Category × sort × page is a small key space, so listing views repeated (repeat ratio ~0.6) even when browsing was meant to be a long tail. Price ranges are how faceted browsing really spreads keys, and the plan already calls listings "filtered". |
| Every listing aggregates across the category | Only "sort by rating" aggregates the whole category; newest and price sorts page first | At ~21 ms of database time per listing the database saturated long before the API, so `api-compute` could never be the first pressure. See [calibration.md](calibration.md). |
| Genesis has no index beyond keys | Genesis has `(category_id, price_cents)` | A startup adds the index its main page needs. Facets went from ~2 ms to an index-only scan. |
| Search is part of every storyline's mix | "Word gets out" sends search on 0.2% of shopper steps | A naive `ILIKE` costs 100–330 ms; at 2% it was half of all database time. Search is the seed of pain for the search storylines. |
| Flash sale: "half of all detail and listing views go to 20 sale products and the sale category" | 55% of detail and listing views, with a 90 s ramp; hot listings are mostly "top deals" (rating-sorted) pages | At 50% the repeat ratio sat at ~0.6, the cache's boundary. A flash sale starts abruptly, and the plan requires its hot pages to be expensive aggregates. |
| "Telemetry: a key-repetition sketch for cacheable reads, a per-client top-k counter, and the writes-per-seller distribution" in the API | Computed by the node agent on `node-01` from nginx's request log | Each API pod sees only its share of keys, so per-pod sketches cannot be merged in Prometheus. nginx is the single front door for the whole run, so the edge sees every key exactly. |

## Capacity and the ledger

| Plan | Build | Why |
| --- | --- | --- |
| "The sum of all caps on a node must stay below its physical CPU" | Genesis `node-01` is overcommitted (API 1.0 + PostgreSQL 1.5 + nginx and agent ≈ 2.8 cores on 2 vCPU); the rule applies to everything the architect adds | The plan's own genesis puts these caps on one 2-vCPU VM. A young product's single box running everything is the point; it is also why "scale up first" loses in the first decision record. |
| API pod: request 700m, limit 1000m; HPA 2–6 | Request 600m, limit 1000m | Six pods at 700m (4.2 cores) do not fit on `node-02` and `node-03` (3.8 allocatable). Three 600m pods per node do. |
| Move 2 routes eligible reads "10% → 50% → 100%" to the replica | At 100% the primary stays in the read pool at half a replica's weight | Move 3 triggers at 70% of the primary's and replica's *combined* read capacity. With every eligible read on the replica, the replica passes its cap long before then while the primary idles with writes and token-carrying reads. |
| `db-read` triggers on `db.cpu_ratio` | `db-read` watches the primary before a replica exists and combined read capacity after it, with a forecast | The plan uses one condition for Move 2 and another for Move 3. |

## The architect

| Plan | Build | Why |
| --- | --- | --- |
| Pressures arm on current values; forecasting (principle 7) is for slow components such as a search index | `api-compute` and `db-read` also arm on a two-minute linear forecast | The fleet and the replica take about two minutes to relieve, and traffic can double in that time. Without lead time the twin showed the single process failing requests before the fleet took traffic. |
| "Before each step, check SLO burn and the fleet's own error rate" | The fleet's guard is the fleet's own 5xx share (`api.fleet_error_ratio`), measured per upstream at the edge | Errors from the overload the move exists to cure tripped a global-error guard, which paused the move and rolled it back in the twin. |
| Error budget: 0.5% of the run's requests | 0.5% of the run's *projected* requests: so far, plus the current rate for the rest of a nominal 15 minutes | Counting only requests so far made a handful of early errors look like a spent budget, which blocked every later move. The plan's own example budgets "about 2,000 failed requests" for the whole run. |
| Verification compares measured relief with the prediction | CPU-ratio targets are scaled by traffic growth before comparing | The replica "under-delivered" only because traffic grew 34% between the decision and verification. |
| Step state persisted in SQLite | Persisted in one JSON file replaced atomically, plus the event stream | One writer and a small state; the same restart guarantee without a database dependency. Revisit if the run report needs queries. |
