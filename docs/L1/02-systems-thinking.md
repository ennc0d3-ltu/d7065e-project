# L1 — Systems thinking

## Boundary

- **Inside (v1, after scope cuts S1–S6):** physics simulator, sensor gateway, sprinkler actuator, detector, responder service (route, occupancy status, dispatch), BuildSim bridge, storage writer, dashboard.
- **Shared world:** BuildSim (geometry, device registry, occupancy collection) and the MQTT broker.
- **Outside:** the real fire (simulated by our physics simulator); the people (simulated by course `occupancysim`, D12 revised); responders (mock).

## Feedback loops

| Loop | Type | Note |
|---|---|---|
| Sprinkler → smoke/temp fall → score falls → release | Balancing | Core loop. Latching (D6) deliberately breaks it so a falling score cannot switch sprinklers off early |
| Fire grows → smoke spreads to adjacent rooms → more rooms alarm | Reinforcing | A closing fire door slows it (D7) |
| Route → people move → occupancy changes → planner re-routes | Balancing | **Deferred (S1):** no evacuee motion in v1, so this loop is open. Design kept: oscillation risk, needs hysteresis (D11). Restore if people movement during a fire is added |
| Warning persists → escalation → actuation | Reinforcing | By design; a persistent false warning becomes a false actuation (H6) |

## Timescales

Detector and actuators: seconds. Planner and occupancy tracker: seconds to tens of seconds. Dashboard and storage: minutes. Slow components must never sit in the fast path (D1).

## Hidden couplings

- Course `occupancysim` is the single writer of `PUT /api/occupancy` and `PUT /api/entities`; all others read only (D12 revised). Its clock and ours must agree: our simulator drives `POST /api/clock` (D5).
- Detector, actuator fallback and baseline threshold share limits → one config source.
- Responder service depends on occupancy read from BuildSim: stale occupancy gives responders unreliable status.
- The simulator is a single point of failure for the experiment (not for a real deployment).

## Degradation matrix (each row is a fault-injection test)

| Component down | System behaviour |
|---|---|
| Dashboard | No safety effect |
| TimescaleDB | Detection continues; history buffered; dashboard loses history |
| Responder service | Response continues (sprinklers do not depend on it); dispatch and route display are lost, dashboard shows the component down |
| `occupancysim` | Occupancy goes stale; responder status says "unknown"; detection and sprinklers unaffected |
| Detector | Latched sprinklers stay on; actuator-local thresholds take over (D6) |
| Broker | Actuators use local fallback; sensors buffer readings |
| Sensor or actuator type | Missing data flagged; warning raised; other types keep working |
