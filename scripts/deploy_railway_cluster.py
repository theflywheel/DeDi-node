#!/usr/bin/env python3
"""Deploy one DeDi node as a Raft cluster of replicas on Railway.

    scripts/deploy_railway_cluster.py --project-id <id> --name dedi-ha \
        --db-url-template 'postgresql://…@postgres.railway.internal:5432/dedi_ha_{n}' \
        --key "$(cat node.key)" --origin dedi-ha.example.org/log

This deploys *one node, replicated* — not a witness ring. All replicas share
one identity key and one origin because from the outside they are a single
node: a checkpoint must verify against a single public key whichever replica
served it. If you want independent nodes that witness each other, that is
deploy_railway.py, and they must NOT share a key.

Prerequisites this script does not do for you, because both are destructive if
guessed wrong:

  * The per-replica databases must already exist. Replicas cannot share one —
    each applies every command to its own copy of the state, and two replicas
    writing the same rows would corrupt it on the first append.
  * The identity key must be minted once (`dedid keygen`) and passed in. A
    self-provisioning replica would mint three different keys and the cluster
    would serve three different origins.

Requires: the `railway` CLI, logged in.
"""

from __future__ import annotations  # macOS still ships Python 3.9 as `python3`

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

API = "https://backboard.railway.com/graphql/v2"
NODE_PORT = 8080
RAFT_PORT = 7000


def token() -> str:
    if env := os.environ.get("RAILWAY_TOKEN"):
        return env
    try:
        with open(os.path.expanduser("~/.railway/config.json")) as fh:
            return json.load(fh)["user"]["token"]
    except (OSError, KeyError):
        sys.exit("no Railway token: run `railway login`, or set RAILWAY_TOKEN")


def gql(query: str, variables: dict | None = None) -> dict:
    body = json.dumps({"query": query, "variables": variables or {}}).encode()
    req = urllib.request.Request(
        API, body,
        {"Authorization": "Bearer " + token(), "Content-Type": "application/json",
         # Railway rejects urllib's default agent outright.
         "User-Agent": "dedi-deploy"},
    )
    try:
        payload = json.load(urllib.request.urlopen(req))
    except urllib.error.HTTPError as exc:
        sys.exit(f"railway api {exc.code}: {exc.read().decode()[:400]}")
    if payload.get("errors"):
        sys.exit("railway api: " + json.dumps(payload["errors"])[:400])
    return payload["data"]


def run(*args: str) -> None:
    print("$", " ".join(args))
    subprocess.run(args, check=True)


def production_env(project_id: str) -> str:
    envs = gql("query($id: String!) { project(id: $id) { environments { edges { node { id name } } } } }",
               {"id": project_id})["project"]["environments"]["edges"]
    for edge in envs:
        if edge["node"]["name"] == "production":
            return edge["node"]["id"]
    return envs[0]["node"]["id"]


def create_service(project_id: str, env_id: str, name: str, variables: dict) -> str:
    return gql(
        "mutation($in: ServiceCreateInput!) { serviceCreate(input: $in) { id } }",
        {"in": {"projectId": project_id, "environmentId": env_id,
                "name": name, "variables": variables}},
    )["serviceCreate"]["id"]


def add_volume(project_id: str, service_id: str, mount: str) -> None:
    gql("mutation($in: VolumeCreateInput!) { volumeCreate(input: $in) { id } }",
        {"in": {"projectId": project_id, "serviceId": service_id, "mountPath": mount}})


def add_domain(env_id: str, service_id: str) -> str:
    return gql(
        "mutation($in: ServiceDomainCreateInput!) { serviceDomainCreate(input: $in) { domain } }",
        {"in": {"environmentId": env_id, "serviceId": service_id, "targetPort": NODE_PORT}},
    )["serviceDomainCreate"]["domain"]


def wait_for(url: str, check, timeout: int = 600):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=10) as resp:
                body = resp.read().decode()
            got = check(body)
            if got is not None:
                return got
        except Exception:
            pass
        time.sleep(5)
    return None


def main() -> None:
    ap = argparse.ArgumentParser(description="Deploy a replicated DeDi node to Railway.")
    ap.add_argument("--project-id", required=True)
    ap.add_argument("--name", default="dedi-ha", help="service name prefix")
    ap.add_argument("--replicas", type=int, default=3)
    ap.add_argument("--db-url-template", required=True,
                    help="Postgres URL with {n} for the replica number; each must already exist")
    ap.add_argument("--key", required=True, help="the node identity key, shared by every replica")
    ap.add_argument("--origin", required=True, help="log origin, identical on every replica")
    ap.add_argument("--checkpoint-interval", default="30s")
    args = ap.parse_args()

    if args.replicas % 2 == 0:
        # An even cluster tolerates no more failures than the odd size below it
        # and has more ways to lose quorum.
        print(f"note: {args.replicas} replicas tolerate the same {(args.replicas - 1) // 2} "
              f"failure(s) as {args.replicas - 1}, using one machine more")
    if "{n}" not in args.db_url_template:
        sys.exit("--db-url-template needs {n}: replicas must not share a database")

    env_id = production_env(args.project_id)
    ids = [f"{args.name}-{i}" for i in range(1, args.replicas + 1)]

    # Raft members address each other over the project's private network, which
    # is IPv6 — hence the [::] bind. Members are named by hostname rather than a
    # resolved address so a redeployed replica coming back on a new IP is still
    # reachable under the identity the cluster already agreed on.

    services: list[tuple[str, str, str]] = []
    for n, sid_name in enumerate(ids, start=1):
        variables = {
            "DEDI_DB_URL": args.db_url_template.format(n=n),
            "DEDI_KEY": args.key,
            "DEDI_ORIGIN": args.origin,
            "DEDI_LISTEN": f":{NODE_PORT}",
            "DEDI_CHECKPOINT_INTERVAL": args.checkpoint_interval,
            "DEDI_NODE_NAME": args.name,
            "DEDI_CLUSTER_ID": sid_name,
            "DEDI_CLUSTER_BIND": f"[::]:{RAFT_PORT}",
            "DEDI_CLUSTER_DATA_DIR": "/data/raft",
            # Exactly one replica bootstraps the cluster, and only the first
            # time it starts; the others learn the configuration from it.
            # Bootstrapping an existing cluster is a no-op, so this is safe to
            # leave set across redeploys.
            "DEDI_CLUSTER_BOOTSTRAP": "true" if n == 1 else "false",
        }
        service_id = create_service(args.project_id, env_id, sid_name, variables)
        # The Raft log and snapshots must outlive the container, or a restarted
        # replica loses its record of what it had applied.
        add_volume(args.project_id, service_id, "/data")
        domain = add_domain(env_id, service_id)
        services.append((sid_name, service_id, domain))
        print(f"created {sid_name} ({service_id}) -> {domain}")

    # The peer list needs every replica's public URL, so it can only be written
    # once all the domains exist.
    peer_spec = ",".join(
        f"{name}={name}.railway.internal:{RAFT_PORT}=https://{domain}"
        for name, _, domain in services
    )
    for name, service_id, _ in services:
        gql("mutation($in: VariableUpsertInput!) { variableUpsert(input: $in) }",
            {"in": {"projectId": args.project_id, "environmentId": env_id,
                    "serviceId": service_id, "name": "DEDI_CLUSTER_PEERS", "value": peer_spec}})
    print(f"peers: {peer_spec}")

    for name, service_id, _ in services:
        # Uploaded rather than pulled: Railway has no access to a private
        # repository, and there is no published image yet.
        run("railway", "link", "-p", args.project_id, "-e", env_id, "-s", service_id)
        run("railway", "up", "-s", service_id, "--detach")

    for name, service_id, domain in services:
        url = f"https://{domain}"
        print(f"waiting for {url}/healthz …")
        if wait_for(url + "/healthz", lambda b: b if '"ok"' in b or "ok" in b else None) is None:
            sys.exit(f"{name} did not become healthy; check `railway logs -s {service_id}`")
        print(f"  {name} healthy")

    # A cluster with no leader has not formed, however healthy each replica
    # looks on its own — so this is the check that actually matters.
    leader = wait_for(f"https://{services[0][2]}/dedi/network",
                      lambda b: (json.loads(b)["data"]["cluster"] or {}).get("leader_id") or None)
    if not leader:
        sys.exit("replicas are up but no leader was elected — check the private network and logs")
    print(f"\ncluster formed, leader: {leader}")
    for name, _, domain in services:
        print(f"  https://{domain}")


if __name__ == "__main__":
    main()
