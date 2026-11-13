"""Read pages: assembling each page's data, ranking it, and rendering it.

Requests do realistic work, as a young product's would: aggregate queries, ranking related
products and search results in Python, and server-side HTML. Browsers get HTML; clients that
ask for JSON get the same page as JSON.
"""

import datetime as dt
import math
import re
import time
from collections import Counter
from pathlib import Path

from jinja2 import Environment, FileSystemLoader, select_autoescape

from . import settings, sql
from .db import Conn, stats

TOKEN = re.compile(r"[a-z0-9]+")
PAGE = settings.LISTING_PAGE_SIZE


def tokens(text: str) -> set[str]:
    return set(TOKEN.findall(text.lower()))


def money(cents: int | None) -> str:
    if cents is None:
        return "—"
    return f"£{cents // 100:,}.{cents % 100:02d}"


def stars(rating: float | None) -> str:
    rating = rating or 0
    full = int(rating + 0.25)
    return "★" * full + "☆" * (5 - full)


def iso(value) -> str:
    return value.isoformat() if isinstance(value, (dt.datetime, dt.date)) else str(value)


def card(row: dict) -> dict:
    """The display form of one product in a grid, shared by home, listing, and search."""
    now = dt.datetime.now(dt.timezone.utc)
    created = row.get("created_at")
    badges = []
    if isinstance(created, dt.datetime) and (now - created).days < 30:
        badges.append("new")
    stock = row.get("stock")
    if stock is not None and stock < 100:
        badges.append("low stock")
    if (row.get("review_count") or 0) >= 20 and (row.get("avg_rating") or 0) >= 4.5:
        badges.append("bestseller")
    return {
        "id": row["product_id"],
        "seller_id": row["seller_id"],
        "title": row["title"],
        "price": money(row.get("min_price") or row.get("price_cents")),
        "price_cents": row.get("min_price") or row.get("price_cents"),
        "rating": round(row.get("avg_rating") or 0, 2),
        "stars": stars(row.get("avg_rating")),
        "reviews": row.get("review_count") or 0,
        "thumbnail": f"/media/{row['thumbnail_key']}" if row.get("thumbnail_key") else None,
        "badges": badges,
    }


# --- Data ---

class Categories:
    """Categories never change during a run, so each instance loads them once."""

    rows: list[dict] = []
    by_id: dict[int, dict] = {}
    by_slug: dict[str, dict] = {}

    @classmethod
    async def load(cls, conn: Conn) -> None:
        cls.rows = await conn.all(sql.CATEGORIES)
        cls.by_id = {r["category_id"]: r for r in cls.rows}
        cls.by_slug = {r["slug"]: r for r in cls.rows}


async def home_data(conn: Conn) -> dict:
    featured = await conn.all(sql.HOME_FEATURED)
    return {
        "categories": [{"id": c["category_id"], "slug": c["slug"], "name": c["name"]} for c in Categories.rows],
        "featured": [card(r) for r in featured],
    }


async def listing_data(conn: Conn, category: int, sort: str, page: int, min_price: int | None = None, max_price: int | None = None) -> dict:
    rows = await conn.all(sql.LISTING[sort], {
        "category": category, "limit": PAGE, "offset": (page - 1) * PAGE,
        "min_price": min_price or 0, "max_price": max_price if max_price is not None else 2**31 - 1,
    })
    facets = await conn.all(sql.LISTING_FACETS, {"category": category})
    total = sum(f["n"] for f in facets)
    width = 20000 // 8
    return {
        "category": Categories.by_id[category],
        "sort": sort,
        "page": page,
        "min_price": min_price,
        "max_price": max_price,
        "total": total,
        "pages": max(1, -(-total // PAGE)),
        "items": [card(r) for r in rows],
        "facets": [
            {
                "label": f"{money((f['bucket'] - 1) * width)} – {money(f['bucket'] * width)}" if 1 <= f["bucket"] <= 8 else "other",
                "count": f["n"],
            }
            for f in facets
        ],
    }


async def detail_data(conn: Conn, seller: int, product: int, mode: str) -> dict | None:
    key = {"seller": seller, "product": product}
    row = await conn.one(sql.PRODUCT, key)
    if row is None:
        return None
    if mode == "batched":
        batch = await conn.one(sql.DETAIL_BATCHED, key)
        variants, reviews, summary = batch["variants"], batch["reviews"], batch["summary"]
    else:
        variants = []
        for v in await conn.all(sql.VARIANT_IDS, key):
            variants.append(await conn.one(sql.VARIANT, {"seller": seller, "variant": v["variant_id"]}))
        reviews = []
        for r in await conn.all(sql.REVIEW_IDS, key):
            reviews.append(await conn.one(sql.REVIEW, {"seller": seller, "review": r["review_id"]}))
        summary = await conn.one(sql.REVIEW_SUMMARY, key)
    candidates = await conn.all(sql.RELATED_CANDIDATES, {"category": row["category_id"], "product": product})
    started = time.thread_time()
    related = rank_related(row, candidates)
    stats().span("rank", time.thread_time() - started)
    return {
        "product": {
            "id": row["product_id"],
            "seller_id": row["seller_id"],
            "category_id": row["category_id"],
            "title": row["title"],
            "description": row["description"],
            "description_version": row["description_version"],
            "price": money(row["price_cents"]),
            "price_cents": row["price_cents"],
            "image": f"/media/{row['image_key']}" if row["image_key"] else None,
            "updated_at": iso(row["updated_at"]),
        },
        "variants": [
            {"id": v["variant_id"], "sku": v["sku"], "options": v["options"], "price": money(v["price_cents"]), "stock": v["stock"]}
            for v in variants if v
        ],
        "reviews": [
            {"id": r["review_id"], "rating": r["rating"], "stars": stars(r["rating"]), "body": r["body"], "at": iso(r["created_at"])}
            for r in reviews if r
        ],
        "summary": {"count": summary["cnt"], "rating": round(summary["avg_rating"] or 0, 2), "stars": stars(summary["avg_rating"])},
        "related": related,
    }


def rank_related(product: dict, candidates: list[dict]) -> list[dict]:
    """Rank same-category products by text similarity, then price closeness.

    Similarity is the cosine of term-frequency vectors over title and description, with title
    words counted three times, so a candidate that shares the product's distinctive words wins.
    """
    def vector(title: str, text: str) -> Counter:
        v = Counter(TOKEN.findall(text.lower()))
        for word in TOKEN.findall(title.lower()):
            v[word] += 3
        return v

    def cosine(a: Counter, b: Counter) -> float:
        dot = sum(n * b[w] for w, n in a.items() if w in b)
        return dot / (math.sqrt(sum(n * n for n in a.values())) * math.sqrt(sum(n * n for n in b.values())) or 1)

    mine = vector(product["title"], product["description"])
    price = product["price_cents"] or 1
    scored = []
    for c in candidates:
        similarity = cosine(mine, vector(c["title"], c["blurb"]))
        closeness = 1 / (1 + abs((c["price_cents"] or 0) - price) / price)
        scored.append((similarity * 3 + closeness, c))
    scored.sort(key=lambda pair: (-pair[0], pair[1]["product_id"]))
    return [card(c) | {"score": round(score, 3)} for score, c in scored[:6]]


async def search_data(conn: Conn, q: str, filters: dict) -> dict:
    terms = [t for t in TOKEN.findall(q.lower()) if t][:5]
    if not terms:
        return {"q": q, "count": 0, "results": [], "filters": filters}
    rows = await conn.all(sql.SEARCH, {"pattern": f"%{q.strip()}%", **filters})
    started = time.thread_time()
    results = rank_search(terms, rows)
    stats().span("rank", time.thread_time() - started)
    return {"q": q, "count": len(results), "results": results[:20], "filters": filters}


def rank_search(terms: list[str], rows: list[dict]) -> list[dict]:
    scored = []
    for r in rows:
        title, body = r["title"].lower(), r["description"].lower()
        score = sum(title.count(t) * 3 + body.count(t) for t in terms)
        at = body.find(terms[0])
        snippet = r["description"][max(0, at - 40): at + 80] if at >= 0 else r["description"][:120]
        scored.append((score, r, snippet))
    scored.sort(key=lambda s: (-s[0], s[1]["product_id"]))
    return [card(r) | {"snippet": snippet, "score": score} for score, r, snippet in scored]


# --- HTML ---

TEMPLATES = Environment(
    loader=FileSystemLoader(Path(__file__).parent / "templates"),
    autoescape=select_autoescape(["html"]),
    trim_blocks=True,
    lstrip_blocks=True,
)


def _render(name: str, d: dict, **extra) -> str:
    return TEMPLATES.get_template(name).render(**d, **extra)


def render_home(d: dict) -> str:
    return _render("home.html", d)


def render_listing(d: dict) -> str:
    return _render("listing.html", d, sorts=list(sql.SORTS))


def render_detail(d: dict) -> str:
    return _render("detail.html", d)


def render_search(d: dict) -> str:
    return _render("search.html", d)
