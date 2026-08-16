#!/usr/bin/env python3
"""Build, and optionally publish, the Railway template behind the deploy button.

    scripts/railway_template.py --build            # stand the source project up
    scripts/railway_template.py --generate <pid>   # snapshot it into a template
    scripts/railway_template.py --publish <tid>    # list it on Railway's marketplace

A Railway template is generated *from a real project*, not written as a file, so
the only way to change what one-click deploy produces is to stand the project
up, get it right, and snapshot it. That makes this script the source of truth
for the button in the README: the project it builds is what a stranger gets.

The difference from deploy_railway.py, which provisions nodes we operate, is
that nothing here is uploaded. The service pulls `flywheelai/dedi-node`, because
a template that built from source would need the stranger to have a fork of this
repo, and one-click deploy is exactly the case where they do not.

Requires a Railway login (`railway login`) or RAILWAY_TOKEN.
"""

from __future__ import annotations  # macOS still ships Python 3.9 as `python3`

import argparse
import json
import os

import sys
import time
import urllib.error
import urllib.request

API = "https://backboard.railway.com/graphql/v2"
NODE_IMAGE = "flywheelai/dedi-node:latest"
PG_IMAGE = "ghcr.io/railwayapp-templates/postgres-ssl:18"
NODE_PORT = 8080
PROJECT_NAME = "dedi-node-template"


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
        {"in": {"name": name, "workspaceId": workspace_id(),
                "description": "Source project for the DeDi node one-click deploy template."}},
    )["projectCreate"]
    envs = [e["node"] for e in data["environments"]["edges"]]
    env = next((e for e in envs if e["name"] == "production"), envs[0])
    return data["id"], env["id"]


def create_service(project_id: str, env_id: str, name: str, variables: dict, image: str) -> str:
    return gql(
        "mutation($in: ServiceCreateInput!) { serviceCreate(input: $in) { id } }",
        {"in": {"projectId": project_id, "environmentId": env_id, "name": name,
                "variables": variables, "source": {"image": image}}},
    )["serviceCreate"]["id"]


def deploy_service(env_id: str, service_id: str) -> None:
    gql(
        "mutation($e: String!, $s: String!) { serviceInstanceDeployV2(environmentId: $e, serviceId: $s) }",
        {"e": env_id, "s": service_id},
    )


def provision_postgres(project_id: str, env_id: str) -> str:
    """Provision a Postgres whose every setting survives templateGenerate.

    Generating a template from a project **drops variables whose value is a
    plain literal** and keeps only interpolations — it cannot tell a literal
    secret from a literal setting, so it discards both. Anything spelled out
    here as `postgres` or `:8080` would reach a stranger as a required field
    with no default, and one-click deploy would open by asking them to type the
    Postgres data directory.

    So nothing below is a literal. The values that are genuinely per-deployment
    are functions, the paths are derived from Railway's own variables, and the
    settings that merely restate the image's defaults — POSTGRES_USER,
    POSTGRES_DB, SSL_CERT_DAYS — are omitted so the image supplies them.
    """
    service_id = create_service(
        project_id, env_id, "Postgres",
        {
            # Generated per deployment by Railway, not by us: a password
            # baked in here would be shared by every node deployed from the
            # template, and would be sitting in this repository's history.
            "POSTGRES_PASSWORD": "${{secret(32)}}",
            # A subdirectory, because Postgres refuses to initialise into a
            # mount point that is not empty. Derived from the volume rather
            # than hardcoded so the two cannot drift apart.
            "PGDATA": "${{RAILWAY_VOLUME_MOUNT_PATH}}/pgdata",
            # `postgres` twice over is the image's own default user and
            # database, so this stays correct without variables to carry it.
            "DATABASE_URL":
                "postgresql://postgres:${{POSTGRES_PASSWORD}}@${{RAILWAY_PRIVATE_DOMAIN}}:5432/postgres",
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
    return service_id


def add_domain(env_id: str, service_id: str) -> str:
    return gql(
        "mutation($in: ServiceDomainCreateInput!) { serviceDomainCreate(input: $in) { domain } }",
        {"in": {"environmentId": env_id, "serviceId": service_id, "targetPort": NODE_PORT}},
    )["serviceDomainCreate"]["domain"]


def wait_until_healthy(url: str, timeout: int = 600) -> dict | None:
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


def build() -> None:
    project_id, env_id = create_project(PROJECT_NAME)
    print(f"created project {PROJECT_NAME} ({project_id})")

    provision_postgres(project_id, env_id)
    print("provisioned Postgres with a volume")

    node_id = create_service(
        project_id, env_id, "dedi-node",
        {
            "DEDI_DB_URL": "${{Postgres.DATABASE_URL}}",
            "DEDI_ORIGIN": "${{RAILWAY_PUBLIC_DOMAIN}}/log",
            "DEDI_PUBLIC_URL": "https://${{RAILWAY_PUBLIC_DOMAIN}}",
            # Set before the write plane exists rather than after. A one-click
            # node is read-only until its operator generates a publisher key,
            # so the console is 404 today — but the credential is in place for
            # the moment they open it, which is the moment nobody remembers to
            # go back and add a password. Railway resolves secret(32) per
            # deployment, so no two nodes share one and this repository never
            # learns any of them.
            "DEDI_ADMIN_PASSWORD": "${{secret(32)}}",
            # DEDI_LISTEN, DEDI_ADMIN_USER and DEDI_CHECKPOINT_INTERVAL are
            # deliberately absent: the binary already defaults them to :8080,
            # `admin` and 30s. Setting them here would only add three literals
            # for templateGenerate to strip, turning each into a question the
            # deployer has to answer to get the default they were going to get.
        },
        image=NODE_IMAGE,
    )
    domain = add_domain(env_id, node_id)
    deploy_service(env_id, node_id)
    print(f"created dedi-node ({node_id}) on {domain} -> :{NODE_PORT}")

    url = "https://" + domain
    print(f"waiting for {url}/healthz …")
    health = wait_until_healthy(url)
    if health is None:
        sys.exit("the template's own project never became healthy — fix that before "
                 "generating a template from it")
    print(f"serving: {url}\n  health {json.dumps(health)}")
    print(f"\nnext: scripts/railway_template.py --generate {project_id}")


def generate(project_id: str) -> None:
    t = gql(
        "mutation($in: TemplateGenerateInput!) { templateGenerate(input: $in) { id code name } }",
        {"in": {"projectId": project_id}},
    )["templateGenerate"]
    print(f"template {t['id']}")
    print(f"deploy URL  https://railway.com/deploy/{t['code']}")
    print(f"\nnext (public listing, optional): scripts/railway_template.py --publish {t['id']}")


def publish(template_id: str, readme_path: str) -> None:
    with open(readme_path) as fh:
        readme = fh.read()
    t = gql(
        "mutation($id: String!, $in: TemplatePublishInput!) { templatePublish(id: $id, input: $in) { code } }",
        {"id": template_id,
         "in": {"category": "Web Servers",
                "description": "A tamper-evident public directory node implementing the "
                               "Decentralized Directory Protocol, backed by a Merkle "
                               "transparency log.",
                "readme": readme,
                "workspaceId": workspace_id()}},
    )["templatePublish"]
    print(f"published: https://railway.com/deploy/{t['code']}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--build", action="store_true", help="create the template's source project")
    g.add_argument("--generate", metavar="PROJECT_ID", help="snapshot a project into a template")
    g.add_argument("--publish", metavar="TEMPLATE_ID", help="list a template publicly")
    ap.add_argument("--readme", default=os.path.join(os.path.dirname(__file__), "..",
                                                     "docs", "railway-template.md"))
    args = ap.parse_args()

    if args.build:
        build()
    elif args.generate:
        generate(args.generate)
    else:
        publish(args.publish, args.readme)


if __name__ == "__main__":
    main()
