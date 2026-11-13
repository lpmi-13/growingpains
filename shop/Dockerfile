# The shop's image. The same filesystem runs as a systemd unit at genesis (RootDirectory=) and
# as the API fleet's container after the first move, so the move is a handover, not a rebuild.
FROM python:3.15-slim@sha256:65a65fc7b30b06cb6df4930fd4650697cb23f4c2698ab81fc87e4b031bc36928 AS runtime
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: -r /tmp/requirements.txt && rm /tmp/requirements.txt
COPY gp_shop /opt/shop/gp_shop
WORKDIR /opt/shop
RUN python -m compileall -q gp_shop
USER 65534
EXPOSE 8000
# One process, one worker: the standard build's GIL gives it a ceiling of about one core.
CMD ["uvicorn", "gp_shop.app:app", "--host", "0.0.0.0", "--port", "8000", "--loop", "uvloop", \
     "--http", "httptools", "--no-access-log", "--timeout-graceful-shutdown", "20", "--backlog", "2048"]
