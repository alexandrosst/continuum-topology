#!/usr/bin/env python3
"""An example advisor for the decision loop (see ../decision-loop.md).

It reads each approved agent's own load from the server and, for an agent whose CPU has stayed high, proposes
pausing that agent's flow collector. It only ever PROPOSES: nothing changes until a person accepts the proposal in
the UI or with POST .../proposals/{id}/accept. Replace decide() with your own method; the rest is plumbing.

No dependencies beyond Python 3.8+. Configuration is by environment, so the token never appears in a process list:

    IKHNOS_URL    base URL of the server's JSON API, e.g. https://ikhnos.example.com
    IKHNOS_ORG    organisation id, e.g. default
    IKHNOS_TOKEN  a personal API token of a user with the editor role (Settings -> API tokens)

    advisor.py run --dry-run        show what it would propose, send nothing
    advisor.py run                  propose once
    advisor.py run --every 300      propose every five minutes until stopped
    advisor.py list [--status pending]
    advisor.py show pr-...          one proposal, with its Outcome once it has one
"""
import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

ADVISOR = "cpu-guard-example"


class ApiError(Exception):
    def __init__(self, status, message):
        super().__init__("HTTP %s: %s" % (status, message))
        self.status = status


class Api:
    def __init__(self, base, org, token, timeout=30):
        self.root = base.rstrip("/") + "/api/v1/orgs/" + org
        self.token = token
        self.timeout = timeout

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.root + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + self.token)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                return json.loads(resp.read() or b"null")
        except urllib.error.HTTPError as e:
            try:
                msg = json.loads(e.read()).get("error", "")
            except Exception:
                msg = ""
            raise ApiError(e.code, msg or e.reason)

    def get(self, path):
        return self.call("GET", path)

    def post(self, path, body):
        return self.call("POST", path, body)


def decide(agent, samples, has_pending, threshold=25.0, window=12, minimum=6):
    """The method. Given one agent's state document, its self-telemetry samples (oldest first) and whether it already has
    a proposal waiting, return a proposal body (without "advisor" and "modelVersion") or None.

    This one is deliberately simple: a high mean CPU of the agent's own process over the last few samples, while its
    flow collector is running. Its "predicted" figure is a guess to be replaced by whatever your method estimates.
    """
    if has_pending or agent.get("status") != "approved" or agent.get("accessTier", 0) < 2:
        return None  # flows are collected from tier 2; below that there is nothing to pause
    paused = (agent.get("consent") or {}).get("pausedCollectors") or []
    if "flow" in paused:
        return None
    cpu = [s["cpuPct"] for s in samples if s.get("cpuPct") is not None][-window:]
    if len(cpu) < minimum:
        return None  # not enough to judge
    mean = sum(cpu) / len(cpu)
    if mean <= threshold:
        return None
    return {
        "change": {"agentId": agent["id"], "pause": ["flow"]},
        "evidence": [
            {"name": "agent CPU, mean of last %d samples" % len(cpu), "value": "%.1f %%" % mean, "class": "measured",
             "observedAt": samples[-1]["t"], "source": "GET /telemetry/self"},
            {"name": "flow collector paused by an administrator", "value": "no", "class": "declared", "source": "GET /state"},
        ],
        "predicted": {"agent_cpu_cores": -0.3},
        "ttlSeconds": 3600,
    }


def propose_once(api, dry_run=False, out=sys.stdout, **rule):
    state = api.get("/state")
    telemetry = {e["id"]: e.get("samples", []) for e in api.get("/telemetry/self") if e.get("kind") == "agent"}
    pending = {p["agentId"] for p in api.get("/proposals?status=pending")["proposals"]}
    version = api.get("/model").get("modelVersion", 0)
    made = 0
    for agent in state.get("agents", []):
        body = decide(agent, telemetry.get(agent["id"], []), agent["id"] in pending, **rule)
        if body is None:
            continue
        body.update({"advisor": ADVISOR, "modelVersion": version})
        label = "%s (%s)" % (agent.get("name", agent["id"]), agent["id"])
        if dry_run:
            print("would propose for %s:\n%s" % (label, json.dumps(body, indent=2)), file=out)
            continue
        try:
            p = api.post("/proposals", body)
        except ApiError as e:
            print("not proposed for %s: %s" % (label, e), file=out)
            continue
        made += 1
        print("proposed %s for %s: pause flow (widens: %s, confidence: %s, expires %s)"
              % (p["id"], label, p["widens"], p["confidence"], p["expiresAt"]), file=out)
    if not made and not dry_run:
        print("nothing to propose", file=out)
    return made


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run", help="look at the agents and propose")
    r.add_argument("--dry-run", action="store_true")
    r.add_argument("--every", type=int, metavar="SECONDS", help="repeat until stopped")
    r.add_argument("--threshold", type=float, default=25.0, help="mean CPU percent of the agent's process above which to propose (default 25)")
    l = sub.add_parser("list", help="list proposals")
    l.add_argument("--status")
    s = sub.add_parser("show", help="show one proposal")
    s.add_argument("id")
    args = ap.parse_args(argv)

    missing = [k for k in ("IKHNOS_URL", "IKHNOS_ORG", "IKHNOS_TOKEN") if not os.environ.get(k)]
    if missing:
        ap.error("set " + ", ".join(missing))
    api = Api(os.environ["IKHNOS_URL"], os.environ["IKHNOS_ORG"], os.environ["IKHNOS_TOKEN"])
    try:
        if args.cmd == "run":
            while True:
                propose_once(api, dry_run=args.dry_run, threshold=args.threshold)
                if not args.every:
                    break
                time.sleep(args.every)
        elif args.cmd == "list":
            q = "?status=" + args.status if args.status else ""
            for p in api.get("/proposals" + q)["proposals"]:
                print("%s  %-9s %s  %s" % (p["id"], p["status"], p["agentId"], json.dumps(p["change"], sort_keys=True)))
        else:
            print(json.dumps(api.get("/proposals/" + args.id), indent=2))
    except ApiError as e:
        print("error: %s" % e, file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
