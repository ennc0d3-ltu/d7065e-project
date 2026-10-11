# Project Proposal (Week 2) — Fire Detection & Autonomous Emergency Response

D7065E, Week 2 Proposal

## Summary

| | |
|---|---|
| **Team** | Nagarajan Ganesan (naggan-4) |
| **Use case** | Fire detection and autonomous emergency response |
| **Pitch** | Detect real fires (not false alarms) from correlated smoke/CO/temperature readings, and autonomously trigger sprinklers, fire-door control, evacuation routing, and emergency-responder notification. |
| **Repository** | https://github.com/ennc0d3-ltu/d7065e-project |

## 1. Use case, and why

This project detects fire in a BuildSim office floor and drives an autonomous emergency
response: sprinklers, fire-door control, and evacuation guidance. It was chosen because it has
a genuine tension — reacting fast to a real fire versus not triggering sprinklers and lockdowns
on cooking smoke or dust — which makes the detection decision interesting, and because
BuildSim's room adjacency and walkable graph give a natural substrate for both smoke spread
and evacuation routing.

## 2. Scenario

Smoke concentration rises in one room. CO and temperature begin rising shortly after in a
correlated way. The system fuses the three signals, confirms a real fire (rather than a cooking
or dust false alarm), and responds: sprinklers activate in the affected room, nearby fire doors
unlock, an evacuation route is computed away from the affected zone, and a simulated call is
placed to the building's emergency-response team (fire brigade / security) with the affected
room, severity, and a live occupancy status — who is still in the affected zone, who has reached
a safe area, and who is unaccounted for — updated as people move. The 3D dashboard
highlights the room red, the spread risk orange, and the safe route green. As smoke and heat
recede, sprinklers deactivate.

## 3. What will be built (rough sketch)

- **Sensors:** smoke, CO, and temperature per room, registered with BuildSim.
- **Physical simulator:** advances per-room smoke/CO/temperature and occupancy dynamics,
  reads actuator effects, and generates the underlying "ground truth" that sensors sample.
- **Autonomous decision-maker:** fuses smoke/CO/temperature to decide real-fire vs.
  false alarm, and commands the response.
- **Actuators:** sprinklers and fire-door locks, writing state back to BuildSim.
- **Evacuation planner:** computes a route away from affected rooms using BuildSim's
  walkable graph, re-routing as the affected zone changes.
- **Occupancy tracker:** reads BuildSim's occupancy/entity data per room to maintain a
  live status — in danger zone, en route, safe, unaccounted-for — feeding both the dashboard
  and the notifier.
- **Emergency-response notifier:** on a confirmed fire, alerts a simulated emergency-response
  team (fire brigade / building security) with affected room, severity, and the current
  occupancy status, so responders know who still needs help and where; logs
  acknowledgement/response status.
- **Data pipeline / storage:** collect sensor readings and store recent history in
  TimescaleDB (PostgreSQL time-series extension), feeding both the detector and the dashboard.
- **Dashboard:** room highlights (red/orange/green), affected zones, and evacuation route
  overlaid on the BuildSim 3D view.

Data flow (first version): simulator -> sensors -> BuildSim -> TimescaleDB -> detector ->
actuators -> simulator, with the dashboard reading BuildSim state plus selected history.

Each major component (sensors, detector, actuators, evacuation planner, occupancy tracker,
notifier, storage, and dashboard) runs as a separate process communicating through defined
interfaces, and talks to BuildSim through its API. Each process is independently restartable and
re-registers with BuildSim on restart; requests use timeouts and retries so a slow or crashed
component degrades the system rather than freezing it. The occupancy tracker is the single
owner of `PUT /api/occupancy` — since it replaces the whole collection, exactly one process
may write it, to avoid two writers erasing each other's updates.

## 4. The autonomous part

It decides, per room, whether readings indicate a real fire and what response to trigger.
Plain thresholds alone are not sufficient (false alarms from cooking/dust need to be rejected
with real confidence), so the data-driven component is required rather than optional here:
**starting approach** is a lightweight anomaly/fusion model — e.g. a one-class or isolation-forest
model trained on normal multi-sensor (smoke/CO/temperature) patterns, so a real fire is flagged
as a joint deviation rather than any single threshold crossing. This still runs as a fast/reflex
decision: local, bounded, no external calls, always available. Plain correlated thresholds are
the fallback if training data proves too thin, but the model-based approach is the target for
the first working loop, not an optional upgrade.

## 5. Biggest unknowns & risks

- Whether the simulated smoke-spread between adjacent rooms is realistic enough that
  detection and evacuation decisions mean anything.
- Tuning thresholds so that correlated cooking/dust events don't trigger a false response,
  without missing a real fire.
- Whether evacuation re-routing against BuildSim's walkable graph is fast enough to update
  as the affected zone grows.
- What a realistic emergency-responder notification interface looks like when there is no real
  external service to call (a mock endpoint, or a logged/simulated dispatch).
- Whether BuildSim's occupancy/entity data is granular and timely enough to reliably classify
  people as "in danger," "en route," or "safe" during a fast-moving scenario.

## 6. Physical simulation approach

BuildSim provides the rooms; the physics and the people are simulated as follows.

- **Physical processes:** a simple per-room model — smoke concentration rises with fire
  intensity and spreads to adjacent rooms through corridors/doorways using the room
  adjacency graph; temperature rises with fire intensity; CO rises correlated with smoke.
  Sprinkler activation reduces smoke concentration and temperature in the room, closing the
  feedback loop.
- **People movement (occupancy):** a standard weekday schedule (arrivals 7–9am, lunch
  11:30–13:00, departures 16:00–18:00, empty weekends), with some randomness, so occupancy
  data is available to inform evacuation routing. During an evacuation, people are moved
  along BuildSim's walkable graph toward the planned safe route, so their live room/position
  can be read back and used to classify each person's status for the occupancy tracker.
- **Public dataset (baseline reference):** no dataset in the course-provided list is
  fire-specific, so smoke/CO/temperature dynamics are physics-modelled rather than replayed.
  The UCI/Kaggle **Occupancy Detection** dataset (light, temperature, humidity, CO2 with
  ground-truth occupancy) is used as a reference for realistic "normal, no-fire" sensor
  baselines and occupancy correlation, which the fire-vs-false-alarm model is trained against.

The models are intentionally simple, but actuator actions (sprinklers) must influence future
sensor readings so the control loop closes.

## 7. First step & rough plan

The first step is **one room working end-to-end**: smoke/CO/temp sensors → detector →
sprinkler actuator → BuildSim, to prove the fire-vs-false-alarm loop closes and sprinklers
visibly reduce smoke/temperature. After that: smoke spread to adjacent rooms, fire-door
control, the evacuation planner, occupancy tracker, the emergency-response notifier, storage,
and the dashboard are added, with failure handling and evaluation (fault injection, false-alarm
testing) following toward the demo.
