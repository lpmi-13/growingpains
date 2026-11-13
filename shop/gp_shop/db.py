"""Database access: the primary, any replicas, read routing, and read-your-writes tokens.

After a write, the API hands the client the primary's WAL position in a short-lived cookie. A
read carrying that token goes to a replica only if the replica has replayed past it, and the
replica's position is checked at most every 100 ms. Lag over the configured bound sends every
read back to the primary (a lag circuit breaker). Reads with tokens also bypass caches.
"""

import asyncio
import contextvars
import logging
import random
import time
from collections import deque
from contextlib import asynccontextmanager
from dataclasses import dataclass, field

from psycopg.rows import dict_row
from psycopg_pool import AsyncConnectionPool

from . import metrics, settings

log = logging.getLogger("gp.db")
IMPL = settings.IMPL


def lsn_to_int(text: str) -> int:
    hi, lo = text.split("/")
    return (int(hi, 16) << 32) | int(lo, 16)


def int_to_lsn(value: int) -> str:
    return f"{value >> 32:X}/{value & 0xFFFFFFFF:X}"


@dataclass
class RequestStats:
    route: str = "unknown"
    statements: int = 0
    rows: int = 0
    db_seconds: float = 0.0
    spans: dict = field(default_factory=dict)
    role: str = "primary"

    def span(self, name: str, seconds: float) -> None:
        self.spans[name] = self.spans.get(name, 0.0) + seconds


CURRENT: contextvars.ContextVar[RequestStats | None] = contextvars.ContextVar("gp_request", default=None)


def stats() -> RequestStats:
    current = CURRENT.get()
    if current is None:
        current = RequestStats()
        CURRENT.set(current)
    return current


class Conn:
    """A connection that counts every statement against the current request."""

    def __init__(self, conn, role: str):
        self.conn = conn
        self.role = role

    def _account(self, started: float, rows: int) -> None:
        s = stats()
        s.statements += 1
        s.rows += rows
        s.db_seconds += time.perf_counter() - started
        metrics.STATEMENTS.labels(s.route, self.role, IMPL).inc()

    async def all(self, sql: str, params=()) -> list[dict]:
        started = time.perf_counter()
        cur = await self.conn.execute(sql, params)
        rows = await cur.fetchall()
        self._account(started, len(rows))
        return rows

    async def one(self, sql: str, params=()) -> dict | None:
        rows = await self.all(sql, params)
        return rows[0] if rows else None

    async def run(self, sql: str, params=()) -> int:
        started = time.perf_counter()
        cur = await self.conn.execute(sql, params)
        self._account(started, max(cur.rowcount, 0))
        return cur.rowcount

    async def many(self, sql: str, seq) -> None:
        seq = list(seq)
        if not seq:
            return
        started = time.perf_counter()
        async with self.conn.cursor() as cur:
            await cur.executemany(sql, seq)
        self._account(started, len(seq))


def _pool(dsn: str, name: str) -> AsyncConnectionPool:
    return AsyncConnectionPool(
        dsn,
        min_size=1,
        max_size=settings.POOL_SIZE,
        name=name,
        open=False,
        timeout=2.0,
        kwargs={"autocommit": True, "prepare_threshold": 5, "row_factory": dict_row},
    )


class Replica:
    def __init__(self, dsn: str):
        self.dsn = dsn
        self.pool = _pool(dsn, "replica-" + dsn.split("@")[-1])
        self.replay_lsn = 0
        self.lag = float("inf")
        self.checked_at = 0.0
        self.refreshing = False

    async def open(self) -> None:
        await self.pool.open(wait=False)

    async def close(self) -> None:
        await self.pool.close()


class Database:
    def __init__(self, dsn: str = settings.DATABASE_URL):
        self.primary = _pool(dsn, "primary")
        self.replicas: dict[str, Replica] = {}
        self.routing = {"replica_pct": 0, "max_lag_seconds": 1.0, "primary_weight": 0.0}
        # (time, primary WAL position) samples, to turn a replica's position into seconds of lag.
        self.primary_positions: deque[tuple[float, int]] = deque(maxlen=200)

    async def open(self) -> None:
        await self.primary.open(wait=True, timeout=30)

    async def close(self) -> None:
        for replica in self.replicas.values():
            await replica.close()
        await self.primary.close()

    async def configure(self, doc: dict) -> None:
        """Follow the runtime config: open new replica pools and close removed ones."""
        routing = doc["read_routing"]
        self.routing = {
            "replica_pct": float(routing["replica_pct"]),
            "max_lag_seconds": float(routing["max_lag_seconds"]),
            "primary_weight": float(routing.get("primary_weight") or 0.0),
        }
        wanted = list(routing.get("replicas") or [])
        for dsn in wanted:
            if dsn not in self.replicas:
                replica = Replica(dsn)
                await replica.open()
                self.replicas[dsn] = replica
        for dsn in list(self.replicas):
            if dsn not in wanted:
                replica = self.replicas.pop(dsn)
                metrics.REPLICA_LAG.remove(replica.dsn.split("@")[-1])
                await replica.close()

    async def _refresh(self, replica: Replica) -> None:
        try:
            async with self.primary.connection() as conn:
                cur = await conn.execute("SELECT /*gp:meta:wal_position*/ pg_current_wal_lsn()::text AS lsn")
                now = time.monotonic()
                self.primary_positions.append((now, lsn_to_int((await cur.fetchone())["lsn"])))
            async with replica.pool.connection(timeout=0.5) as conn:
                cur = await conn.execute("SELECT /*gp:meta:replay_position*/ pg_last_wal_replay_lsn()::text AS lsn")
                row = await cur.fetchone()
            replica.replay_lsn = lsn_to_int(row["lsn"]) if row["lsn"] else 0
            # Lag is how long ago the primary was first ahead of where the replica is now.
            behind = [t for t, pos in self.primary_positions if pos > replica.replay_lsn]
            replica.lag = (time.monotonic() - min(behind)) if behind else 0.0
        except Exception as exc:
            log.warning("replica check failed for %s: %s", replica.dsn.split("@")[-1], exc)
            replica.lag = float("inf")
        finally:
            replica.checked_at = time.monotonic()
            replica.refreshing = False
            lag = replica.lag if replica.lag != float("inf") else 3600.0
            metrics.REPLICA_LAG.labels(replica.dsn.split("@")[-1]).set(lag)

    def _maybe_refresh(self, replica: Replica) -> None:
        if not replica.refreshing and time.monotonic() - replica.checked_at >= settings.REPLICA_CHECK_SECONDS:
            replica.refreshing = True
            asyncio.get_running_loop().create_task(self._refresh(replica))

    def choose(self, eligible: bool, token: int | None) -> tuple[AsyncConnectionPool, str]:
        """Pick the pool for a read. Returns the pool and the role it plays."""
        if not eligible:
            return self.primary, "primary"
        if not self.replicas or random.random() * 100 >= self.routing["replica_pct"]:
            metrics.READ_ROUTING.labels("primary", "percentage", IMPL).inc()
            return self.primary, "primary"
        replicas = list(self.replicas.values())
        # The read pool: every replica at weight 1, and the primary at primary_weight.
        pw = self.routing["primary_weight"]
        if pw > 0 and random.random() * (len(replicas) + pw) < pw:
            metrics.READ_ROUTING.labels("primary", "pool", IMPL).inc()
            return self.primary, "primary"
        replica = random.choice(replicas)
        self._maybe_refresh(replica)
        if replica.lag > self.routing["max_lag_seconds"]:
            metrics.READ_ROUTING.labels("primary", "lag_breaker", IMPL).inc()
            return self.primary, "primary"
        if token is not None and replica.replay_lsn < token:
            metrics.READ_ROUTING.labels("primary", "token", IMPL).inc()
            return self.primary, "primary"
        metrics.READ_ROUTING.labels("replica", "routed", IMPL).inc()
        return replica.pool, "replica"

    @asynccontextmanager
    async def read(self, eligible: bool = True, token: int | None = None):
        pool, role = self.choose(eligible, token)
        stats().role = role
        async with pool.connection() as conn:
            yield Conn(conn, role)

    @asynccontextmanager
    async def write(self):
        """A transaction on the primary. Its .lsn holds the token once it has committed."""
        async with self.primary.connection() as conn:
            tx = Conn(conn, "primary")
            async with conn.transaction():
                yield tx
            row = await tx.one("SELECT /*gp:meta:wal_position*/ pg_current_wal_lsn()::text AS lsn")
            tx.lsn = lsn_to_int(row["lsn"])
