import os
import subprocess
import time
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]


@pytest.fixture(scope="session")
def postgres_url():
    """A throwaway PostgreSQL 18 with the real schema and a small base catalog."""
    if os.environ.get("GP_TEST_DATABASE_URL"):
        yield os.environ["GP_TEST_DATABASE_URL"]
        return
    name = f"gp-test-{os.getpid()}"
    subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "-e", "POSTGRES_USER=shop", "-e", "POSTGRES_PASSWORD=shop",
                    "-e", "POSTGRES_DB=shop", "-p", "127.0.0.1::5432", "postgres:18",
                    "-c", "shared_preload_libraries=pg_stat_statements"], check=True, capture_output=True)
    try:
        port = subprocess.run(["docker", "port", name, "5432"], check=True, capture_output=True, text=True).stdout.strip().rsplit(":", 1)[1]
        for _ in range(60):
            if subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "shop"], capture_output=True).returncode == 0:
                break
            time.sleep(0.5)
        psql = ["docker", "exec", "-i", name, "psql", "-q", "-v", "ON_ERROR_STOP=1", "-U", "shop", "-d", "shop"]
        subprocess.run(psql, input=(ROOT / "db" / "schema.sql").read_bytes(), check=True, capture_output=True)
        seed = subprocess.run(["go", "run", "./cmd/gp-seed", "-products", "2000", "-sellers", "50"], cwd=ROOT, check=True, capture_output=True).stdout
        subprocess.run(psql, input=seed, check=True, capture_output=True)
        yield f"postgresql://shop:shop@127.0.0.1:{port}/shop"
    finally:
        subprocess.run(["docker", "rm", "-f", name], capture_output=True)
