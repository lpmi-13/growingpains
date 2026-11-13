"""Process settings from the environment. Anything a move changes lives in runtime config."""

import os
import socket


def _int(name: str, default: int) -> int:
    return int(os.environ.get(name, default))


def _float(name: str, default: float) -> float:
    return float(os.environ.get(name, default))


# The primary database. Replicas and other backends arrive through runtime config.
DATABASE_URL = os.environ.get("GP_DATABASE_URL", "postgresql://shop:shop@127.0.0.1:5432/shop")
# Server connections per pool. Six pods with two pools each approach max_connections, which is
# what makes the pooler move possible.
POOL_SIZE = _int("GP_POOL_SIZE", 8)
# The architect's runtime config document. Empty means built-in defaults only.
CONFIG_URL = os.environ.get("GP_CONFIG_URL", "")
CONFIG_POLL_SECONDS = _float("GP_CONFIG_POLL_SECONDS", 2.0)
# The name this instance reports, for example "legacy" for the genesis systemd process or the pod name.
INSTANCE = os.environ.get("GP_INSTANCE", socket.gethostname())
IMPL = "python"
# How long a read-your-writes token lasts in the client's cookie.
TOKEN_MAX_AGE = _int("GP_TOKEN_MAX_AGE", 10)
# The replica's replay position is checked at most this often.
REPLICA_CHECK_SECONDS = _float("GP_REPLICA_CHECK_SECONDS", 0.1)
# Idempotency keys are kept this long.
IDEMPOTENCY_SECONDS = _int("GP_IDEMPOTENCY_SECONDS", 600)
LISTING_PAGE_SIZE = 24
MAX_LISTING_PAGE = 50
