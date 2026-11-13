"""The versioned runtime config document that the architect serves from ops-01.

Each instance polls it every two seconds with an ETag, keeps the last good version when ops-01
cannot be reached, and exports the version it is running, so the architect can wait for every
instance to converge before its next step (a convergence barrier).
"""

import asyncio
import copy
import logging
import urllib.error
import urllib.request
from collections.abc import Callable

import orjson

from . import metrics, settings

log = logging.getLogger("gp.config")

DEFAULTS: dict = {
    "version": 0,
    # replica_pct of eligible reads go to the read pool: every replica, plus the primary at
    # primary_weight (relative to one replica), so read load balances across instances.
    "read_routing": {"replica_pct": 0, "replicas": [], "primary_weight": 0.0, "max_lag_seconds": 1.0},
    "cache": {"read_pct": 0, "address": "", "detail_ttl": 60, "listing_ttl": 10, "timeout_ms": 20},
    "search": {"backend": "postgres"},
    "media": {"backend": "postgres"},
    "outbox": {"enabled": False},
    "detail_query": "per_item",
}


def merge(base: dict, update: dict) -> dict:
    """Overlay a document on the defaults, so a missing key always has a value."""
    out = copy.deepcopy(base)
    for key, value in update.items():
        if isinstance(value, dict) and isinstance(out.get(key), dict):
            out[key] = merge(out[key], value)
        else:
            out[key] = value
    return out


class RuntimeConfig:
    def __init__(self, url: str = settings.CONFIG_URL):
        self.url = url
        self.doc = copy.deepcopy(DEFAULTS)
        self.etag: str | None = None
        self.listeners: list[Callable[[dict], object]] = []
        metrics.CONFIG_VERSION.set(0)

    def __getitem__(self, key):
        return self.doc[key]

    @property
    def version(self) -> int:
        return int(self.doc.get("version", 0))

    def on_change(self, listener: Callable[[dict], object]) -> None:
        self.listeners.append(listener)

    def _fetch(self) -> tuple[int, bytes, str | None]:
        request = urllib.request.Request(self.url, headers={"Accept": "application/json"})
        if self.etag:
            request.add_header("If-None-Match", self.etag)
        try:
            with urllib.request.urlopen(request, timeout=1.5) as response:
                return response.status, response.read(), response.headers.get("ETag")
        except urllib.error.HTTPError as exc:
            if exc.code == 304:
                return 304, b"", self.etag
            raise

    async def apply(self, doc: dict) -> None:
        self.doc = merge(DEFAULTS, doc)
        metrics.CONFIG_VERSION.set(self.version)
        for listener in self.listeners:
            result = listener(self.doc)
            if asyncio.iscoroutine(result):
                await result

    async def poll_once(self) -> None:
        status, body, etag = await asyncio.to_thread(self._fetch)
        if status == 304:
            return
        doc = orjson.loads(body)
        if not isinstance(doc, dict) or "version" not in doc:
            raise ValueError("config document has no version")
        self.etag = etag
        if int(doc["version"]) != self.version:
            log.info("runtime config version %s", doc["version"])
            await self.apply(doc)

    async def run(self) -> None:
        if not self.url:
            return
        while True:
            try:
                await self.poll_once()
                metrics.CONFIG_ERRORS.labels("none")
            except Exception as exc:  # keep the last good version
                metrics.CONFIG_ERRORS.labels(type(exc).__name__).inc()
            await asyncio.sleep(settings.CONFIG_POLL_SECONDS)
