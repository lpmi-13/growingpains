"""Cache-aside in Valkey, switched on by runtime config.

Cache calls time out after 20 ms and fail open behind a circuit breaker, so a cache outage only
slows things down. Each instance coalesces concurrent misses for one key into one fill
(single-flight). Detail keys are deleted when written, then deleted again after the replica lag
bound, so a fill from a lagging replica cannot outlive the write.
"""

import asyncio
import logging
import random
import time
from collections.abc import Awaitable, Callable

import orjson

from . import metrics, settings

log = logging.getLogger("gp.cache")
IMPL = settings.IMPL


class Breaker:
    def __init__(self, failures: int = 5, cooldown: float = 5.0):
        self.threshold = failures
        self.cooldown = cooldown
        self.failures = 0
        self.opened_at = 0.0

    def allow(self) -> bool:
        if self.failures < self.threshold:
            return True
        # Half-open: let one call through after the cooldown.
        return time.monotonic() - self.opened_at >= self.cooldown

    def success(self) -> None:
        self.failures = 0

    def failure(self) -> None:
        self.failures += 1
        if self.failures >= self.threshold:
            self.opened_at = time.monotonic()


class Cache:
    def __init__(self):
        self.client = None
        self.address = ""
        self.read_pct = 0.0
        self.timeout = 0.02
        self.detail_ttl = 60
        self.listing_ttl = 10
        self.lag_bound = 1.0
        self.breaker = Breaker()
        self.inflight: dict[str, asyncio.Future] = {}

    async def configure(self, doc: dict) -> None:
        cfg = doc["cache"]
        self.read_pct = float(cfg["read_pct"])
        self.timeout = float(cfg["timeout_ms"]) / 1000
        self.detail_ttl = int(cfg["detail_ttl"])
        self.listing_ttl = int(cfg["listing_ttl"])
        self.lag_bound = float(doc["read_routing"]["max_lag_seconds"])
        address = cfg["address"] or ""
        if address != self.address:
            old, self.client, self.address = self.client, None, address
            if old is not None:
                await old.aclose()
            if address:
                import valkey.asyncio as valkey

                host, _, port = address.partition(":")
                self.client = valkey.Valkey(
                    host=host, port=int(port or 6379),
                    socket_timeout=self.timeout, socket_connect_timeout=0.1,
                    max_connections=64,
                )
                self.breaker = Breaker()

    def use_for(self, token: int | None) -> bool:
        """Whether this request reads through the cache. Token-carrying reads never do."""
        return (
            self.client is not None
            and token is None
            and random.random() * 100 < self.read_pct
            and self.breaker.allow()
        )

    def ttl(self, kind: str) -> int:
        if kind == "detail":
            return max(1, int(self.detail_ttl * random.uniform(0.9, 1.1)))
        return self.listing_ttl

    async def _get(self, key: str):
        return await asyncio.wait_for(self.client.get(key), self.timeout)

    async def get_or_fill(self, route: str, key: str, kind: str, fill: Callable[[], Awaitable[dict]]) -> dict:
        started = time.perf_counter()
        try:
            raw = await self._get(key)
            self.breaker.success()
        except Exception:
            self.breaker.failure()
            metrics.CACHE.labels(route, "error", IMPL).inc()
            return await fill()
        finally:
            from .db import stats

            stats().span("cache", time.perf_counter() - started)
        if raw is not None:
            metrics.CACHE.labels(route, "hit", IMPL).inc()
            return orjson.loads(raw)
        metrics.CACHE.labels(route, "miss", IMPL).inc()
        pending = self.inflight.get(key)
        if pending is not None:
            return await asyncio.shield(pending)
        future = asyncio.get_running_loop().create_future()
        self.inflight[key] = future
        try:
            value = await fill()
            future.set_result(value)
        except BaseException as exc:
            future.set_exception(exc)
            # Nobody else may be waiting; retrieve the exception so it is not logged as lost.
            future.exception()
            raise
        finally:
            self.inflight.pop(key, None)
        asyncio.get_running_loop().create_task(self._set(key, value, self.ttl(kind)))
        return value

    async def _set(self, key: str, value: dict, ttl: int) -> None:
        try:
            await asyncio.wait_for(self.client.set(key, orjson.dumps(value), ex=ttl), self.timeout)
        except Exception:
            self.breaker.failure()

    async def invalidate(self, key: str) -> None:
        if self.client is None:
            return
        await self._delete(key)
        asyncio.get_running_loop().create_task(self._delete_later(key))

    async def _delete(self, key: str) -> None:
        try:
            await asyncio.wait_for(self.client.delete(key), self.timeout)
        except Exception:
            self.breaker.failure()

    async def _delete_later(self, key: str) -> None:
        await asyncio.sleep(self.lag_bound)
        await self._delete(key)
