#!/usr/bin/env python3
"""CPU and memory per container from Prometheus (cAdvisor), idle and during a fire.

    python3 docs/results/resources.py > docs/results/resources.txt

Needs the compose stack running (Prometheus on :9092, console on :8099). The
idle figures are averaged over the last 60 s with nothing happening; the fire
figures over 60 s starting 30 s after a fast fire is started in one room.
"""
import json, os, time, urllib.parse, urllib.request

PROM = "http://127.0.0.1:9092/api/v1/query"
CONSOLE = "http://127.0.0.1:8099"
ROOM = "level0/1552A"
PROJECT = os.environ.get("PROJECT", "d7065e-project") + "-"  # other containers on the host are not ours


def q(expr):
    url = PROM + "?" + urllib.parse.urlencode({"query": expr})
    with urllib.request.urlopen(url, timeout=10) as r:
        return {x["metric"]["name"]: float(x["value"][1]) for x in json.load(r)["data"]["result"] if x["metric"].get("name", "").startswith(PROJECT)}


def snapshot():
    cpu = q('sum(rate(container_cpu_usage_seconds_total{name!=""}[60s])) by (name) * 100')
    mem = q('sum(container_memory_rss{name!=""}) by (name)')
    return cpu, mem


def post(path, body=""):
    req = urllib.request.Request(CONSOLE + path, data=body.encode(), method="POST", headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10).read()


def short(n):
    return n.replace("d7065e-project-", "").removesuffix("-1")


idle = snapshot()
post("/physics/fire", json.dumps({"room": ROOM, "growth": "fast"}))
time.sleep(90)
fire = snapshot()
post("/physics/clear")
post("/detector/operator/" + ROOM + "/release", "{}")

names = sorted(set(idle[0]) | set(fire[0]), key=lambda n: -fire[0].get(n, 0))
print(f"{'container':22} {'CPU idle %':>11} {'CPU fire %':>11} {'RSS idle MB':>12} {'RSS fire MB':>12}")
for n in names:
    print(f"{short(n):22} {idle[0].get(n, 0):11.1f} {fire[0].get(n, 0):11.1f} {idle[1].get(n, 0) / 1e6:12.1f} {fire[1].get(n, 0) / 1e6:12.1f}")
print(f"{'TOTAL':22} {sum(idle[0].values()):11.1f} {sum(fire[0].values()):11.1f} {sum(idle[1].values()) / 1e6:12.1f} {sum(fire[1].values()) / 1e6:12.1f}")
