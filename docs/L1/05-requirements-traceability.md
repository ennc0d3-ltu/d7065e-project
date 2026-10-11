# L1 — Requirements traceability (draft for report §3, §10)

Format follows `final_report_template.pdf` §3 and §10 and the worked example: each requirement has an ID, type, priority (MoSCoW), measurable acceptance criterion, the design element that satisfies it, and the test that verifies it. IDs match `01-requirements.md`; **NFR12 and NFR13 are new** (data quality, scale), added because the guide grades data-quality handling and the worked example tests scale.
All numbers are starting values *(tune)* once scenarios exist. All times are sim time (D5). Containers are the v1 set after scope cuts S1–S6.

## 1. Requirements

| ID | Type | Requirement | Priority | Acceptance criterion | Design element | Verified by |
|---|---|---|---|---|---|---|
| FR1 | Functional | Instrumented rooms publish smoke, CO and temperature at a fixed rate with sequence numbers and sim-time timestamps | Must | ≥ 99% of expected messages arrive in a 10-min fault-free run; every message carries `seq` and `ts` | `sensor-gateway`, `mosquitto` (D1, D8) | T1 |
| FR2 | Functional | Detector fuses the three signals per room, using rolling features, into a score and a tier (`none`, `warning`, `armed`, `fire`): a learned model (isolation forest) with the rule baseline as a safety floor and fallback, and a heat gate (smoke and CO alone arm a room, heat discharges it) (D3, D6, D9) | Must | Tier matches the labelled ground truth on held-out scenarios at the NFR2 rates; the deployed classifier is compared with the rules and with the model alone | `detector` (D3, D9) | T2, T9 |
| FR3 | Functional | `warning` and `armed` pre-alert the dashboard and responders; `fire` triggers the sprinkler, computes the evacuation route and notifies responders | Must | On a fire scenario, sprinkler `on` and a fire dispatch logged; on a warning-only scenario (cooking, dust), no actuator command | `detector`, `sprinkler-actuator`, `responder` (D3) | T3 |
| FR4 | Functional | Escalation follows the mode (`persistence`, `operator`, `both`); operator mode times out to persistence; operator may confirm early or cancel but is never required | Must | With no operator input, escalation happens at timeout T; with cancel before T, no sprinkler command on a benign scenario; every operator action audited | `detector`, `dashboard` (D4) | T4 |
| FR5 | Functional | Responder service reads occupancy and entities from BuildSim (read-only) and classifies each person who was in an affected room as in danger, en route (walking out), safe or unaccounted for (in no room and not walking), and each person in an adjacent room as at risk | Must | For a fixed occupancy snapshot the classification matches the expected table; stale occupancy shows as unknown; a person walking out of an affected room is en route, not missing | `responder` (D12, S1, S2) | T5 |
| FR6 | Functional | Responder service sends the affected rooms, severity, the occupancy classes and the routes to the mock responder as a dispatch, sends another whenever the situation changes (fire, update, all-clear), and logs each acknowledgement | Must | A dispatch record with all fields exists for the fire, for each change and for the all-clear, each with an acknowledgement entry | `responder` (S2) | T5 |
| FR7 | Functional | Sprinklers latch on; release only on all-clear or operator action | Must | After `fire`, killing the detector or dropping the score to 0 leaves the sprinkler on; it releases only on an all-clear or operator command | `sprinkler-actuator` (D6) | T6 |
| FR8 | Functional | Egress doors are locked by default and unlock for a room in alarm without delay; fail safe when the detector is silent, at start and at stop. Closing containment doors is deferred | Should | Locked at rest and the route out blocked; on an alarm only that room's door unlocks and the route opens; relocks when the room is idle; doors unlock when the detector is silent for 30 sim s | `door-actuator`, detector door commands (D19) | Live check 2026-10-11; unit tests; system test still to be added |
| FR9 | Functional | Responder service computes the evacuation route when the affected set changes, with hysteresis; last route cached as fallback (D11). The route is shown, and walked by the people in the affected room through the evacuation proxy (D16) | Should | Route recomputed within 5 s of an affected-set change; no more than one route change per 30 s; cached route served if routing fails; the route never crosses another burning room | `responder` (D11) | T7 |
| NFR1 | Performance | Detection-to-sprinkler latency, measured from the end of the confirmation window (the fire tier has held for the 15 sim s persistence window) to sprinkler state `on` in BuildSim, for fast and medium fires | Must | median <= 10 s, p95 <= 20 s *(tune)*. **Measured 2026-10-10 (20 runs): median 11.3 s, p95 30.3 s, not met**; measured from the first fire-tier sample the figures are 25.6 s and 44.5 s. The pipeline itself (command to sprinkler on) takes 0.04 s wall; the persistence window and the decision period account for the rest | `mosquitto`, `detector`, `sprinkler-actuator` (D1) | T8 |
| NFR2 | Accuracy | Detector quality on held-out runs: 30 fire runs (slow, medium, fast; 10 seeds each) and 40 benign runs (normal, cooking, dust, smoke drifted from a fire elsewhere; 10 each) | Must | Recall >= 95 % on the fire runs; false sprinkler activation <= 5 % of the benign runs (at most 2 of 40) *(tune)*; reported against the baseline per D9 | `detector` (D9) | T9 |
| NFR3 | Availability | Detector or broker lost: actuators reach the safe state | Must | Detector silent for 30 sim s (the actuator's heartbeat limit): the actuator's own rules take over; broker lost: readings still reach the actuator over the direct HTTP path | `sprinkler-actuator` local fallback, heartbeat, TTL (D6) | T10, T11 |
| NFR4 | Recovery | Any process that crashes restarts and re-registers with BuildSim without manual steps | Must | Ready and publishing within 30 s of a crash (the test kills the process and starts the container again, because `docker kill` counts as a manual stop and Docker's restart policy acts only on a real crash) | Compose `restart`, health checks (D10) | T12 |
| NFR5 | Auditability | Every decision, command and operator action is logged and stored with sim time, room and reason | Must | 100 % of commands, decisions, dispatches and operator actions in a run have a stored record with sim time, room and reason; the stored records show why each sprinkler command was sent | all processes, `db-writer` (S4) | T13 |
| NFR6 | Availability | Multi-failure tolerance per the degradation matrix in `02-systems-thinking.md`: the core loop (sensors, broker, detector, sprinkler) survives any one non-core component down | Must | For each matrix row the stated behaviour holds and the core loop keeps working | whole system | T14 |
| NFR7 | Resilience | No cascade: every inter-process call has a timeout, and callers retry or re-register, so a slow or dead BuildSim does not stop another process. Circuit breakers are not part of v1 | Should | BuildSim restarted or unreachable: no other process crashes and every process re-registers within 3 s without manual steps (T15); a test with BuildSim answering slowly (5 s) is still to be run | shared client library | T15 |
| NFR8 | Operability | Shared health library: `/healthz`, `/readyz`, heartbeat | Should | All v1 processes expose the three; heartbeat missing for 2 intervals marks the component down | shared library (S4) | T16 |
| NFR9 | Observability | Traceability and metrics: a reading can be traced to the decision and command it caused by room, sensor id, sequence number and sim time; message gaps are counted at the bridge; end-to-end latency is measured by the evaluation tool. No single correlation ID is carried, and latency is not exported as a live metric | Should | One fire can be followed through the stored decision, command and dispatch records by room and sim time; the gap count is available; `cmd/measure latency` reports the latency distribution | all processes (S4) | T13 |
| NFR10 | Recovery | Restarted process rebuilds its state from the live MQTT stream, BuildSim or TimescaleDB | Should | After a restart no manual step is needed: the detector classifies correctly once its 60 s feature window has refilled, and the sprinkler actuator adopts the state BuildSim holds | `detector`, `responder` | T12 |
| NFR11 | Operability | One dashboard panel shows which components are down | Should | Panel updates within 2 heartbeat intervals of a component stop or start | `dashboard` (S4) | T16 |
| NFR12 | Data quality | Missing, stale, duplicate or out-of-order readings are handled explicitly | Must | A reading older than the staleness limit (15 sim s) is flagged missing and raises a warning, never `normal`; duplicates dropped by `seq`; gaps counted | `detector`, `db-writer` (H7) | T17 |
| NFR13 | Scale | Decision rounds keep up as instrumented rooms grow | Should | With 50 instrumented rooms and a 10x reading burst, one decision round over all rooms takes <= 10 % of the decision period (0.5 s wall at 10x) *(tune)*; the largest room count and reading rate before it fails are reported | `mosquitto`, `detector` | T18 |

**Not requirements:** operator override is allowed but never required (FR4); an LLM is optional and off the critical path (`03`, "LLM use").

## 2. Hazard → requirement trace

| Hazard (`01`) | Requirements |
|---|---|
| H1 missed real fire | FR2, FR4, NFR2 |
| H2 detector dies mid-fire | FR7, NFR3, NFR6 |
| H3 operator cancels a real fire | FR4, NFR5 |
| H4 door traps people | FR8 (deferred), D7 |
| H5 two writers erase occupancy | D12 (design rule, checked by T5) |
| H6 false sprinkler activation | FR3, NFR2 |
| H7 message lost or delayed | NFR12, NFR3, NFR7 |

## 3. Test plan (draft for report §10)

| ID | Test | Requirement | Expected result / evidence |
|---|---|---|---|
| T1 | Run 10 min fault-free; count messages and `seq` gaps | FR1 | ≥ 99% received; each has `seq`, `ts` |
| T2 | Replay labelled scenarios (normal, cooking, dust, slow/medium/fast fire) through the detector | FR2 | Tier per scenario recorded |
| T3 | Fire scenario vs warning-only scenario | FR3 | Sprinkler command and one dispatch only on fire |
| T4 | Run each escalation mode with and without operator input; cancel a benign case | FR4 | Timeout fallback fires; cancel respected; audit entries |
| T5 | Feed a known occupancy snapshot; check status and dispatch fields; assert only `occupancysim` writes occupancy | FR5, FR6, H5 | Table matches; one writer |
| T6 | `docker kill detector` during a fire; send low scores | FR7 | Sprinkler stays on; releases only on all-clear |
| T7 | Change the affected set repeatedly; break the route computation | FR9 | Hysteresis holds; cached route served |
| T8 | Run 20 fire scenarios, measure sample-to-actuator latency | NFR1 | Latency distribution (median, p95) |
| T9 | Held-out fire and benign runs, learned model vs rule baseline | NFR2, FR2 | Recall, false-activation rate, detection delay against baseline |
| T10 | Stop the detector; time to actuator safe state | NFR3 | ≤ 2 × heartbeat |
| T11 | Stop the broker; time to actuator local fallback | NFR3 | ≤ 2 × heartbeat; sensors buffer |
| T12 | `docker kill` each v1 container in turn | NFR4, NFR10 | Recovery time ≤ 30 s; state rebuilt |
| T13 | Trace one reading to the actuator by correlation ID; replay the log | NFR5, NFR9 | Complete chain; identical tiers on replay |
| T14 | Walk every row of the degradation matrix | NFR6 | Stated behaviour for each row |
| T15 | Slow or stop BuildSim | NFR7 | No crash; breaker opens and closes |
| T16 | Stop and start components; watch the status panel | NFR8, NFR11 | Panel and heartbeat reflect state in time |
| T17 | Inject stale, duplicate, out-of-order and missing readings, and a stuck sensor | NFR12 | Flagged as stated; no false "normal" |
| T18 (stress) | Scale to 50 instrumented rooms; 10× reading burst | NFR13 | Decision interval vs room count curve |

## 4. Status against the requirements (reviewed 2026-10-10)

Evidence is in `docs/results/` and report Section 11. The requirement wording in section 1 is **not** changed here: points where it is out of date are listed in the right-hand column and are open for a decision.

| ID | Status | Evidence | Wording problem or gap |
|---|---|---|---|
| FR1 | Met, partly scripted | Sequence numbers, sim-time stamps, gap accounting tested; 1 sample per sim second | T1 (10-minute fault-free count) not scripted; "fixed rate" is not stated |
| FR2 | Met | Held-out evaluation: recall 1.00, no false alarms; four tiers (none, warning, armed, fire) | Wording updated (four tiers) |
| FR3 | Met | System test T3: fire discharges, cooking and dust never do | Wording updated (dispatches) |
| FR4 | Met | Unit tests and the operator API | none |
| FR5 | Met and exceeded | In danger, en route, at risk, safe and unaccounted for, seen live (104 people) | Wording updated (en route, unaccounted) |
| FR6 | Met | Dispatch and acknowledgement stored | Wording updated (fire, update, all-clear) |
| FR7 | Met | System test T6 | none |
| FR8 | Met for egress unlock | Live check 2026-10-11 (locked at rest, route blocked; unlocks on the alarm, route opens; relocks at idle); unit tests | Wording updated 2026-10-11 (D19). Containment closing is deferred; no automated system test yet |
| FR9 | Met | Unit tests; the evacuation proxy now walks people on the route | Wording updated (evacuees walk the route) |
| NFR1 | **Not met** (as restated) | 20 runs: median 25.6 s, p95 44.5 s | Restated 2026-10-11 from the end of the persistence window, targets unchanged: median 11.3 s, p95 30.3 s |
| NFR2 | Met | Recall 1.00, false activation 0.00 on 30 fire and 40 benign held-out runs | Wording updated (the sets are defined); the benign set is probably too easy |
| NFR3 | Met | System tests T10, T11 | Wording updated (30 sim s of detector silence) |
| NFR4 | Met | Ready again in 0.2 to 0.6 s | Wording updated (crash, not `docker kill`) |
| NFR5 | Met as restated | Decisions, commands, dispatches and operator actions are stored with sim time and reason | Restated 2026-10-11: no correlation ID and no replay claim; records carry sim time, room and reason |
| NFR6 | Partial | Covered by T6, T10 to T12, T17 | Wording updated; the matrix is still not scripted as T14 |
| NFR7 | Partial | Timeouts on every client; retry and re-registration loops; BuildSim restart tested (T15) | Restated 2026-10-11 to timeouts and retries; **circuit breakers not built**; the slow-BuildSim (5 s) test is still to run |
| NFR8 | Met for the core processes | `/healthz`, `/readyz` and heartbeat | evac-proxy, registry and scenario-console have health endpoints but no heartbeat |
| NFR9 | Met as restated | Gaps counted at the bridge; latency measured offline by `cmd/measure` | Restated 2026-10-11: traced by room, sensor id, seq and sim time; **no correlation ID, no live latency metric** |
| NFR10 | Met as restated | The detector and responder rebuild from the live stream and BuildSim | Wording updated (live stream, BuildSim, TimescaleDB; not retained MQTT) |
| NFR11 | Met | The dashboard raises a component-down alert from heartbeats | T16 not scripted |
| NFR12 | Met | T17 with stuck, offline and drifting sensors; duplicates dropped by sequence number | Wording updated (15 sim s staleness limit) |
| NFR13 | Met | 11 ms per decision round at 5000 rooms; broker path saturates near 23 000 readings/s | Wording updated (round duration is the metric) |
| NFR14 | Designed, not built | `06-device-layer-design.md` | New requirement for the device layer: a failure visible in the console within 10 s |

## 5. Checks before using this in the report

- Every Must/Should requirement above has a test; FR8 is the only requirement without one, by design (Won't in v1).
- Every number is a starting value; fix them once the first scenarios run, then record any change in the traceability status table.
- NFR13 and NFR12 are additions, not in the original proposal; list them under "changes since the proposal".
