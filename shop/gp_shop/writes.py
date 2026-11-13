"""Write handlers. Every write is one transaction on the primary.

Writes carry an Idempotency-Key. The key is claimed inside the write's own transaction and
stores the response, so a client that retries across a cutover gets the first answer instead of
a second write. Genesis does its side effects inside the request: thumbnails are made with
Pillow and every follower gets a notification row before the response is sent.
"""

import base64
import io
import time

import orjson

from . import metrics, sql
from .db import Conn, Database, stats


class HTTPError(Exception):
    def __init__(self, status: int, error: str, **extra):
        super().__init__(error)
        self.status = status
        self.body = {"error": error, **extra}


class Replay(Exception):
    pass


def require(body: dict, field: str, kind=int):
    value = body.get(field)
    if value is None:
        raise HTTPError(400, "missing_field", field=field)
    try:
        return kind(value)
    except (TypeError, ValueError):
        raise HTTPError(400, "bad_field", field=field) from None


async def idempotent(db: Database, key: str | None, route: str, work) -> tuple[int, dict, int]:
    """Run work(tx) once per idempotency key. Returns status, body, and the WAL token."""
    try:
        async with db.write() as tx:
            if key:
                if await tx.one(sql.IDEM_CLAIM, {"key": key, "route": route}) is None:
                    raise Replay
            status, body = await work(tx)
            if key:
                await tx.run(sql.IDEM_STORE, {"key": key, "status": status, "body": orjson.dumps(body)})
        return status, body, tx.lsn
    except Replay:
        pass
    metrics.IDEMPOTENT_REPLAYS.labels(route).inc()
    async with db.write() as tx:
        row = await tx.one(sql.IDEM_GET, {"key": key})
    if row is None or row["status"] == 0:
        return 409, {"error": "in_progress"}, tx.lsn
    return row["status"], orjson.loads(bytes(row["body"])), tx.lsn


# --- Side effects, done synchronously at genesis ---

def make_thumbnail(raw: bytes) -> bytes:
    from PIL import Image

    with Image.open(io.BytesIO(raw)) as img:
        img = img.convert("RGB")
        img.thumbnail((128, 128))
        out = io.BytesIO()
        img.save(out, "JPEG", quality=80)
        return out.getvalue()


async def store_image(tx: Conn, seller: int, product: int, image_b64: str) -> None:
    started, cpu = time.perf_counter(), time.thread_time()
    try:
        raw = base64.b64decode(image_b64, validate=True)
        thumb = make_thumbnail(raw)
    except Exception:
        raise HTTPError(400, "bad_image") from None
    stats().span("side_effect_cpu", time.thread_time() - cpu)
    key = f"p{product}-{int(time.time() * 1000) % 100000}"
    await tx.run(sql.IMAGE_INSERT, {"seller": seller, "key": key, "product": product, "kind": "original", "content_type": "image/jpeg", "bytes": raw})
    await tx.run(sql.IMAGE_INSERT, {"seller": seller, "key": key + "-t", "product": product, "kind": "thumbnail", "content_type": "image/jpeg", "bytes": thumb})
    await tx.run(sql.PRODUCT_SET_IMAGE, {"seller": seller, "product": product, "image": key, "thumb": key + "-t"})
    stats().span("side_effect", time.perf_counter() - started)


async def notify_followers(tx: Conn, seller: int, product: int, title: str) -> None:
    started = time.perf_counter()
    followers = await tx.all(sql.FOLLOWERS, {"seller": seller})
    cpu = time.thread_time()
    rows = [
        {"follower": f["follower_id"], "seller": seller, "product": product,
         "message": f"Hello #{f['follower_id']}: a shop you follow just listed “{title}”. Take a look at /products/{product}."}
        for f in followers
    ]
    stats().span("side_effect_cpu", time.thread_time() - cpu)
    await tx.many(sql.NOTIFY, rows)
    stats().span("side_effect", time.perf_counter() - started)


# --- Handlers' work functions. Each takes the transaction and returns (status, body). ---

def variant_rows(seller: int, product: int, variants: list) -> list[dict]:
    if not isinstance(variants, list) or not 1 <= len(variants) <= 9:
        raise HTTPError(400, "bad_variants")
    rows = []
    for k, v in enumerate(variants, start=1):
        vid = product * 10 + k
        rows.append({
            "seller": seller, "variant": vid, "product": product, "sku": f"SKU-{vid}",
            "options": orjson.dumps(v.get("options") or {}).decode(),
            "price": int(v.get("price_cents") or 0), "stock": int(v.get("stock") or 0),
        })
    return rows


async def create_product(tx: Conn, seller: int, body: dict, notify: bool = True) -> dict:
    title = require(body, "title", str)
    created = await tx.one(sql.PRODUCT_CREATE, {
        "seller": seller, "category": require(body, "category_id"), "title": title,
        "description": require(body, "description", str), "price": require(body, "price_cents"),
    })
    product = created["product_id"]
    await tx.many(sql.VARIANT_INSERT, variant_rows(seller, product, body.get("variants") or [{"price_cents": body["price_cents"], "stock": 100}]))
    if body.get("image"):
        await store_image(tx, seller, product, body["image"])
    if notify:
        await notify_followers(tx, seller, product, title)
    return {"id": product, "description_version": created["description_version"]}


def check_seller(tx_seller_row) -> None:
    if tx_seller_row is None:
        raise HTTPError(404, "no_such_seller")


async def work_create(tx: Conn, body: dict):
    seller = require(body, "seller_id")
    check_seller(await tx.one(sql.SELLER_EXISTS, {"seller": seller}))
    return 201, await create_product(tx, seller, body)


async def work_batch(tx: Conn, body: dict):
    seller = require(body, "seller_id")
    products = body.get("products")
    if not isinstance(products, list) or not 1 <= len(products) <= 100:
        raise HTTPError(400, "bad_batch")
    check_seller(await tx.one(sql.SELLER_EXISTS, {"seller": seller}))
    ids = [(await create_product(tx, seller, p, notify=False))["id"] for p in products]
    return 201, {"ids": ids}


async def work_variants(tx: Conn, seller: int, product: int, body: dict):
    rows = variant_rows(seller, product, body.get("variants"))
    touched = await tx.one(sql.PRODUCT_TOUCH_PRICE, {"seller": seller, "product": product, "price": min(r["price"] for r in rows)})
    if touched is None:
        raise HTTPError(404, "no_such_product")
    await tx.run(sql.VARIANTS_DELETE, {"seller": seller, "product": product})
    await tx.many(sql.VARIANT_INSERT, rows)
    return 200, {"id": product, "variants": len(rows), "description_version": touched["description_version"]}


async def work_patch(tx: Conn, seller: int, product: int, body: dict):
    params = {
        "seller": seller, "product": product,
        "description": body.get("description"), "price": body.get("price_cents"), "title": body.get("title"),
    }
    row = await tx.one(sql.PRODUCT_PATCH, params)
    if row is None:
        raise HTTPError(404, "no_such_product")
    for s in body.get("stock") or []:
        await tx.run(sql.VARIANT_SET_STOCK, {"seller": seller, "variant": int(s["variant_id"]), "stock": int(s["stock"])})
    return 200, {"id": product, "description_version": row["description_version"], "price_cents": row["price_cents"]}


async def work_delete(tx: Conn, seller: int, product: int):
    if await tx.run(sql.PRODUCT_DELETE, {"seller": seller, "product": product}) == 0:
        raise HTTPError(404, "no_such_product")
    return 200, {"id": product, "deleted": True}


async def work_review(tx: Conn, seller: int, product: int, body: dict):
    rating = require(body, "rating")
    if not 1 <= rating <= 5:
        raise HTTPError(400, "bad_field", field="rating")
    if await tx.one(sql.PRODUCT, {"seller": seller, "product": product}) is None:
        raise HTTPError(404, "no_such_product")
    row = await tx.one(sql.REVIEW_CREATE, {
        "seller": seller, "product": product, "user": require(body, "user_id"),
        "rating": rating, "body": str(body.get("body") or "")[:2000],
    })
    return 201, {"review_id": row["review_id"]}


async def work_order(tx: Conn, body: dict):
    seller = require(body, "seller_id")
    lines = body.get("lines")
    if not isinstance(lines, list) or not 1 <= len(lines) <= 50:
        raise HTTPError(400, "bad_lines")
    merged: dict[int, int] = {}
    for line in lines:
        variant, qty = int(line["variant_id"]), int(line["quantity"])
        if qty < 1 or variant // 10_000_000 != seller:
            raise HTTPError(400, "bad_line", variant_id=variant)
        merged[variant] = merged.get(variant, 0) + qty
    total, priced = 0, []
    for variant in sorted(merged):  # one lock order for every transaction
        qty = merged[variant]
        row = await tx.one(sql.STOCK_TAKE, {"seller": seller, "variant": variant, "qty": qty})
        if row is None:
            if await tx.one(sql.VARIANT_PRESENT, {"seller": seller, "variant": variant}) is None:
                raise HTTPError(404, "no_such_variant", variant_id=variant)
            raise HTTPError(409, "out_of_stock", variant_id=variant)
        total += row["price_cents"] * qty
        priced.append((variant, qty, row["price_cents"]))
    order = (await tx.one(sql.ORDER_CREATE, {"seller": seller, "buyer": require(body, "buyer_id"), "total": total, "lines": len(priced)}))["order_id"]
    await tx.many(sql.ORDER_ITEM, [
        {"seller": seller, "order": order, "line": i, "variant": v, "qty": q, "price": p}
        for i, (v, q, p) in enumerate(priced, start=1)
    ])
    await tx.many(sql.RESERVATION, [{"seller": seller, "order": order, "variant": v, "qty": q} for v, q, _ in priced])
    return 201, {"order_id": order, "total_cents": total, "lines": len(priced)}


async def work_seller_create(tx: Conn, body: dict):
    row = await tx.one(sql.SELLER_CREATE, {"name": str(body.get("name") or "New shop")[:200]})
    return 201, {"seller_id": row["seller_id"]}


async def work_seller_delete(tx: Conn, seller: int):
    if await tx.run(sql.SELLER_DELETE, {"seller": seller}) == 0:
        raise HTTPError(404, "no_such_seller")
    return 200, {"seller_id": seller, "deleted": True}
