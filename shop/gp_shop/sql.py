"""Every SQL statement the API runs, tagged so pg_stat_statements can class it.

The tag /*gp:<read|write>:<name>*/ is kept in pg_stat_statements' query text. The agent classes
database time by it into reads and writes, which is the plan's "fixed map of the API's
statements".
"""

HOME_FEATURED = """SELECT /*gp:read:home_featured*/ p.seller_id, p.product_id, p.title, p.price_cents, p.thumbnail_key, p.category_id,
       r.cnt AS review_count, r.avg_rating
FROM products p
LEFT JOIN LATERAL (
    SELECT count(*) AS cnt, avg(rating)::float AS avg_rating FROM reviews
    WHERE reviews.seller_id = p.seller_id AND reviews.product_id = p.product_id
) r ON true
ORDER BY p.created_at DESC
LIMIT 12"""

CATEGORIES = "SELECT /*gp:read:categories*/ category_id, slug, name FROM categories ORDER BY category_id"

# The listing. Newest-first and price sorts page through the category first and aggregate only
# the 24 rows shown. Sorting by rating has to aggregate the reviews of every product in the
# category before it can page, so it is the expensive one, and its cost grows with the category.
_LISTING_PAGED = """WITH /*gp:read:listing_{sort}*/ page AS (
    SELECT seller_id, product_id, title, price_cents, thumbnail_key, created_at
    FROM products
    WHERE category_id = %(category)s AND price_cents BETWEEN %(min_price)s AND %(max_price)s
    ORDER BY {order} LIMIT %(limit)s OFFSET %(offset)s
)
SELECT page.*, r.cnt AS review_count, coalesce(r.avg_rating, 0) AS avg_rating, v.min_price, v.stock
FROM page
LEFT JOIN LATERAL (
    SELECT count(*) AS cnt, avg(rating)::float AS avg_rating FROM reviews
    WHERE reviews.seller_id = page.seller_id AND reviews.product_id = page.product_id
) r ON true
LEFT JOIN LATERAL (
    SELECT min(price_cents) AS min_price, sum(stock) AS stock FROM variants
    WHERE variants.seller_id = page.seller_id AND variants.product_id = page.product_id
) v ON true
ORDER BY {order}"""

_LISTING_BY_RATING = """WITH /*gp:read:listing_rating*/ rated AS (
    SELECT p.seller_id, p.product_id, p.title, p.price_cents, p.thumbnail_key, p.created_at,
           r.cnt AS review_count, coalesce(r.avg_rating, 0) AS avg_rating
    FROM products p
    LEFT JOIN LATERAL (
        SELECT count(*) AS cnt, avg(rating)::float AS avg_rating FROM reviews
        WHERE reviews.seller_id = p.seller_id AND reviews.product_id = p.product_id
    ) r ON true
    WHERE p.category_id = %(category)s AND p.price_cents BETWEEN %(min_price)s AND %(max_price)s
    ORDER BY avg_rating DESC, review_count DESC, product_id
    LIMIT %(limit)s OFFSET %(offset)s
)
SELECT rated.*, v.min_price, v.stock FROM rated
LEFT JOIN LATERAL (
    SELECT min(price_cents) AS min_price, sum(stock) AS stock FROM variants
    WHERE variants.seller_id = rated.seller_id AND variants.product_id = rated.product_id
) v ON true
ORDER BY avg_rating DESC, review_count DESC, product_id"""

SORTS = {
    "newest": "created_at DESC, product_id",
    "price_asc": "price_cents ASC, product_id",
    "price_desc": "price_cents DESC, product_id",
    "rating": None,
}
LISTING = {
    name: _LISTING_PAGED.format(sort=name, order=order) if order else _LISTING_BY_RATING
    for name, order in SORTS.items()
}

# Price buckets for the category's filter panel. Their sum is the category's product count.
LISTING_FACETS = """SELECT /*gp:read:listing_facets*/ width_bucket(price_cents, 0, 20000, 8) AS bucket, count(*) AS n
FROM products WHERE category_id = %(category)s GROUP BY 1 ORDER BY 1"""

PRODUCT = """SELECT /*gp:read:product*/ seller_id, product_id, category_id, title, description, description_version, price_cents,
       image_key, thumbnail_key, created_at, updated_at
FROM products WHERE seller_id = %(seller)s AND product_id = %(product)s"""

# Genesis loads variants and reviews one row at a time: an N+1 pattern.
VARIANT_IDS = """SELECT /*gp:read:variant_ids*/ variant_id FROM variants WHERE seller_id = %(seller)s AND product_id = %(product)s ORDER BY variant_id"""
VARIANT = """SELECT /*gp:read:variant*/ variant_id, sku, options, price_cents, stock FROM variants
WHERE seller_id = %(seller)s AND variant_id = %(variant)s"""
REVIEW_IDS = """SELECT /*gp:read:review_ids*/ review_id FROM reviews WHERE seller_id = %(seller)s AND product_id = %(product)s
ORDER BY created_at DESC LIMIT 10"""
REVIEW = """SELECT /*gp:read:review*/ review_id, user_id, rating, body, created_at FROM reviews
WHERE seller_id = %(seller)s AND review_id = %(review)s"""
REVIEW_SUMMARY = """SELECT /*gp:read:review_summary*/ count(*) AS cnt, coalesce(avg(rating), 0)::float AS avg_rating FROM reviews
WHERE seller_id = %(seller)s AND product_id = %(product)s"""

# The batched alternative that the "batched queries" tuning move switches to.
DETAIL_BATCHED = """SELECT /*gp:read:detail_batched*/
  (SELECT coalesce(json_agg(v ORDER BY v.variant_id), '[]') FROM (
      SELECT variant_id, sku, options, price_cents, stock FROM variants
      WHERE seller_id = %(seller)s AND product_id = %(product)s) v) AS variants,
  (SELECT coalesce(json_agg(r), '[]') FROM (
      SELECT review_id, user_id, rating, body, created_at FROM reviews
      WHERE seller_id = %(seller)s AND product_id = %(product)s
      ORDER BY created_at DESC LIMIT 10) r) AS reviews,
  (SELECT json_build_object('cnt', count(*), 'avg_rating', coalesce(avg(rating), 0)::float)
      FROM reviews WHERE seller_id = %(seller)s AND product_id = %(product)s) AS summary"""

RELATED_CANDIDATES = """SELECT /*gp:read:related*/ seller_id, product_id, title, price_cents, thumbnail_key,
       left(description, 400) AS blurb
FROM products
WHERE category_id = %(category)s AND product_id <> %(product)s
ORDER BY created_at DESC LIMIT 120"""

# Naive search: a sequential scan of every title and description.
SEARCH = """SELECT /*gp:read:search*/ seller_id, product_id, category_id, title, description, price_cents, thumbnail_key
FROM products
WHERE (title ILIKE %(pattern)s OR description ILIKE %(pattern)s)
  AND (%(category)s::int IS NULL OR category_id = %(category)s)
  AND (%(min_price)s::int IS NULL OR price_cents >= %(min_price)s)
  AND (%(max_price)s::int IS NULL OR price_cents <= %(max_price)s)
LIMIT 50"""

MEDIA = """SELECT /*gp:read:media*/ content_type, bytes FROM images WHERE seller_id = %(seller)s AND image_key = %(key)s"""

# --- Writes ---

IDEM_CLAIM = """INSERT /*gp:write:idem_claim*/ INTO idempotency_keys (idem_key, route, status, body) VALUES (%(key)s, %(route)s, 0, '')
ON CONFLICT DO NOTHING RETURNING idem_key"""
IDEM_GET = "SELECT /*gp:read:idem_get*/ status, body FROM idempotency_keys WHERE idem_key = %(key)s"
IDEM_STORE = """UPDATE /*gp:write:idem_store*/ idempotency_keys SET status = %(status)s, body = %(body)s WHERE idem_key = %(key)s"""
IDEM_EXPIRE = """DELETE /*gp:write:idem_expire*/ FROM idempotency_keys WHERE idem_key IN (
    SELECT idem_key FROM idempotency_keys WHERE created_at < now() - make_interval(secs => %(age)s) LIMIT 5000)"""

SELLER_EXISTS = "SELECT /*gp:read:seller_exists*/ 1 AS ok FROM sellers WHERE seller_id = %(seller)s"
SELLER_CREATE = """INSERT /*gp:write:seller_create*/ INTO sellers (seller_id, name) VALUES (nextval('seller_seq'), %(name)s) RETURNING seller_id"""
SELLER_DELETE = "DELETE /*gp:write:seller_delete*/ FROM sellers WHERE seller_id = %(seller)s"

PRODUCT_CREATE = """INSERT /*gp:write:product_create*/ INTO products (seller_id, product_id, category_id, title, description, price_cents)
VALUES (%(seller)s, %(seller)s::bigint * 1000000 + nextval('product_local_seq'), %(category)s,
        %(title)s, %(description)s, %(price)s)
RETURNING product_id, description_version"""
PRODUCT_SET_IMAGE = """UPDATE /*gp:write:product_set_image*/ products SET image_key = %(image)s, thumbnail_key = %(thumb)s
WHERE seller_id = %(seller)s AND product_id = %(product)s"""
VARIANT_INSERT = """INSERT /*gp:write:variant_insert*/ INTO variants (seller_id, variant_id, product_id, sku, options, price_cents, stock)
VALUES (%(seller)s, %(variant)s, %(product)s, %(sku)s, %(options)s, %(price)s, %(stock)s)"""
VARIANTS_DELETE = """DELETE /*gp:write:variants_delete*/ FROM variants WHERE seller_id = %(seller)s AND product_id = %(product)s"""
PRODUCT_TOUCH_PRICE = """UPDATE /*gp:write:product_touch*/ products SET price_cents = %(price)s, updated_at = now()
WHERE seller_id = %(seller)s AND product_id = %(product)s RETURNING description_version"""
IMAGE_INSERT = """INSERT /*gp:write:image_insert*/ INTO images (seller_id, image_key, product_id, kind, content_type, bytes)
VALUES (%(seller)s, %(key)s, %(product)s, %(kind)s, %(content_type)s, %(bytes)s)"""
FOLLOWERS = "SELECT /*gp:read:followers*/ follower_id FROM followers WHERE seller_id = %(seller)s"
NOTIFY = """INSERT /*gp:write:notify*/ INTO notifications (follower_id, seller_id, product_id, message)
VALUES (%(follower)s, %(seller)s, %(product)s, %(message)s)"""

PRODUCT_PATCH = """UPDATE /*gp:write:product_patch*/ products SET
    description = coalesce(%(description)s, description),
    description_version = description_version + (%(description)s IS NOT NULL)::int,
    price_cents = coalesce(%(price)s, price_cents),
    title = coalesce(%(title)s, title),
    updated_at = now()
WHERE seller_id = %(seller)s AND product_id = %(product)s
RETURNING description_version, price_cents"""
VARIANT_SET_STOCK = """UPDATE /*gp:write:variant_stock*/ variants SET stock = %(stock)s WHERE seller_id = %(seller)s AND variant_id = %(variant)s"""
PRODUCT_DELETE = """DELETE /*gp:write:product_delete*/ FROM products WHERE seller_id = %(seller)s AND product_id = %(product)s"""

REVIEW_CREATE = """INSERT /*gp:write:review_create*/ INTO reviews (seller_id, product_id, user_id, rating, body)
VALUES (%(seller)s, %(product)s, %(user)s, %(rating)s, %(body)s) RETURNING review_id"""

# Stock decrements take each variant's row lock, in variant order to avoid deadlocks.
STOCK_TAKE = """UPDATE /*gp:write:stock_take*/ variants SET stock = stock - %(qty)s
WHERE seller_id = %(seller)s AND variant_id = %(variant)s AND stock >= %(qty)s
RETURNING price_cents"""
VARIANT_PRESENT = """SELECT /*gp:read:variant_present*/ 1 AS ok FROM variants WHERE seller_id = %(seller)s AND variant_id = %(variant)s"""
ORDER_CREATE = """INSERT /*gp:write:order_create*/ INTO orders (seller_id, buyer_id, total_cents, line_count)
VALUES (%(seller)s, %(buyer)s, %(total)s, %(lines)s) RETURNING order_id"""
ORDER_ITEM = """INSERT /*gp:write:order_item*/ INTO order_items (seller_id, order_id, line_no, variant_id, quantity, price_cents)
VALUES (%(seller)s, %(order)s, %(line)s, %(variant)s, %(qty)s, %(price)s)"""
RESERVATION = """INSERT /*gp:write:reservation*/ INTO reservations (seller_id, order_id, variant_id, quantity, expires_at)
VALUES (%(seller)s, %(order)s, %(variant)s, %(qty)s, now() + interval '15 minutes')"""
