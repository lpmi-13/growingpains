#!/usr/bin/env python3
"""What a CPU quota does to the database's latency, on node-01 of a private session.

    python3 e2e/quota_bench.py <session id> [--loads 0.40 0.52] [--seconds 45]

It stops nginx and the genesis API on node-01, so the database sees only the benchmark, then
runs the shop's rating sort (a random category, unfiltered) and a search (a pattern that never
matches, so a full scan) from pgbench at fixed Poisson rates. Each load is a target in cores of
the database unit's CPU, met by the rating sort's rate with 1.2 searches a second; each runs
under the images' quota (65% with a 10 ms period), the same quota with systemd's default 100 ms
period, the quota with cpu.max.burst equal to it, and no quota. For each it prints the unit's
CPU, its throttled time and periods, and every statement's latency percentiles as a client sees
them (pgbench measures from the scheduled start, so queueing counts).

Then it puts the image's quota back, removes the runtime drop-in, and starts nginx and the API
again. One session through the platform. Run it on an idle session: the crowd's requests fail
while nginx is stopped.

First run 2026-11-17 (docs/calibration.md, "The plateau measured uncapped"). The burst case did
not run then: the case before it removed the quota, systemd then disabled the unit's cpu
controller, and cpu.max.burst was gone. It now runs before the uncapped case.
"""

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import lab  # noqa: E402

SCRIPT = r"""
UNIT=postgresql@18-main.service
CG=/sys/fs/cgroup$(systemctl show -p ControlGroup --value $UNIT)
[ -f $CG/cpu.stat ] || { echo "no cgroup at $CG" >&2; exit 1; }
PGB=/usr/lib/postgresql/18/bin/pgbench
[ -x "$PGB" ] || { echo "no pgbench at $PGB" >&2; exit 1; }

echo "kernel $(uname -r), $(nproc) CPUs, cfs bandwidth slice $(cat /proc/sys/kernel/sched_cfs_bandwidth_slice_us 2>/dev/null || echo '?') us"
echo "cpu.max now: $(cat $CG/cpu.max); cpu.max.burst: $(cat $CG/cpu.max.burst 2>/dev/null || echo 'none')"
psql() { runuser -u postgres -- /usr/bin/psql -d shop -Atq "$@"; }
psql -c "select 'reviews: ' || count(*) || ' total, ' || count(*) filter (where created_at > now() - interval '3 hours') || ' in the last 3 hours' from reviews"
ncat=$(psql -c "select max(category_id) from categories")

systemctl stop nginx gp-api
restore() {
  systemctl set-property --runtime $UNIT CPUQuota=65% CPUQuotaPeriodSec=10ms
  [ -f $CG/cpu.max.burst ] && echo 0 > $CG/cpu.max.burst || true
  rm -rf /run/systemd/system.control/$UNIT.d
  systemctl daemon-reload
  systemctl start gp-api nginx
  echo "restored: cpu.max $(cat $CG/cpu.max), nginx $(systemctl is-active nginx), gp-api $(systemctl is-active gp-api)"
}
trap restore EXIT

work=$(mktemp -d); chown postgres "$work"; cd "$work"
cat > rating.sql <<'SQL'
\set cat random(1, :ncat)
WITH rated AS (
    SELECT p.seller_id, p.product_id, p.title, p.price_cents, p.thumbnail_key, p.created_at,
           r.cnt AS review_count, coalesce(r.avg_rating, 0) AS avg_rating
    FROM products p
    LEFT JOIN LATERAL (
        SELECT count(*) AS cnt, avg(rating)::float AS avg_rating FROM reviews
        WHERE reviews.seller_id = p.seller_id AND reviews.product_id = p.product_id
    ) r ON true
    WHERE p.category_id = :cat AND p.price_cents BETWEEN 0 AND 2147483647
    ORDER BY avg_rating DESC, review_count DESC, product_id
    LIMIT 24 OFFSET 0
)
SELECT rated.*, v.min_price, v.stock FROM rated
LEFT JOIN LATERAL (
    SELECT min(price_cents) AS min_price, sum(stock) AS stock FROM variants
    WHERE variants.seller_id = rated.seller_id AND variants.product_id = rated.product_id
) v ON true
ORDER BY avg_rating DESC, review_count DESC, product_id;
SQL
cat > search.sql <<'SQL'
SELECT seller_id, product_id, category_id, title, description, price_cents, thumbnail_key
FROM products WHERE (title ILIKE '%zqxj%' OR description ILIKE '%zqxj%') LIMIT 50;
SQL
chown postgres *.sql

# With no quota systemd may drop the unit's cpu controller: cpu.stat then has usage only.
stat_of() { awk -v k="$1" '$1==k {v=$2} END {print v+0}' $CG/cpu.stat; }
setcase() {
  case $1 in
    p10)   systemctl set-property --runtime $UNIT CPUQuota=65% CPUQuotaPeriodSec=10ms ;;
    p100)  systemctl set-property --runtime $UNIT CPUQuota=65% CPUQuotaPeriodSec=100ms ;;
    burst) systemctl set-property --runtime $UNIT CPUQuota=65% CPUQuotaPeriodSec=10ms ;;
    none)  systemctl set-property --runtime $UNIT CPUQuota= ;;
  esac
  if [ -f $CG/cpu.max.burst ]; then
    if [ $1 = burst ]; then echo 6500 > $CG/cpu.max.burst; else echo 0 > $CG/cpu.max.burst; fi
  fi
}
bench() { # name rate pgbench-args...
  local name=$1 rate=$2; shift 2
  local u0 t0 p0 n0 s0 u1 t1 p1 n1 s1
  u0=$(stat_of usage_usec); t0=$(stat_of throttled_usec); p0=$(stat_of nr_periods); n0=$(stat_of nr_throttled); s0=$(date +%s.%N)
  runuser -u postgres -- $PGB -d shop -n -c 24 -j 2 -T "$DUR" -R "$rate" -D ncat=$ncat "$@" -l --log-prefix="log_$name" >/dev/null 2>"err_$name" \
    || { echo "$name: pgbench failed: $(tail -3 err_$name)"; return; }
  u1=$(stat_of usage_usec); t1=$(stat_of throttled_usec); p1=$(stat_of nr_periods); n1=$(stat_of nr_throttled); s1=$(date +%s.%N)
  awk -v n="$name" -v u=$((u1-u0)) -v t=$((t1-t0)) -v p=$((p1-p0)) -v th=$((n1-n0)) -v s0=$s0 -v s1=$s1 'BEGIN {
    w = (s1-s0)*1e6; printf "%-14s cgroup %.3f cores", n, u/w
    if (p > 0) printf ", throttled %.3f of the window (summed over CPUs), %d of %d periods throttled", t/w, th, p
    printf "\n" }'
  cat log_"$name".* | awk '{print $4, $3/1000}' | sort -k1,1n -k2,2g | awk -v n="$name" '
    { lat[$1, ++c[$1]] = $2; sum[$1] += $2 }
    END { for (s in c) { k=c[s]; printf "%-14s %-6s %6d calls, mean %8.2f ms, p50 %8.2f, p95 %8.2f, p99 %8.2f, max %8.2f\n",
          n, (s == 0 ? "rating" : "search"), k, sum[s]/k, lat[s, int(k*0.5)+1], lat[s, int(k*0.95)+1], lat[s, int(k*0.99)+1], lat[s, k] } }'
}

# Calibrate uncapped: the unit's background CPU with no queries, then each statement's CPU per call.
setcase none
u0=$(stat_of usage_usec); sleep 10; u1=$(stat_of usage_usec)
bg=$(awk -v u=$((u1-u0)) 'BEGIN {printf "%.3f", u/10e6}')
echo "calibrate background: $bg cores with no queries"
for s in rating search; do
  rate=20; [ $s = search ] && rate=2
  u0=$(stat_of usage_usec)
  runuser -u postgres -- $PGB -d shop -n -c 4 -j 2 -T 15 -R $rate -D ncat=$ncat -f $s.sql -l --log-prefix=cal_$s >/dev/null 2>err_cal_$s
  u1=$(stat_of usage_usec)
  calls=$(cat cal_$s.* | wc -l)
  cpu=$(awk -v u=$((u1-u0)) -v c=$calls -v bg=$bg 'BEGIN {printf "%.3f", (u - bg*15e6)/c/1000}')
  lat=$(awk '{s+=$3} END {printf "%.2f", s/NR/1000}' cal_$s.*)
  echo "calibrate $s: $calls calls, $cpu ms of CPU per call, mean latency $lat ms"
  eval "cpu_$s=$cpu"
done

search_rate=1.2
for target in $LOADS; do
  rating_rate=$(awk -v t=$target -v bg=$bg -v sr=$search_rate -v sc=$cpu_search -v rc=$cpu_rating 'BEGIN {printf "%.1f", (t - bg - sr*sc/1000)/(rc/1000)}')
  total=$(awk -v a=$rating_rate -v b=$search_rate 'BEGIN {printf "%.1f", a+b}')
  wr=$(awk -v a=$rating_rate 'BEGIN {printf "%d", a*10}'); ws=$(awk -v b=$search_rate 'BEGIN {printf "%d", b*10}')
  echo; echo "== load $target cores: rating $rating_rate/s, search $search_rate/s"
  for c in p10 p100 burst none; do
    [ $c = burst ] && [ ! -f $CG/cpu.max.burst ] && { echo "burst: no cpu.max.burst"; continue; }
    setcase $c
    bench "${c}_$target" "$total" -f rating.sql@$wr -f search.sql@$ws
  done
done
"""


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("session")
    p.add_argument("--loads", type=float, nargs="+", default=[0.40, 0.52],
                   help="the database unit's CPU to aim for, in cores (its quota is 0.65)")
    p.add_argument("--seconds", type=int, default=45, help="length of each case")
    args = p.parse_args()
    cases = 4 * len(args.loads)
    env = f"DUR={args.seconds}\nLOADS='{' '.join(f'{x:.2f}' for x in args.loads)}'\n"
    try:
        print(lab.ssh(args.session, "node-01", env + SCRIPT, timeout=120 + cases * (args.seconds + 15)), flush=True)
    except RuntimeError as e:
        sys.exit(str(e))
    print(f"sessions opened: {lab.connections}")


if __name__ == "__main__":
    main()
