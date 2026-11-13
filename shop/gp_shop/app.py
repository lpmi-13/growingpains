"""The shop's HTTP API: one Starlette app, served by one uvicorn process with one worker."""

import asyncio
import contextlib
import logging
import time

import orjson
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest
from psycopg_pool import PoolTimeout
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import HTMLResponse, Response
from starlette.routing import Route

from . import metrics, pages, settings, sql, writes
from .cache import Cache
from .db import CURRENT, Database, RequestStats, lsn_to_int, int_to_lsn, stats
from .runtime_config import RuntimeConfig

log = logging.getLogger("gp.shop")
IMPL = settings.IMPL

db = Database()
cache = Cache()
config = RuntimeConfig()
state = {"ready": False}


# --- Instrumentation ---

class CpuTimed:
    """Await a coroutine while adding up the CPU it uses between suspensions.

    In one event loop, a request's CPU is the time spent running its own coroutine steps, so
    this attributes CPU to routes without counting the other requests interleaved with it.
    """

    def __init__(self, coro):
        self.coro = coro
        self.cpu = 0.0

    def __await__(self):
        coro, value, error = self.coro, None, None
        while True:
            started = time.thread_time()
            try:
                future = coro.throw(error) if error is not None else coro.send(value)
            except StopIteration as stop:
                self.cpu += time.thread_time() - started
                return stop.value
            except BaseException:
                self.cpu += time.thread_time() - started
                raise
            self.cpu += time.thread_time() - started
            try:
                value, error = (yield future), None
            except BaseException as exc:  # delivered into the coroutine on the next step
                value, error = None, exc


class Instrument:
    def __init__(self, app):
        self.app = app

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)
        for name, value in scope["headers"]:
            if name == b"x-request-start":
                try:
                    started = float(value.decode().removeprefix("t="))
                    metrics.QUEUE_TIME.labels(IMPL).observe(max(0.0, time.time() - started))
                except ValueError:
                    pass
                break
        request_stats = RequestStats(route="other")
        reset = CURRENT.set(request_stats)
        status = [500]

        async def send_with_status(message):
            if message["type"] == "http.response.start":
                status[0] = message["status"]
            await send(message)

        timed = CpuTimed(self.app(scope, receive, send_with_status))
        began = time.perf_counter()
        metrics.INFLIGHT.inc()
        try:
            await timed
        finally:
            metrics.INFLIGHT.dec()
            CURRENT.reset(reset)
            route = request_stats.route
            if route != "meta":
                metrics.REQUESTS.labels(route, scope["method"], str(status[0]), IMPL).inc()
                metrics.DURATION.labels(route, IMPL).observe(time.perf_counter() - began)
                metrics.REQUEST_CPU.labels(route, IMPL).inc(timed.cpu)
                metrics.ROWS.labels(route, IMPL).inc(request_stats.rows)
                metrics.SPAN_SECONDS.labels(route, "db", IMPL).inc(request_stats.db_seconds)
                for span, seconds in request_stats.spans.items():
                    if span.endswith("_cpu"):
                        metrics.SPAN_CPU.labels(span.removesuffix("_cpu"), IMPL).inc(seconds)
                    else:
                        metrics.SPAN_SECONDS.labels(route, span, IMPL).inc(seconds)


# --- Helpers ---

def wants_json(request: Request) -> bool:
    return "application/json" in request.headers.get("accept", "") or request.query_params.get("format") == "json"


def read_token(request: Request) -> int | None:
    raw = request.cookies.get("gp_lsn")
    if not raw:
        return None
    try:
        return lsn_to_int(raw)
    except ValueError:
        return None


def json_response(status: int, body: dict, headers: dict | None = None) -> Response:
    return Response(orjson.dumps(body), status_code=status, media_type="application/json", headers=headers)


def page_response(request: Request, data: dict, render, headers: dict) -> Response:
    if wants_json(request):
        return json_response(200, data, headers)
    started = time.thread_time()
    html = render(data)
    stats().span("render_cpu", time.thread_time() - started)
    return HTMLResponse(html, headers=headers)


async def read_through(route: str, key: str, kind: str, token: int | None, fill) -> tuple[dict, str]:
    if cache.use_for(token):
        return await cache.get_or_fill(route, key, kind, fill), "cache"
    metrics.CACHE.labels(route, "bypass", IMPL).inc()
    return await fill(), "bypass"


def base_headers(route: str) -> dict:
    return {"X-GP-Route": route, "X-GP-Role": stats().role}


def with_token(response: Response, lsn: int, previous: int | None) -> Response:
    lsn = max(lsn, previous or 0)
    response.set_cookie("gp_lsn", int_to_lsn(lsn), max_age=settings.TOKEN_MAX_AGE, httponly=True, samesite="lax")
    return response


def product_ids(request: Request) -> tuple[int, int]:
    product = int(request.path_params["product_id"])
    return product // 1_000_000, product


# --- Reads ---

async def home(request: Request) -> Response:
    stats().route = "home"
    token = read_token(request)

    async def fill():
        async with db.read(True, token) as conn:
            return await pages.home_data(conn)

    data, _ = await read_through("home", "home", "listing", token, fill)
    return page_response(request, data, pages.render_home, base_headers("home"))


async def listing(request: Request) -> Response:
    stats().route = "listing"
    q = request.query_params
    raw = q.get("category", "")
    category = pages.Categories.by_slug.get(raw) or (pages.Categories.by_id.get(int(raw)) if raw.isdigit() else None)
    if category is None:
        return json_response(404, {"error": "no_such_category"}, base_headers("listing"))
    sort = q.get("sort", "newest")
    if sort not in sql.SORTS:
        sort = "newest"
    try:
        page = min(max(int(q.get("page", 1)), 1), settings.MAX_LISTING_PAGE)
    except ValueError:
        page = 1
    lo, hi = _optional_int(q.get("min_price")), _optional_int(q.get("max_price"))
    token = read_token(request)
    cid = category["category_id"]

    async def fill():
        async with db.read(True, token) as conn:
            return await pages.listing_data(conn, cid, sort, page, lo, hi)

    data, _ = await read_through("listing", f"listing:{cid}:{sort}:{page}:{lo}:{hi}", "listing", token, fill)
    return page_response(request, data, pages.render_listing, base_headers("listing"))


async def detail(request: Request) -> Response:
    stats().route = "detail"
    seller, product = product_ids(request)
    token = read_token(request)
    mode = config["detail_query"]

    async def fill():
        async with db.read(True, token) as conn:
            return await pages.detail_data(conn, seller, product, mode) or {"missing": True}

    data, _ = await read_through("detail", f"detail:{product}", "detail", token, fill)
    headers = base_headers("detail") | {"X-GP-Seller": str(seller)}
    if data.get("missing"):
        return json_response(404, {"error": "no_such_product"}, headers)
    return page_response(request, data, pages.render_detail, headers)


def _optional_int(value: str | None) -> int | None:
    try:
        return int(value) if value not in (None, "") else None
    except ValueError:
        return None


async def search(request: Request) -> Response:
    stats().route = "search"
    q = request.query_params
    text = q.get("q", "")[:100]
    category = q.get("category")
    filters = {
        "category": (pages.Categories.by_slug.get(category) or {}).get("category_id") if category else None,
        "min_price": _optional_int(q.get("min_price")),
        "max_price": _optional_int(q.get("max_price")),
    }
    token = read_token(request)
    async with db.read(True, token) as conn:
        data = await pages.search_data(conn, text, filters)
    headers = base_headers("search") | {"X-GP-Results": str(data["count"])}
    return page_response(request, data, pages.render_search, headers)


async def media(request: Request) -> Response:
    stats().route = "media"
    key = request.path_params["key"]
    try:
        seller = int(key[1:].split("-")[0]) // 1_000_000
    except ValueError:
        return json_response(404, {"error": "no_such_media"})
    async with db.read(True, read_token(request)) as conn:
        row = await conn.one(sql.MEDIA, {"seller": seller, "key": key})
    if row is None:
        return json_response(404, {"error": "no_such_media"}, base_headers("media"))
    return Response(bytes(row["bytes"]), media_type=row["content_type"],
                    headers=base_headers("media") | {"Cache-Control": "public, max-age=3600"})


# --- Writes ---

async def body_json(request: Request) -> dict:
    try:
        body = orjson.loads(await request.body())
    except orjson.JSONDecodeError:
        raise writes.HTTPError(400, "bad_json") from None
    if not isinstance(body, dict):
        raise writes.HTTPError(400, "bad_json")
    return body


async def write(request: Request, route: str, seller: int | None, work) -> Response:
    stats().route = route
    token = read_token(request)
    headers = {"X-GP-Route": route, "X-GP-Role": "primary"}
    try:
        status, body, lsn = await writes.idempotent(db, request.headers.get("idempotency-key"), route, work)
    except writes.HTTPError as err:
        if seller is not None:
            headers["X-GP-Seller"] = str(seller)
        return json_response(err.status, err.body, headers)
    seller = seller if seller is not None else body.get("seller_id")
    if seller is not None:
        headers["X-GP-Seller"] = str(seller)
    response = json_response(status, body, headers)
    if 200 <= status < 300:
        if route in ("patch", "variants", "delete", "review"):
            await cache.invalidate(f"detail:{body.get('id') or request.path_params.get('product_id')}")
        return with_token(response, lsn, token)
    return response


async def create(request: Request) -> Response:
    body = await body_json(request)
    return await write(request, "create", _optional_int(str(body.get("seller_id"))), lambda tx: writes.work_create(tx, body))


async def batch(request: Request) -> Response:
    body = await body_json(request)
    return await write(request, "batch", _optional_int(str(body.get("seller_id"))), lambda tx: writes.work_batch(tx, body))


async def put_variants(request: Request) -> Response:
    seller, product = product_ids(request)
    body = await body_json(request)
    return await write(request, "variants", seller, lambda tx: writes.work_variants(tx, seller, product, body))


async def patch(request: Request) -> Response:
    seller, product = product_ids(request)
    body = await body_json(request)
    return await write(request, "patch", seller, lambda tx: writes.work_patch(tx, seller, product, body))


async def delete(request: Request) -> Response:
    seller, product = product_ids(request)
    return await write(request, "delete", seller, lambda tx: writes.work_delete(tx, seller, product))


async def review(request: Request) -> Response:
    seller, product = product_ids(request)
    body = await body_json(request)
    return await write(request, "review", seller, lambda tx: writes.work_review(tx, seller, product, body))


async def order(request: Request) -> Response:
    body = await body_json(request)
    return await write(request, "order", _optional_int(str(body.get("seller_id"))), lambda tx: writes.work_order(tx, body))


async def seller_create(request: Request) -> Response:
    body = await body_json(request)
    return await write(request, "seller_create", None, lambda tx: writes.work_seller_create(tx, body))


async def seller_delete(request: Request) -> Response:
    seller = int(request.path_params["seller_id"])
    return await write(request, "seller_delete", seller, lambda tx: writes.work_seller_delete(tx, seller))


# --- Meta ---

async def metrics_endpoint(request: Request) -> Response:
    stats().route = "meta"
    return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)


async def healthz(request: Request) -> Response:
    stats().route = "meta"
    return json_response(200, {"ok": True, "config_version": config.version})


async def readyz(request: Request) -> Response:
    stats().route = "meta"
    return json_response(200 if state["ready"] else 503, {"ready": state["ready"], "config_version": config.version})


async def config_endpoint(request: Request) -> Response:
    stats().route = "meta"
    return json_response(200, config.doc)


# --- Errors and lifecycle ---

async def pool_timeout(request: Request, exc: Exception) -> Response:
    return json_response(503, {"error": "database_busy"}, {"X-GP-Route": stats().route})


async def bad_request(request: Request, exc: Exception) -> Response:
    if isinstance(exc, writes.HTTPError):
        return json_response(exc.status, exc.body, {"X-GP-Route": stats().route})
    return json_response(400, {"error": "bad_request"}, {"X-GP-Route": stats().route})


async def expire_idempotency_keys() -> None:
    while True:
        await asyncio.sleep(60)
        try:
            async with db.write() as tx:
                await tx.run(sql.IDEM_EXPIRE, {"age": settings.IDEMPOTENCY_SECONDS})
        except Exception as exc:
            log.warning("idempotency cleanup failed: %s", exc)


async def apply_config(doc: dict) -> None:
    await db.configure(doc)
    await cache.configure(doc)


@contextlib.asynccontextmanager
async def lifespan(app):
    await db.open()
    async with db.read(eligible=False) as conn:
        await pages.Categories.load(conn)
    config.on_change(apply_config)
    if config.url:
        try:
            await config.poll_once()
        except Exception as exc:
            log.warning("starting with built-in config: %s", exc)
    tasks = [asyncio.create_task(config.run()), asyncio.create_task(expire_idempotency_keys())]
    state["ready"] = True
    try:
        yield
    finally:
        state["ready"] = False
        for task in tasks:
            task.cancel()
        await db.close()


routes = [
    Route("/", home),
    Route("/products", listing, methods=["GET"]),
    Route("/products", create, methods=["POST"]),
    Route("/products/batch", batch, methods=["POST"]),
    Route("/products/{product_id:int}", detail, methods=["GET"]),
    Route("/products/{product_id:int}", patch, methods=["PATCH"]),
    Route("/products/{product_id:int}", delete, methods=["DELETE"]),
    Route("/products/{product_id:int}/variants", put_variants, methods=["PUT"]),
    Route("/products/{product_id:int}/reviews", review, methods=["POST"]),
    Route("/orders", order, methods=["POST"]),
    Route("/sellers", seller_create, methods=["POST"]),
    Route("/sellers/{seller_id:int}", seller_delete, methods=["DELETE"]),
    Route("/search", search),
    Route("/media/{key}", media),
    Route("/metrics", metrics_endpoint),
    Route("/healthz", healthz),
    Route("/readyz", readyz),
    Route("/internal/config", config_endpoint),
]

app = Instrument(
    Starlette(
        routes=routes,
        lifespan=lifespan,
        exception_handlers={PoolTimeout: pool_timeout, writes.HTTPError: bad_request, ValueError: bad_request, KeyError: bad_request},
    )
)
