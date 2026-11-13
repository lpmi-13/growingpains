# syntax=docker/dockerfile:1
#
# The playground's VM images. Build from the repository root with `make images`.
#   node  one image for node-01 to node-04; each picks its role from its hostname at boot
#   ops   ops-01: k3s server, Prometheus, Grafana, architect, crowd, prober
#
# The shop's container image and the lab's binaries are built first (see the Makefile), and
# k3s imports every container image from files at boot, so nothing is pulled at runtime.
ARG SHOP_IMAGE=registry.growingpains.internal/shop:1
ARG LAB_IMAGE=growingpains-lab

FROM ${SHOP_IMAGE} AS shop
FROM ${LAB_IMAGE} AS lab

FROM ghcr.io/iximiuz/labs/rootfs:ubuntu-26-04@sha256:9be358de5a0eee258adf110f6fd33f1dfea39acf71d17754920c954a83563145 AS base
USER root
ENV HOME=/root
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       ca-certificates curl iproute2 jq less lsof procps openssh-client prometheus-node-exporter vim-tiny \
    && systemctl enable prometheus-node-exporter \
    && rm -rf /var/lib/apt/lists/*
ADD --checksum=sha256:265b3ee6a3198ef14033c63ef46cbb32370d83795199fb6d1b01f21575eddbda \
    --chmod=755 https://github.com/k3s-io/k3s/releases/download/v1.37.2%2Bk3s1/k3s /usr/local/bin/k3s
COPY --from=lab /usr/local/bin/gp-agent /usr/local/bin/gp-agent

# --- The base dataset, seeded once into a PostgreSQL 18 cluster at build time ---
FROM base AS node
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nginx postgresql-18 pgbouncer \
    && systemctl disable nginx postgresql pgbouncer \
    && rm -rf /var/lib/apt/lists/* /etc/nginx/sites-enabled/default
COPY --from=lab /usr/local/bin/gp-seed /tmp/db/gp-seed
COPY db/schema.sql db/conf/shop.conf /tmp/db/
COPY rootfs/generated/roles.sql rootfs/generated/grants.sql rootfs/generated/pg_hba.append /tmp/db/
RUN set -eu; \
    chown -R postgres:postgres /tmp/db; \
    install -o postgres -g postgres -m 0644 /tmp/db/shop.conf /etc/postgresql/18/main/conf.d/shop.conf; \
    cat /tmp/db/pg_hba.append >>/etc/postgresql/18/main/pg_hba.conf; \
    install -d -o postgres -g postgres /run/postgresql; \
    pg_ctlcluster 18 main start; \
    su postgres -c "psql -q -v ON_ERROR_STOP=1 -f /tmp/db/roles.sql -c 'CREATE DATABASE shop OWNER shop'"; \
    su postgres -c "psql -q -v ON_ERROR_STOP=1 -d shop -f /tmp/db/schema.sql"; \
    /tmp/db/gp-seed | su postgres -c "psql -q -v ON_ERROR_STOP=1 -d shop" >/dev/null; \
    su postgres -c "psql -q -v ON_ERROR_STOP=1 -d shop -f /tmp/db/grants.sql"; \
    pg_ctlcluster 18 main stop; \
    rm -rf /tmp/db; \
    truncate --no-create -s 0 /var/log/postgresql/postgresql-18-main.log
# The genesis API runs the shop image's own filesystem (RootDirectory=).
COPY --from=shop / /opt/shop/rootfs/
COPY rootfs/node/ /
COPY deploy/nginx/shop.conf /etc/nginx/sites-available/shop
COPY deploy/nginx/upstream-genesis.conf /etc/nginx/gp/upstream.conf
COPY rootfs/generated/images/ /var/lib/rancher/k3s/agent/images/
COPY rootfs/generated/k3s-agent.yaml /etc/rancher/k3s/config.yaml
COPY rootfs/generated/api.env rootfs/generated/agent-dsn /etc/gp/
COPY rootfs/generated/authorized_keys /root/.ssh/authorized_keys
COPY rootfs/generated/pgpass /var/lib/postgresql/.pgpass
COPY rootfs/generated/pgbouncer-userlist.txt /etc/pgbouncer/userlist.txt
RUN set -eu; \
    ln -s /etc/nginx/sites-available/shop /etc/nginx/sites-enabled/shop; \
    useradd --system --no-create-home --shell /usr/sbin/nologin gp-agent; \
    chown postgres:postgres /var/lib/postgresql/.pgpass; chmod 0600 /var/lib/postgresql/.pgpass; \
    chmod 0700 /root/.ssh; chmod 0600 /root/.ssh/authorized_keys /etc/rancher/k3s/config.yaml; \
    chown root:gp-agent /etc/gp/agent-dsn; chmod 0640 /etc/gp/agent-dsn /etc/gp/api.env; \
    chown postgres:postgres /etc/pgbouncer/userlist.txt; chmod 0600 /etc/pgbouncer/userlist.txt; \
    install -d -o postgres -g postgres -m 0700 /var/lib/gp; \
    nginx -t; \
    systemctl enable gp-node-role gp-agent k3s-agent
USER laborant
ENV HOME=/home/laborant

# --- Upstream releases for ops-01, unpacked in their own stage to keep rebuilds cheap ---
FROM base AS ops-dist
ADD --checksum=sha256:0388822ace519c5a3a8ef556a541ce1b536e21d872f82deded465bd27a5a0699 \
    https://github.com/prometheus/prometheus/releases/download/v3.13.5/prometheus-3.13.5.linux-amd64.tar.gz /tmp/prometheus.tar.gz
ADD --checksum=sha256:cec8f98cc59bc1a1945833b652371a073be678c5c159a9b1695ad63c04fae5a7 \
    https://dl.grafana.com/oss/release/grafana-13.3.0.linux-amd64.tar.gz /tmp/grafana.tar.gz
RUN set -eu; \
    install -d /opt/prometheus /opt/grafana; \
    tar -xzf /tmp/prometheus.tar.gz -C /opt/prometheus --strip-components=1; \
    tar -xzf /tmp/grafana.tar.gz -C /opt/grafana --strip-components=1; \
    rm -f /tmp/prometheus.tar.gz /tmp/grafana.tar.gz; \
    find /opt/grafana/public -name '*.map' -delete

# --- ops-01: the outside world and the cloud provider ---
FROM base AS ops
# A standalone kubectl, because the one inside k3s unpacks a data directory per user.
ADD --checksum=sha256:c04487523e7aa7d1493f7808174c899db93d7a9cf57531420cd76b9bd415f8f4 \
    --chmod=755 https://dl.k8s.io/release/v1.37.2/bin/linux/amd64/kubectl /usr/local/bin/kubectl
COPY --from=ops-dist /opt/prometheus /opt/prometheus
COPY --from=ops-dist /opt/grafana /opt/grafana
COPY --from=lab /usr/local/bin/gp-architect /usr/local/bin/gp-narrator /usr/local/bin/gp-crowd /usr/local/bin/gp-prober /usr/local/bin/gp-twin /usr/local/bin/
COPY rootfs/ops/ /
COPY deploy/prometheus/rules /etc/prometheus/rules
COPY deploy/grafana/dashboards /var/lib/grafana/dashboards
COPY rootfs/generated/grafana-provisioning /etc/grafana/provisioning
COPY rootfs/generated/images/ /var/lib/rancher/k3s/agent/images/
COPY rootfs/generated/k3s-server.yaml /etc/rancher/k3s/config.yaml
COPY rootfs/generated/tokens.csv /etc/rancher/k3s/tokens.csv
COPY rootfs/generated/architect.token rootfs/generated/id_ed25519 rootfs/generated/playground.json rootfs/generated/architect.env /etc/gp/
COPY rootfs/generated/prometheus.token /etc/prometheus/k8s.token
COPY rootfs/generated/grafana.env /etc/grafana/grafana.env
RUN set -eu; \
    id prometheus >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin prometheus; \
    useradd --system --no-create-home --shell /usr/sbin/nologin grafana; \
    install -d -o prometheus -g prometheus /var/lib/prometheus; \
    install -d -o grafana -g grafana /var/lib/grafana /var/log/grafana; \
    chmod 0600 /etc/rancher/k3s/config.yaml /etc/rancher/k3s/tokens.csv /etc/gp/*; chmod 0644 /etc/gp/architect.env; \
    chown root:prometheus /etc/prometheus/k8s.token; chmod 0640 /etc/prometheus/k8s.token; \
    chown root:grafana /etc/grafana/grafana.env; chmod 0640 /etc/grafana/grafana.env; \
    systemctl enable k3s prometheus grafana gp-architect gp-narrator gp-crowd gp-prober
USER laborant
ENV HOME=/home/laborant
