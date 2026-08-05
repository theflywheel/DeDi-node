#!/usr/bin/env python3
"""Stand up a complete DeDi node on Railway in one command.

    scripts/deploy_railway.py --name dedi-b
    scripts/deploy_railway.py --name dedi-c --project-id <id>      # into an existing project
    scripts/deploy_railway.py --name dedi-b --db-url "$SHARED_PG"  # reuse a Postgres you run

What "complete" means here is a node that serves signed checkpoints without any
follow-up: a Postgres with a volume, the node service, a domain, and the wait
that proves it actually came up. The node mints its own identity key on first
boot, so nothing has to be generated in advance and handed to it.

Two things are done through the GraphQL API rather than the CLI because the CLI
cannot express them: creating a domain with an explicit targetPort, and
attaching a volume. The targetPort is not a detail -- left unset, Railway's edge
guesses which port to route to and a node that is running perfectly well answers
502 from the outside.

Requires: the `railway` CLI, logged in (`railway login`).
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

API = "https://backboard.railway.com/graphql/v2"
PG_IMAGE = "ghcr.io/railwayapp-templates/postgres-ssl:18"
NODE_PORT = 8080


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
        API,
        body,
        {
            "Authorization": "Bearer " + token(),
            "Content-Type": "application/json",
            # Railway rejects urllib's default agent outright.
            "User-Agent": "dedi-deploy",
        },
    )
    try:
        out = json.load(urllib.request.urlopen(req))
    except urllib.error.HTTPError as e:
        sys.exit(f"Railway API {e.code}: {e.read().decode()[:400]}")
    if "errors" in out:
        sys.exit("Railway API: " + json.dumps(out["errors"], indent=2))
    return out["data"]


def run(*args: str) -> None:
    print("  $", " ".join(args))
    subprocess.run(args, check=True)


def workspace_id() -> str:
    spaces = gql("{ me { workspaces { id name } } }")["me"]["workspaces"]
    if not spaces:
        sys.exit("this account has no Railway workspace")
    return spaces[0]["id"]


def create_project(name: str) -> tuple[str, str]:
    data = gql(
        """mutation($in: ProjectCreateInput!) {
             projectCreate(input: $in) {
               id environments { edges { node { id name } } } } }""",
        {"in": {"name": name, "workspaceId": workspace_id()}},
    )["projectCreate"]
    envs = [e["node"] for e in data["environments"]["edges"]]
    env = next((e for e in envs if e["name"] == "production"), envs[0])
    return data["id"], env["id"]


def production_env(project_id: str) -> str:
    envs = [
        e["node"]
        for e in gql(
            "query($id: String!) { project(id: $id) { environments { edges { node { id name } } } } }",
            {"id": project_id},
        )["project"]["environments"]["edges"]
    ]
    env = next((e for e in envs if e["name"] == "production"), envs[0])
    return env["id"]


def create_service(project_id: str, env_id: str, name: str, variables: dict,
                   image: str | None = None) -> str:
    payload: dict = {
        "projectId": project_id,
        "environmentId": env_id,
        "name": name,
        "variables": variables,
    }
    if image:
        payload["source"] = {"image": image}
    return gql(
        "mutation($in: ServiceCreateInput!) { serviceCreate(input: $in) { id } }",
        {"in": payload},
    )["serviceCreate"]["id"]


def deploy_service(env_id: str, service_id: str) -> None:
    gql(
        "mutation($e: String!, $s: String!) { serviceInstanceDeployV2(environmentId: $e, serviceId: $s) }",
        {"e": env_id, "s": service_id},
    )


def provision_postgres(project_id: str, env_id: str) -> str:
    """Create a Postgres the node owns, and return the URL it should dial.

    The password is generated here and referenced, never echoed: it ends up in
    the service's own variables, which is the only place that needs it.
    """
    import secrets

    password = secrets.token_urlsafe(24)
    service_id = create_service(
        project_id, env_id, "Postgres",
        {
            "POSTGRES_USER": "postgres",
            "POSTGRES_PASSWORD": password,
            "POSTGRES_DB": "railway",
            "PGDATA": "/var/lib/postgresql/data/pgdata",
            "SSL_CERT_DAYS": "820",
            "DATABASE_URL":
                "postgresql://postgres:${{POSTGRES_PASSWORD}}@${{RAILWAY_PRIVATE_DOMAIN}}:5432/${{POSTGRES_DB}}",
        },
        image=PG_IMAGE,
    )
    # Without a volume the database lives in the container filesystem and the
    # node loses its log, and its identity, on the next redeploy.
    gql(
        "mutation($in: VolumeCreateInput!) { volumeCreate(input: $in) { id } }",
        {"in": {"projectId": project_id, "serviceId": service_id,
                "mountPath": "/var/lib/postgresql/data"}},
    )
    deploy_service(env_id, service_id)
    return "${{Postgres.DATABASE_URL}}"


def add_domain(env_id: str, service_id: str) -> str:
    return gql(
        "mutation($in: ServiceDomainCreateInput!) { serviceDomainCreate(input: $in) { domain } }",
        {"in": {"environmentId": env_id, "serviceId": service_id, "targetPort": NODE_PORT}},
    )["serviceDomainCreate"]["domain"]


def wait_until_healthy(url: str, timeout: int = 600) -> dict | None:
    """Poll /healthz until the node reports it can reach its database.

    A deploy that returns without checking has only proved the platform accepted
    an upload. /healthz answers 503 while the database is unreachable, so this
    distinguishes "running" from "serving".
    """
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url + "/healthz", timeout=10) as resp:
                if resp.status == 200:
                    return json.load(resp)
        except Exception:
            pass
        time.sleep(5)
    return None


def main() -> None:
    ap = argparse.ArgumentParser(description="Deploy a DeDi node to Railway.")
    ap.add_argument("--name", required=True, help="service name, also the project name when creating one")
    ap.add_argument("--project-id", help="deploy into this existing project instead of creating one")
    ap.add_argument("--db-url", help="Postgres URL to use; omit to provision a dedicated Postgres")
    ap.add_argument("--origin", help="log origin in checkpoint signatures (default: the node's public domain)")
    ap.add_argument("--witness-url",
                    help="base URL of a node this one should watch, including /dedi "
                         "(e.g. https://node-a.example.org/dedi)")
    ap.add_argument("--witness-key", help="that node's verifier key")
    ap.add_argument("--witness-origin", help="that node's log origin")
    ap.add_argument("--witness-interval", default="60s")
    args = ap.parse_args()

    if args.project_id:
        project_id, env_id = args.project_id, production_env(args.project_id)
        print(f"using project {project_id}")
    else:
        project_id, env_id = create_project(args.name)
        print(f"created project {args.name} ({project_id})")

    db_url = args.db_url
    if not db_url:
        print("provisioning Postgres…")
        db_url = provision_postgres(project_id, env_id)

    variables = {
        "DEDI_DB_URL": db_url,
        # Explicit, so the domain's targetPort and the port the node listens on
        # are decided in one place rather than negotiated through $PORT.
        "DEDI_LISTEN": f":{NODE_PORT}",
        "DEDI_ORIGIN": args.origin or "${{RAILWAY_PUBLIC_DOMAIN}}/log",
        "DEDI_CHECKPOINT_INTERVAL": "30s",
    }
    if args.witness_url:
        if not args.witness_key:
            sys.exit("--witness-url requires --witness-key: a witness that does not check "
                     "a signature is not witnessing anything")
        # The witness appends /log/checkpoint to this, so a URL missing /dedi
        # fails on every poll -- and it fails quietly, as a witness that never
        # records a verdict rather than as a startup error.
        if not args.witness_url.rstrip("/").endswith("/dedi"):
            sys.exit(f"--witness-url must end in /dedi, got {args.witness_url}")
        variables.update({
            "DEDI_WITNESS_TARGET_URL": args.witness_url,
            "DEDI_WITNESS_TARGET_KEY": args.witness_key,
            "DEDI_WITNESS_TARGET_ORIGIN": args.witness_origin or args.witness_url,
            "DEDI_WITNESS_INTERVAL": args.witness_interval,
        })

    service_id = create_service(project_id, env_id, args.name, variables)
    print(f"created service {args.name} ({service_id})")

    domain = add_domain(env_id, service_id)
    print(f"domain {domain} -> :{NODE_PORT}")

    # The source is uploaded rather than pulled: Railway has no access to a
    # private repository, and there is no published image yet.
    run("railway", "link", "-p", project_id, "-e", env_id, "-s", service_id)
    run("railway", "up", "-s", service_id, "--detach")

    url = "https://" + domain
    print(f"waiting for {url}/healthz …")
    health = wait_until_healthy(url)
    if health is None:
        sys.exit(f"node did not become healthy; check `railway logs -s {service_id}`")

    print(f"\nnode is serving: {url}")
    print(f"  health     {json.dumps(health)}")
    with urllib.request.urlopen(url + "/dedi/log/checkpoint", timeout=10) as resp:
        print("  checkpoint " + resp.read().decode().splitlines()[0])
    print(f"\nIts verifier key is printed in the boot log; anyone verifying this node, or "
          f"witnessing it, needs it:\n  railway logs -s {service_id} | grep -A1 'verifier key'")


if __name__ == "__main__":
    main()
