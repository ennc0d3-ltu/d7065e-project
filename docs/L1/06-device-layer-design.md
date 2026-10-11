# L1 — Device layer: asset and device model (designed, not built)

Status: **design only** (decision D18), settled 2026-10-10. What runs today is
decision D17: three sensor processes (`sensor-smoke`, `sensor-co`,
`sensor-temperature`) that sample ground truth, degrade it and publish to MQTT.
This note records the next step so that it can be built without redesign and so
the report can state it.

## 1. Model

The model follows the asset and device split used by IoT platforms (ThingsBoard).

| Concept | In this system | Notes |
|---|---|---|
| **Asset** | a room (`level0/A1008`) | logical; owns no hardware; the unit the detector decides on |
| **Device** | a sensor node: its own process and its own BuildSim equipment record | physical, battery powered, can fail alone; one or several sensing elements |
| **Telemetry** | smoke, CO, temperature, and the device's own health | |
| **Relation** | "room contains device" | the deployment descriptor |

A big conference room may hold several devices at different places: two smoke
detectors, two CO sensors, a multi-sensor node. **One process per device.**

### Deployment descriptor

One file lists rooms and their devices; a generator turns it into compose
services, so a new scenario is a data change and not a code change.

```yaml
rooms:
  level0/A1008:
    devices:
      - { id: a1008-n1, sensors: [smoke, co, temperature], position: [210, 22] }
      - { id: a1008-n2, sensors: [smoke],                   position: [260, 40] }
```

## 2. Data path

```
device --CoAP--> sensor gateway A --MQTT ingest/A/...--\
       \-CoAP--> sensor gateway B --MQTT ingest/B/...---> broker --> de-duplicator --> sensors/... --> bridge, db-writer, room-aggregator, dashboard
                                                          broker --> detector, sprinkler-actuator (raw ingest, their own de-duplication)
```

- **CoAP** between devices and gateways: small, UDP, suited to constrained devices.
- **Two gateways, both active.** A device sends every reading to both. A
  gateway validates and publishes it unchanged on `ingest/<gateway>/...`, and
  keeps the direct HTTP path to the actuator (D6, D13). Losing either gateway
  loses nothing and needs no switch-over.
- **De-duplicator** (a service after the broker): forwards the first copy of
  each reading (key: sensor id and sequence number, with a small window for
  out-of-order arrival and for a device restart that resets the sequence) to
  the existing canonical topic `sensors/<level>/<room>/<type>`, and drops the
  second. It publishes statistics: duplicates dropped, readings per gateway,
  time since each gateway last delivered.
- **Option B (decided): the de-duplicator is not on the safety path.** The
  detector and the sprinkler actuator read the raw ingest topics and
  de-duplicate with the same shared package, so a crash of the service never
  blinds the sprinkler. The service serves everything else.
- A device registers itself in BuildSim (one equipment record, one sensor per
  element) and re-registers every 3 s, as today.

## 3. Fusion within a kind

A shared `fusion` package, used by the detector's feature step and by the
sprinkler actuator's local fallback so the two cannot disagree. It applies when
a room has several devices of one kind; cross-kind fusion (rules, isolation
forest, heat gate) is unchanged.

1. **Freshness:** a device not heard from within the limit (15 s) is dropped.
2. **Plausibility:** a device reading flat for a long time, out of physical
   range, or far from its neighbours is **flagged**. A flagged device **stays in
   the vote** (decided: for smoke and CO, dropping an alarming sensor is the
   riskier mistake); the flag goes to the console.
3. **Combine:** the **highest** fresh value (a missed fire is worse than a false
   alarm; one stuck-low device cannot hide a fire; no minimum number of devices).
4. **Slopes:** each device's slope is computed from its own history and the
   **highest slope** is used. Taking the highest value at every instant would
   switch device mid-window and produce a fake slope spike.
5. If every device of a kind is stale, the kind is stale (a warning, never
   normal), as today.

Cost: one drifting-high device can raise a warning. The heat gate and the 15 s
persistence window still protect the sprinkler from a false discharge.

## 4. Room aggregator (observability, off the safety path)

Subscribes to the de-duplicated readings, runs the same fusion package and
publishes a **room state** on `rooms/<level>/<room>/state`: fused smoke, CO and
temperature; each device's last value, age, battery, state and flags; coverage
per kind (fresh devices of total, and whether one more can fail); disagreement
between devices; degraded when a kind has no fresh device. The db-writer stores
it (`room_state`). **The detector does not depend on it.** Test: kill it during
a fire; the sprinkler still comes on.

## 5. Device health

Each device reports health with its heartbeat. **Basic models only.**

- **Battery.** Starts at 100 %. Normal drain is realistic and slow (a base drain
  plus a small cost per transmission), so it does not affect a demo or a
  measurement run. A **drain fault** speeds it up so a battery can be shown dying
  in minutes. Below 20 % an alert shows; below 5 % readings get noisier (low
  battery degrades data quality before the device goes silent); at 0 % the
  device is silent. A **replace battery** action restores 100 %.
- **Link.** A signal strength per device-to-gateway link (dBm). Packet loss rises
  as the signal weakens; **loss only, no delay**. Links to gateway A and B are
  independent, so one weak link costs a few readings and not the room. Fault
  injection can degrade or cut one link. Each device counts readings sent; the
  de-duplicator counts arrivals per gateway; the difference is the measured
  delivery ratio.
- **Maintenance state:** in service, in maintenance (the device is excluded
  without raising a stale alarm), replaced.

### Device-health model

A simple statistical model, off the safety path, console-only (shown in the
health API and the Health tab, never in a decision). It may flag and advise, and
**never lowers a tier or removes a device from the vote.**
- **Neighbour residual:** a robust estimate of the room's typical value per kind;
  a device far from it for long enough is flagged (stuck, drifting,
  miscalibrated). Needs several devices in the room.
- **Battery remaining life:** a linear fit of the drain history gives "replace in
  about N hours".
- **Link health** is a threshold on the delivery ratio, not a model.
It is evaluated honestly: time to flag an injected stuck sensor, and the rate of
flags on healthy devices.

## 6. Console: Health tab

The scenario console gets a second tab, **Health**, fed by a small read-only
health API (`/components`, `/rooms`, `/devices`) that is built from heartbeats,
the room state and the de-duplicator statistics. Off the safety path.
- **Components:** each process with heartbeat age, gateway A and B status,
  de-duplicator rate and duplicates, outbox depth, dead letters, BuildSim and
  database reachability, the active tuning bundle version, CPU and memory.
- **Rooms:** fresh devices per kind, redundancy left, flags.
- **Devices:** battery, last seen, link A and B, health-model note.
- **Actions:** replace battery, set maintenance.
- **Alerting:** a **banner only**, when a room loses its last fresh device of a
  kind. Otherwise status colours.

## 7. New requirement

NFR14: a failed component or silent device is visible in the console within
**10 s** (wall clock) of the failure. Measured by killing it and timing the
Health tab, in the style of the recovery table.

## 8. Failure experiments this design makes possible

| Experiment | Expected behaviour |
|---|---|
| kill gateway A during a fire | gateway B carries on; nothing lost; fire caught; visible in the console |
| kill one device | the other devices of its kind carry the room; coverage shown |
| kill the only device of a kind | that kind is stale (warning, never normal); other kinds still decide |
| drain a battery | readings degrade, then stop; the alert shows first |
| stuck-low device beside a healthy one | the highest value wins; fire caught; the flag shows |
| kill the de-duplicator mid-fire | the bridge, database and dashboards lag; the sprinkler is unaffected |
| kill the room-aggregator mid-fire | the room view is gone; the sprinkler is unaffected |
| cut one link, or CoAP loss on both | readings thin out; the other gateway and the direct actuator path limit the impact |

## 9. Known limits

- A room is a single well-mixed volume in the physics, so devices in one room see
  the same truth plus their own noise and faults; they do not see a fire reach
  the far corner later. A distance-based lag per device is a small optional
  addition.
- "Highest" trades some false warnings for safety.
- Two gateways and two ingest paths on one laptop share a host: this shows the
  failover logic, not independence from a host failure.
- The de-duplicator and the consumers both de-duplicate (option B): a little
  duplicated work, in return for no single point of failure.

## 10. Why it is deferred

The quantitative evaluation (latency, scale, recovery, resource use) was
missing, and the grading emphasises evidence over breadth. The model is
documented so the report can state it as the evolution of the device layer.
