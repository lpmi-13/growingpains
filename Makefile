# Growing Pains. Run `make help` for the targets.
SHELL := /bin/bash
SEED ?= 42
STORYLINE ?= word-gets-out
COMPOSE := docker compose -f dev/compose.yaml

# Images follow the other labs: ghcr.io/lpmi-13/<lab>-<vm>-rootfs:<tag>
TAG ?= dev
REGISTRY ?= ghcr.io/lpmi-13
SHOP_IMAGE := registry.growingpains.internal/shop:1
LAB_IMAGE := growingpains-lab
VALKEY_IMAGE := docker.io/valkey/valkey:9.1
# Everything k3s v1.37.2 needs at runtime with traefik, servicelb, and the helm controller off.
SYSTEM_IMAGES := rancher/mirrored-pause:3.10.2 rancher/mirrored-coredns-coredns:1.14.7 \
  rancher/mirrored-metrics-server:v0.9.0 rancher/local-path-provisioner:v0.0.37 rancher/mirrored-library-busybox:1.37.0
GENERATED := rootfs/generated

.PHONY: help test go-test shop-test shop-venv generated dashboard seed dev-up dev-down dev-reset dev-start dev-status dev-report \
  credentials new-credentials lab-image shop-image container-images images

help:
	@echo "make test        Go and Python tests (Python's API tests start a PostgreSQL 18 container)"
	@echo "make generated   regenerate the Grafana dashboard"
	@echo "make dev-up      build and start the local genesis stack (STORYLINE=$(STORYLINE))"
	@echo "make dev-start   press Start: the crowd begins the storyline from SEED=$(SEED)"
	@echo "make dev-status  the crowd's run state"
	@echo "make dev-report  per-minute capacity table for a calibration ramp"
	@echo "make dev-down    stop the stack; dev-reset also deletes the database volume"

test: go-test shop-test

go-test:
	go vet ./...
	go test ./...

shop-venv:
	@test -x shop/.venv/bin/python || (cd shop && uv venv -q --python 3.15 .venv && uv pip install -q --python .venv/bin/python -r requirements.txt pytest httpx)

shop-test: shop-venv
	cd shop && .venv/bin/python -m pytest -q

generated: dashboard

# Generated once, so the node and ops images always share one set of secrets. `make
# new-credentials` replaces them, and both images must then be rebuilt.
credentials: $(GENERATED)/playground.json

$(GENERATED)/playground.json:
	python3 tools/generate_credentials.py

new-credentials:
	python3 tools/generate_credentials.py

lab-image:
	docker build -q -f build/lab.Dockerfile -t $(LAB_IMAGE) .

shop-image:
	docker build -q -t $(SHOP_IMAGE) shop

# Saved where the VM images pick them up: k3s imports these files at boot.
container-images: shop-image
	@for image in $(SYSTEM_IMAGES) $(VALKEY_IMAGE); do docker pull -q $$image >/dev/null; done
	mkdir -p $(GENERATED)/images
	docker save -o $(GENERATED)/images/system.tar $(SYSTEM_IMAGES)
	docker save -o $(GENERATED)/images/shop.tar $(SHOP_IMAGE) $(VALKEY_IMAGE)

# COPY keeps each file's mode, and sshd refuses a group-writable authorized_keys, so the build
# machine's umask must not reach the images.
images: dashboard credentials lab-image container-images
	chmod -R go-w rootfs
	@for vm in node ops; do \
	  echo "== $$vm"; \
	  docker build -f rootfs/Dockerfile --target $$vm -t $(REGISTRY)/growingpains-$$vm-rootfs:$(TAG) . || exit 1; \
	done

dashboard:
	python3 tools/render_dashboard.py

dev/.data/seed.sql: $(wildcard internal/catalog/*.go cmd/gp-seed/*.go)
	mkdir -p dev/.data
	go run ./cmd/gp-seed > $@

seed: dev/.data/seed.sql

dev-up: seed dashboard
	GP_STORYLINE=$(STORYLINE) $(COMPOSE) up -d --build

dev-down:
	$(COMPOSE) down

dev-reset:
	$(COMPOSE) down -v

dev-start:
	curl -fsS -XPOST 'http://127.0.0.1:9101/start?seed=$(SEED)'; echo

dev-status:
	curl -fsS http://127.0.0.1:9101/status; echo

# Usage: make dev-report START=<unix seconds of Start> STEPS=7
dev-report:
	python3 tools/calibration_report.py --start $(START) --steps $(STEPS)
