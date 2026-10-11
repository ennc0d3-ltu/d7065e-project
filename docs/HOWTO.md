# How to run, demo and break it

## 1. Start

```bash
cp .env.example .env            # set D7065E_DIR to your checkout of github.com/eislab-cps/D7065E
docker compose up -d --build
docker compose ps               # every service should become "healthy"
```

| What | Where |
|---|---|
| Scenario console (start here) | <http://127.0.0.1:8099> |
| 3D viewer, fire-risk layer | <http://127.0.0.1:9090/?floor=level0&layer=fire-risk> |
| Grafana (application and state) | <http://127.0.0.1:3000> |
| Occupancy simulator (the people) | <http://127.0.0.1:8081> |

Stop and reset everything (including history): `docker compose down -v`.

## 2. Run a scenario

No fire is running at start. Open the **scenario console**, pick a room, and press:

- **Fire: slow / medium / fast** - a real fire. Expect: room goes *armed* (pre-alert, no water), then *fire* once heat appears and holds 15 s; sprinkler on; dispatch names the people in the room; route drawn in the viewer.
- **Cooking / Dust** - false alarms. Expect: warning at most, **no sprinkler**.
- **Clear scenarios** - stops the sources. Smoke and heat decay on their own; the sprinkler stays latched until 300 s without any signal.
- **Confirm / Cancel / Release** (per room) - act as the operator. Cancel is honoured for 300 s; a latched fire is only ended by Release or an all-clear.

The sim runs at 10x, so a fire reaches *fire* within about two real minutes. People follow the occupancy simulator's timetable, so pick a room that has people in it right now (the 3D viewer shows them, and the lecture hall `level0/A112_A` is full around 10:30 sim time); the responder's dispatch then lists them as in danger, en route, safe or unaccounted for.

## 3. Break things (it should stay safe)

| Do this | Expect |
|---|---|
| `docker compose kill detector` during a fire | sprinkler stays on; with no detector the actuator's own rules take over |
| `docker compose kill mosquitto` | sprinklers still fire (direct gateway-to-actuator path); history is replayed when the broker returns |
| `docker compose restart buildsim` | everything re-registers within about 3 s |
| `curl localhost:9090/api/doors` before and during a fire | the room's door is `locked`, then `unlocked` while the room is in alarm (and the route out of the room opens: `GET /api/graph/route?from_name=A1008&from_level=level0&to_name=A1016&to_level=level0&type=walkable` answers 404 while it is locked) |
| `curl -XPOST localhost:8091/faults/level0-A1008-smoke -d '{"mode":"stuck"}'` | fire still caught (modes: `stuck`, `offline`, `drift`, `none`) |
| `docker compose stop db-writer timescaledb` | detection unaffected; history catches up later |

## 4. Look at what happened

- **Grafana**: room state over time, readings, decisions with reasons, sprinkler commands, dispatches, dead letters, component health.
- **Operator audit**: every operator action is a decision with its reason in the `decisions` table.
- **SQL**: `psql postgres://fire@127.0.0.1:55432/fire` (tables: `readings`, `decisions`, `commands`, `dispatches`, `dispatch_acks`, `dead_letters`, `component_status`).

## 5. Tune the detector (registry)

Thresholds and models are versioned bundles in the registry (<http://127.0.0.1:8101>). Publishing never activates one; a named person promotes it. The detector polls every 30 s, checks the hard limits again itself, and keeps its last good bundle if the registry is down, slow or sends something invalid.

```bash
curl -XPOST localhost:8101/bundles -d '{"Thresholds":{"SmokeWarn":0.03,"SmokeFire":0.12,"COWarn":25,"COFire":90,"TempWarn":35,"TempFire":57,"RiseWarn":0.05,"RiseFire":0.139},"Note":"more sensitive","Author":"me"}'
curl -XPOST localhost:8101/bundles/1/promote -d '{"by":"me"}'   # detector log: "registry bundle applied"
curl -XPOST localhost:8101/rollback -d '{"by":"me"}'            # back to the previous active bundle
curl localhost:8101/bundles                                      # every version and its status
```

Thresholds looser than the hard limits in `internal/detector/bounds.go` are refused (HTTP 422).

## 6. Measure it

With the stack running (`go build -o /tmp/measure ./cmd/measure` in `src/`):

```bash
/tmp/measure latency -runs 20 -growth fast,medium      # NFR1: sample to sprinkler on, ~25 min
/tmp/measure recovery                                   # kill each process, time its return
SCALE=1 go test -count=1 -v -run 'TestScale|TestIngest' ./internal/detectsvc/   # in src/: rooms and broker limits
python3 docs/results/resources.py                       # CPU and memory idle vs in a fire
```

Results from the last runs are in `docs/results/`. Container CPU and memory are also live in Grafana, dashboard **Containers (cAdvisor)**, fed by cAdvisor and Prometheus (<http://127.0.0.1:9092>).

## 7a. Turn on fire spread between rooms (off in the demo)

The demo rooms are isolated: no `DOORS` list is configured, so a fire stays in its room and the responder lists no at-risk rooms. To connect two rooms, add the pair to **both** `physics-sim` and `responder` in `docker-compose.yml`, then `docker compose up -d physics-sim responder`:

```yaml
DOORS: "level0/1549|level0/1552A:0.05"   # roomA|roomB|openness (1 open, 0.05 closed door)
```

Expect the neighbour to arm and people there to be listed as at risk. Heat crosses doors as freely as smoke in this model, so with an open connection a neighbour's sprinkler can discharge.

## 7. Add a room

Add it to `ROOMS` in `physics-sim` (`level/room:volume_m3:sprinkler_id`), and to `ROOMS` of the three sensor services (`sensor-smoke`, `sensor-co`, `sensor-temperature`) and `sprinkler-actuator`, then `docker compose up -d`. It then appears in the console's room list.

## 8. Run the tests

See [`../DEVELOP.md`](../DEVELOP.md). Unit: `cd src && go vet ./... && go test -race ./...`. System tests need `BUILDSIM_BIN`.

## 9. Build the report

```bash
sudo tlmgr install newunicodechar pgfplots titlesec xurl   # once
make -C docs/diagrams                  # D2 -> SVG and PDF with the course theme (needs d2 and rsvg-convert)
cd report && pdflatex final_report.tex && pdflatex final_report.tex
```

## Troubleshooting

| Symptom | Cause |
|---|---|
| `required variable D7065E_DIR is missing` | set it in `.env` |
| port 5432 busy | not ours; TimescaleDB is on 55432 |
| viewer shows every component down | broker unreachable (liveness comes from MQTT heartbeats) |
| console room list empty | physics-sim not up yet; it fills on the next refresh |
