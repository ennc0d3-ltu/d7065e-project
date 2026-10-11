# Developing

Go code lives in `src/` (module `github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src`):
`cmd/<name>` is one container each, `internal/<pkg>` is the logic, `systest/`
runs the real processes together.

## Tests

```bash
cd src
go vet ./... && go test -race ./...                         # unit tests, no services needed
```

Database tests need TimescaleDB and skip otherwise:

```bash
docker compose up -d timescaledb
TEST_DATABASE_URL="postgres://fire@127.0.0.1:55432/fire?sslmode=disable" go test -race ./internal/store/
```

System tests start the real binaries, an in-process MQTT broker and the real
BuildSim, then kill things (fire vs benign, detector dead, broker dead,
BuildSim restart, failed sensors, outage replay). Build BuildSim from the course
repo first:

```bash
(cd $D7065E_DIR/buildingsim && go build -o /tmp/buildsim ./cmd)
BUILDSIM_BIN=/tmp/buildsim go test -tags system -timeout 15m -v ./systest/
```

Evaluation numbers (recall, false alarms, delay, warning lead time) are printed
by `go test -v -run RulesModelAndDeployed ./internal/eval/`.

## Working on the code

- **Test first.** Write the failing test, watch it fail for the right reason,
  then make it pass. Several real bugs were found only by the system tests
  (a persistent smoke warning flooding a kitchen, BuildSim restarts leaving the
  system blind), so add a system test for anything about failure behaviour.
- **Decisions are recorded.** A change that overturns a design decision goes in
  `docs/L1/03-decisions.md` (the old row stays).
- **Safety rules to keep:** the rule baseline is a floor under the model;
  smoke and CO arm a room but heat discharges it; sprinkler commands and
  heartbeats are never queued for replay; only readings may be shed from a full
  outbox; BuildSim collections that a `PUT` replaces have exactly one writer.

## The model

`go run ./cmd/trainer -out model.json` retrains it (deterministic: fixed seed
and fixed training runs, which never contain a fire). The detector image does
this at build time, so the shipped model is the evaluated model.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `required variable D7065E_DIR is missing` | copy `.env.example` to `.env` and set the path; every `docker compose` command needs it |
| port 5432 already in use | not ours: TimescaleDB is published on 55432 |
| the viewer lists every component as down | the broker is unreachable; the dashboard learns liveness from MQTT heartbeats |
| a fresh demo shows no history | `docker compose down -v` also deletes the database volume |
