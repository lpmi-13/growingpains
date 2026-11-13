"""The API end to end against a real PostgreSQL 18."""

import base64
import importlib
import io
import uuid

import pytest

pytestmark = pytest.mark.postgres


@pytest.fixture(scope="module")
def client(postgres_url, monkeypatch_module):
    monkeypatch_module.setenv("GP_DATABASE_URL", postgres_url)
    from gp_shop import settings
    importlib.reload(settings)
    from gp_shop import db as db_module
    importlib.reload(db_module)
    from gp_shop import app as app_module
    importlib.reload(app_module)
    from starlette.testclient import TestClient

    with TestClient(app_module.app) as c:
        c.headers["Accept"] = "application/json"
        yield c


@pytest.fixture(scope="module")
def monkeypatch_module():
    mp = pytest.MonkeyPatch()
    yield mp
    mp.undo()


def key():
    return str(uuid.uuid4())


def test_reads(client):
    home = client.get("/")
    assert home.status_code == 200 and len(home.json()["featured"]) == 12
    for sort in ["newest", "price_asc", "price_desc", "rating"]:
        r = client.get(f"/products?category=kitchen&sort={sort}&page=1")
        assert r.status_code == 200, r.text
        assert r.headers["x-gp-route"] == "listing"
    filtered = client.get("/products?category=kitchen&min_price=1000&max_price=3000").json()
    assert all(1000 <= i["price_cents"] or i["price_cents"] is None for i in filtered["items"])
    detail = client.get("/products/1000001").json()
    assert detail["product"]["id"] == 1000001 and detail["variants"]
    assert client.get("/products/1000001", headers={"Accept": "text/html"}).text.startswith("<!doctype html>")
    search = client.get("/search?q=oak")
    assert search.status_code == 200 and int(search.headers["x-gp-results"]) >= 0
    assert client.get("/products/999000001").status_code == 404


def test_write_cycle_with_token_and_idempotency(client):
    seller = client.post("/sellers", json={"name": "Test"}).json()["seller_id"]
    from PIL import Image

    img = io.BytesIO()
    Image.new("RGB", (300, 200), (10, 20, 30)).save(img, "JPEG")
    body = {"seller_id": seller, "category_id": 1, "title": "Test Board", "description": "first", "price_cents": 500,
            "variants": [{"options": {"size": "S"}, "price_cents": 500, "stock": 3}], "image": base64.b64encode(img.getvalue()).decode()}
    k = key()
    first = client.post("/products", json=body, headers={"Idempotency-Key": k})
    assert first.status_code == 201 and "gp_lsn" in first.cookies
    again = client.post("/products", json=body, headers={"Idempotency-Key": k})
    assert again.json() == first.json(), "a retried write must return the first answer"
    pid = first.json()["id"]
    assert pid // 1_000_000 == seller

    edited = client.patch(f"/products/{pid}", json={"description": "second"}, headers={"Idempotency-Key": key()})
    assert edited.json()["description_version"] == 2
    assert client.get(f"/products/{pid}").json()["product"]["description_version"] == 2

    variant = pid * 10 + 1
    assert client.post("/orders", json={"seller_id": seller, "buyer_id": 1, "lines": [{"variant_id": variant, "quantity": 2}]}).status_code == 201
    assert client.post("/orders", json={"seller_id": seller, "buyer_id": 1, "lines": [{"variant_id": variant, "quantity": 2}]}).status_code == 409
    assert client.put(f"/products/{pid}/variants", json={"variants": [{"price_cents": 1, "stock": 1}, {"price_cents": 2, "stock": 2}]}).json()["variants"] == 2
    assert client.post(f"/products/{pid}/reviews", json={"user_id": 1, "rating": 4}).status_code == 201
    assert client.delete(f"/products/{pid}").status_code == 200
    assert client.get(f"/products/{pid}").status_code == 404
    assert client.delete(f"/products/{pid}").status_code == 404
    assert client.delete(f"/sellers/{seller}").status_code == 200


def test_bad_input_is_a_400(client):
    assert client.post("/products", content=b"not json").status_code == 400
    assert client.post("/orders", json={"seller_id": 1, "buyer_id": 1, "lines": []}).status_code == 400
    assert client.post("/orders", json={"seller_id": 1, "buyer_id": 1, "lines": [{"variant_id": 999 * 10_000_000 + 11, "quantity": 1}]}).status_code == 400


def test_metrics_and_config(client):
    text = client.get("/metrics").text
    assert "gp_http_requests_total" in text and "gp_request_cpu_seconds_total" in text
    assert 'route="meta"' not in text
    assert client.get("/internal/config").json()["version"] == 0
