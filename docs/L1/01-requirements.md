# L1 — Requirements

Fire detection and autonomous emergency response on BuildSim (D7065E).
Source: `docs/proposal.md`, refined through design discussion. All times are **sim time** (D5).
Targets marked *(tune)* are starting values, to be revised once scenarios exist.

## 1. Priorities

1. **Safety**: never miss a real fire.
2. **Availability**: keep operating through component failures; degrade toward the safe action.
3. Reject cooking/dust false alarms.
4. Stay deliverable for one person.

## 2. Stakeholders

| Stakeholder | Needs |
|---|---|
| Occupants | Early warning, a safe route, not being locked in |
| Emergency responders (simulated) | Affected room, severity, who is unaccounted for |
| Building operator | Monitor; confirm or cancel (audited) |
| Developer / examiner | Evidence for every claim |

## 3. Hazards

| ID | Hazard | Mitigation |
|---|---|---|
| H1 | Model scores a real fire as normal, so no escalation | Persistence/rate-of-rise escalation (D3, D4); **the rule baseline is a safety floor under the model** (`detector.Deployed`), so the model can raise a tier but never lower the rules' (D3 amended 2026-10-07; measured: the model alone rated smoke 5 1/m and CO 500 ppm as normal); rule fallback (D6, D9). Residual risk: both detectors are tuned on our own simulator |
| H2 | Detector dies mid-fire | Latched sprinklers, command TTL, actuator-local fallback (D6) |
| H3 | Operator cancels a real fire | Audit log, limited cancel scope, timeout fallback (D4) |
| H4 | A locked or closed door traps people | Doors unlock on an alarm and fail safe (D19); a closed fire door stays unlocked (close, never lock, D7); `fire_exit` never locked. Containment closing is deferred. Residual: BuildSim keeps a door state if the actuator is killed |
| H5 | Two writers erase occupancy/entity state (both calls replace the whole collection) | Course `occupancysim` is the single writer of `PUT /api/occupancy` and `PUT /api/entities` (D12 revised); our responder service is read-only |
| H6 | False sprinkler activation (including a neighbouring room flooded on drifted smoke) | Two-tier response (D3); **pre-action heat gate**: smoke and CO arm a room, heat discharges it, with a 120 s persistence safety valve and a bypass when the heat sensor is down (see Open items in `03`) |
| H7 | Broker message lost or delayed | Sequence numbers detect gaps; stale reading = missing data (warning, never "normal"); commands QoS 1 + TTL + idempotent; actuator-local fallback covers broker loss |

## 4. Functional requirements

| ID | Requirement |
|---|---|
| FR1 | Each room publishes smoke, CO and temperature at a fixed rate with sequence numbers and sim-time timestamps |
| FR2 | Detector fuses the three signals per room, using rolling features, into a score and a tier (`none`, `warning`, `armed`, `fire`): a learned model (isolation forest) with the rule baseline as a safety floor and fallback, and a heat gate (smoke and CO alone arm a room, heat discharges it) (D3, D6, D9) |
| FR3 | `warning` and `armed` pre-alert the dashboard and responders; `fire` triggers the sprinkler, computes the evacuation route and notifies responders |
| FR4 | Escalation follows the configured mode: `persistence`, `operator`, `both` (default `both`). Operator mode falls back to persistence after timeout T. The operator may confirm early or cancel but is never required |
| FR5 | Responder service reads occupancy and entities from BuildSim (read-only) and classifies each person who was in an affected room as in danger, en route (walking out), safe or unaccounted for (in no room and not walking), and each person in an adjacent room as at risk |
| FR6 | Responder service sends the affected rooms, severity, the occupancy classes and the routes to the mock responder as a dispatch, sends another whenever the situation changes (fire, update, all-clear), and logs each acknowledgement |
| FR7 | Sprinklers latch on; release only on all-clear or operator action |
| FR8 | Egress doors are locked by default and unlock for a room in alarm (the armed or fire tier) without delay, and fail safe (unlocked) when the detector is silent for 30 sim s, at start-up and at shutdown (D19, **built**). Fire doors close to contain smoke: **deferred**, reserved in D7 |
| FR9 | Responder service computes the evacuation route when the affected set changes, with hysteresis; last route cached as fallback (D11). The route is shown, and walked by the people in the affected room through the evacuation proxy (D16) |

## 5. Quality requirements

| ID | Requirement | Target |
|---|---|---|
| NFR1 | Detection-to-sprinkler latency, measured from the end of the confirmation window (the fire tier has held for the 15 sim s persistence window) to sprinkler state `on` in BuildSim, for fast and medium fires | median <= 10 s, p95 <= 20 s *(tune)*. **Measured 2026-10-10 (20 runs): median 11.3 s, p95 30.3 s, not met**; measured from the first fire-tier sample the figures are 25.6 s and 44.5 s. The pipeline itself (command to sprinkler on) takes 0.04 s wall; the persistence window and the decision period account for the rest |
| NFR2 | Detector quality on held-out runs: 30 fire runs (slow, medium, fast; 10 seeds each) and 40 benign runs (normal, cooking, dust, smoke drifted from a fire elsewhere; 10 each) | Recall >= 95 % on the fire runs; false sprinkler activation <= 5 % of the benign runs (at most 2 of 40) *(tune)*; reported against the baseline per D9 |
| NFR3 | Detector or broker lost: actuators reach the safe state | Detector silent for 30 sim s (the actuator's heartbeat limit): the actuator's own rules take over; broker lost: readings still reach the actuator over the direct HTTP path |
| NFR4 | Any process that crashes restarts and re-registers with BuildSim without manual steps | Ready and publishing within 30 s of a crash (the test kills the process and starts the container again, because `docker kill` counts as a manual stop and Docker's restart policy acts only on a real crash) |
| NFR5 | Every decision, command and operator action is logged and stored with sim time, room and reason | 100 % of commands, decisions, dispatches and operator actions in a run have a stored record with sim time, room and reason |
| NFR6 | Multi-failure tolerance per the degradation matrix in `02-systems-thinking.md`: the core loop (sensors, broker, detector, sprinkler) survives any one non-core component down | For each matrix row the stated behaviour holds and the core loop keeps working |
| NFR7 | No cascade: every inter-process call has a timeout, and callers retry or re-register, so a slow or dead BuildSim does not stop another process. Circuit breakers are not part of v1 | BuildSim restarted or unreachable: no other process crashes and every process re-registers within 3 s without manual steps (T15); a test with BuildSim answering slowly (5 s) is still to be run |
| NFR8 | Every process uses one shared health library: `/healthz`, `/readyz` and a heartbeat | all processes |
| NFR9 | Traceability and metrics: a reading can be traced to the decision and command it caused by room, sensor id, sequence number and sim time; message gaps are counted at the bridge; end-to-end latency is measured by the evaluation tool. No single correlation ID is carried, and latency is not exported as a live metric | One fire can be followed through the stored decision, command and dispatch records by room and sim time; the gap count is available; `cmd/measure latency` reports the latency distribution |
| NFR10 | Restarted process rebuilds its state from the live MQTT stream, BuildSim or TimescaleDB | After a restart no manual step is needed: the detector classifies correctly once its 60 s feature window has refilled, and the sprinkler actuator adopts the state BuildSim holds |
| NFR11 | One dashboard panel shows which components are down (from heartbeats); the dashboard is an overlay on the BuildSim viewer (S7) | live |

## 6. Constraints

Go; containers (Docker Compose); one process per component; MQTT broker; TimescaleDB; BuildSim is the shared world and is not modified. Single laptop deployment. Autonomous decision-making required; sophistication not graded.

**Scope (cuts accepted 2026-10-06, see D3, D7, D8, D9, D12):** about 10 containers for v1 — physics simulator, sensor gateway, broker, detector, DB writer + TimescaleDB, sprinkler actuator, BuildSim bridge, responder service, dashboard — plus course `occupancysim`. The detector uses a simple learned model (course requires a data-driven component), with the rule baseline as fallback. No evacuee motion; no door actuation. Build one room end to end first, then widen.
