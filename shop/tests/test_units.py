import asyncio
import time

from gp_shop import pages, runtime_config
from gp_shop.app import CpuTimed
from gp_shop.cache import Breaker
from gp_shop.db import int_to_lsn, lsn_to_int


def test_lsn_round_trip():
    for text in ["0/0", "0/16B3748", "1A/FFFFFFFF"]:
        assert int_to_lsn(lsn_to_int(text)) == text
    assert lsn_to_int("1/0") > lsn_to_int("0/FFFFFFFF")


def test_config_merge_keeps_defaults():
    doc = runtime_config.merge(runtime_config.DEFAULTS, {"version": 3, "read_routing": {"replica_pct": 50}})
    assert doc["version"] == 3
    assert doc["read_routing"]["replica_pct"] == 50
    assert doc["read_routing"]["max_lag_seconds"] == 1.0
    assert doc["cache"]["read_pct"] == 0
    assert runtime_config.DEFAULTS["read_routing"]["replica_pct"] == 0, "merge must not change the defaults"


def candidate(pid, title, price=1000, blurb=""):
    return {"seller_id": 1, "product_id": pid, "title": title, "price_cents": price, "thumbnail_key": None, "blurb": blurb}


def test_related_prefers_shared_words_and_is_deterministic():
    product = {"title": "Rustic Oak Cutting Board", "description": "A rustic oak cutting board.", "price_cents": 1000}
    cands = [candidate(i, f"Glossy Brass Lamp {i}") for i in range(10)] + [candidate(99, "Rustic Oak Serving Board")]
    ranked = pages.rank_related(product, cands)
    assert ranked[0]["id"] == 99
    assert ranked == pages.rank_related(product, list(reversed(cands)))


def test_breaker_opens_and_half_opens():
    b = Breaker(failures=2, cooldown=0.05)
    assert b.allow()
    b.failure(); b.failure()
    assert not b.allow()
    time.sleep(0.06)
    assert b.allow()
    b.success()
    assert b.allow()


def test_cpu_timed_counts_only_its_own_steps():
    async def busy_then_wait():
        end = time.thread_time() + 0.02
        while time.thread_time() < end:
            pass
        await asyncio.sleep(0.05)
        return "done"

    async def main():
        timed = CpuTimed(busy_then_wait())
        assert await timed == "done"
        return timed.cpu

    cpu = asyncio.run(main())
    assert 0.015 < cpu < 0.04, cpu
