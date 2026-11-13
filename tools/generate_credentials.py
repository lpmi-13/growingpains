#!/usr/bin/env python3
"""Generate every secret the VM images need, into rootfs/generated/ (never committed).

A new set per build: k3s join and API tokens, the architect's SSH key for the node dispatcher,
database roles and passwords, and the playground description the architect acts on.

    python3 tools/generate_credentials.py
"""

import json
import secrets
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "rootfs" / "generated"

# The playground's network. Names resolve between VMs from /etc/hosts.
ADDRESSES = {"ops-01": "172.16.0.10", "node-01": "172.16.0.11", "node-02": "172.16.0.12", "node-03": "172.16.0.13", "node-04": "172.16.0.14"}
SUBNET = "172.16.0.0/24"
SHOP_IMAGE = "registry.growingpains.internal/shop:1"
VALKEY_IMAGE = "docker.io/valkey/valkey:9.1"
PAUSE_IMAGE = "rancher/mirrored-pause:3.10.2"


def token(n: int = 24) -> str:
    return secrets.token_hex(n)


def write(name: str, content: str, mode: int = 0o600) -> None:
    path = OUT / name
    path.write_text(content)
    path.chmod(mode)


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    pw = {role: token(16) for role in ["shop", "replicator", "gp_admin", "gp_agent", "grafana"]}
    k3s, architect_token, prometheus_token = token(), token(), token()

    key = OUT / "id_ed25519"
    for p in [key, key.with_suffix(".pub")]:
        p.unlink(missing_ok=True)
    subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "gp-architect", "-f", str(key)], check=True)
    pub = key.with_suffix(".pub").read_text().strip()
    # The node's root account accepts this key only for the dispatcher.
    write("authorized_keys", f'command="/usr/local/libexec/gp/dispatch",no-pty,no-agent-forwarding,no-port-forwarding,no-X11-forwarding {pub}\n')

    write("tokens.csv", f"{architect_token},gp-architect,gp-architect\n{prometheus_token},gp-prometheus,gp-prometheus\n")
    write("architect.token", architect_token + "\n")
    write("prometheus.token", prometheus_token + "\n")
    write("k3s-server.yaml", "\n".join([
        f"token: {k3s}",
        "token-auth-file: /etc/rancher/k3s/tokens.csv",
        f"node-ip: {ADDRESSES['ops-01']}",
        "write-kubeconfig-mode: '0600'",
        # Like a managed control plane: it runs no shop pods.
        "node-taint: ['node-role.kubernetes.io/control-plane=true:NoSchedule']",
        "disable: [traefik, servicelb]",
        "disable-helm-controller: true",
        "kube-controller-manager-arg: ['horizontal-pod-autoscaler-sync-period=5s']",
        "",
    ]))
    write("k3s-agent.yaml", f"server: https://{ADDRESSES['ops-01']}:6443\ntoken: {k3s}\n")

    write("roles.sql", "\n".join([
        f"CREATE ROLE shop LOGIN PASSWORD '{pw['shop']}';",
        f"CREATE ROLE replicator LOGIN REPLICATION PASSWORD '{pw['replicator']}';",
        # Replication slots, and reading pg_stat_replication.
        f"CREATE ROLE gp_admin LOGIN REPLICATION PASSWORD '{pw['gp_admin']}' IN ROLE pg_monitor;",
        f"CREATE ROLE gp_agent LOGIN PASSWORD '{pw['gp_agent']}' IN ROLE pg_monitor;",
        "",
    ]))
    write("grants.sql", "GRANT ALL ON SCHEMA public TO shop;\nGRANT ALL ON ALL TABLES IN SCHEMA public TO shop;\n"
          "GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO shop;\n")
    write("pg_hba.append", "\n".join([
        f"host shop shop {SUBNET} scram-sha-256",
        f"host shop gp_admin {SUBNET} scram-sha-256",
        f"host replication replicator {SUBNET} scram-sha-256",
        "host shop gp_agent 127.0.0.1/32 scram-sha-256",
        "host shop shop 127.0.0.1/32 scram-sha-256",
        "",
    ]), 0o644)
    write("pgpass", f"*:5432:*:replicator:{pw['replicator']}\n")
    write("pgbouncer-userlist.txt", f'"shop" "{pw["shop"]}"\n')
    write("api.env", f"GP_DATABASE_URL=postgresql://shop:{pw['shop']}@127.0.0.1:5432/shop\n"
          f"GP_CONFIG_URL=http://{ADDRESSES['ops-01']}:7000/config\n")
    write("agent-dsn", f"host=127.0.0.1 port=5432 dbname=shop user=gp_agent password={pw['gp_agent']}\n")
    write("grafana.env", f"GF_SECURITY_ADMIN_PASSWORD={pw['grafana']}\n")
    write("architect.env", f"GP_GRAFANA_PASSWORD={pw['grafana']}\nGP_ARCHITECT_ARGS=\n")
    write("playground.json", json.dumps({
        "kube_server": "https://127.0.0.1:6443",
        "kube_token_file": "/etc/gp/architect.token",
        "kube_ca_file": "/var/lib/rancher/k3s/server/tls/server-ca.crt",
        "nodes": {n: a for n, a in ADDRESSES.items() if n != "ops-01"},
        "ssh_key": "/etc/gp/id_ed25519",
        "ssh_user": "root",
        "shop_image": SHOP_IMAGE,
        "valkey_image": VALKEY_IMAGE,
        "pause_image": PAUSE_IMAGE,
        "admin_dsn": f"postgresql://gp_admin:{pw['gp_admin']}@{ADDRESSES['node-01']}:5432/shop",
        "app_dsn": f"postgresql://shop:{pw['shop']}@{ADDRESSES['node-01']}:5432/shop",
        "replica_dsn": f"postgresql://shop:{pw['shop']}@%s:5432/shop",
        "pooler_dsn": f"postgresql://shop:{pw['shop']}@{ADDRESSES['node-01']}:6432/shop",
        "config_url": f"http://{ADDRESSES['ops-01']}:7000/config",
        "cluster_dns": "10.43.0.10",
        "legacy_server": "127.0.0.1:8000",
        "fleet_host": "api.shop.svc.cluster.local",
        "api_port": 8000,
    }, indent=1) + "\n")
    # Grafana on ops-01 reads Prometheus on its own loopback.
    prov = OUT / "grafana-provisioning"
    (prov / "datasources").mkdir(parents=True, exist_ok=True)
    (prov / "dashboards").mkdir(parents=True, exist_ok=True)
    src = ROOT / "deploy" / "grafana" / "provisioning"
    (prov / "datasources" / "prometheus.yml").write_text(
        (src / "datasources" / "prometheus.yml").read_text().replace("http://prometheus:9090", "http://127.0.0.1:9090"))
    (prov / "dashboards" / "growingpains.yml").write_text((src / "dashboards" / "growingpains.yml").read_text())
    print(f"wrote {OUT.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
