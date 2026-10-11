# L1 — Physical model (draft for report §6)

Status: **draft**. All parameter values are assumptions, to be tuned so scenarios behave plausibly. Time is sim time (D5, our simulator is clock master).
Style follows the course example (lumped heat and mass balance per room) and borrows the zone-model idea from CFAST/CONTAM, without their detail.

## 1. Scope and layers

- One **ground-truth state per room**, stepped by our simulator. The detector never sees it directly (truth layer).
- Sensors sample the truth, then degrade it (noise, lag, stuck/offline). Only degraded values are written to BuildSim and published.
- Occupancy and movement come from the course `occupancysim` (D12 revised) via BuildSim and enter the model as `n_i(t)`. People do not react to the fire in v1.
- Fire, smoke and sprinklers can be drawn in the BuildSim viewer through `/api/room-layers` and `/api/effects` (`fire`, `smoke`, `sprinkler`, `water`). These are rendering of our truth, not a physics model.
- Actuator state is read back from BuildSim each tick; this closes the loop.

## 2. State per room *i*

| Symbol | Meaning | Unit |
|---|---|---|
| `T_i` | air temperature | °C |
| `K_i` | smoke extinction coefficient (obscuration source for the smoke sensor) | 1/m |
| `C_i` | CO concentration | ppm |
| `Q_i` | fire heat-release rate (0 if no fire) | kW |
| `s_i` | sprinkler state in [0,1], read from BuildSim | 1 |
| `a_ij` | door openness between rooms *i* and *j*, from door `state`/`lock_state` | 1 |
| `n_i` | head-count, from the people simulator | persons |

Step: Δt = 1 s sim time, explicit Euler. Per tick: read `s_i`, `a_ij`, `n_i` → update fire → update `T, K, C` → write truth to the sensor layer.

## 3. Fire source

A fire starts in room *f* at time `t0` with growth class α (t-squared fire):

```
Q_f(t) = min( α (t − t0)²,  Q_max )          [kW]
```

| Class | α [kW/s²] |
|---|---|
| slow | 0.0029 |
| medium | 0.0117 |
| fast | 0.0469 |

`Q_max` is the ventilation/fuel limit (assumed 1000 kW).

**Suppression.** Once the sprinkler has been on since `t_a`, the effective rate decays:

```
Q_eff(t) = Q_f(t) · [ 1 − s_i · (1 − exp(−(t − t_a)/τ_sup)) ]
```

with τ_sup ≈ 60 s (assumed). Sprinklers reduce, not instantly remove, the fire. The fire is out when `Q_eff` < 1 kW.

## 4. Per-room balance

For a scalar field `x ∈ {T, K, C}` the inter-room exchange term is the same form:

```
X_ij(x) = Σ_j (β a_ij / V_i) · (x_j − x_i)
```

`β` is the open-door exchange flow (assumed 1 m³/s), `V_i` the room volume. Door openness `a_ij`: open = 1, closed-unlocked = 0.05 (leakage), closed fire door = 0.02. The leak values are assumptions. This is a **mixing** model; buoyancy-driven flow through the door is ignored.

**Temperature** (heat balance):

```
dT_i/dt = (1/C_th) · [ k_loss (T_out − T_i) + q_occ n_i + Q_eff,i − q_spr s_i (T_i − T_wet) ] + X_ij(T)
```

| Parameter | Value (assumed) |
|---|---|
| `C_th` thermal mass of air + contents | 300 kJ/K (calibrated; 1000 gave under 3 °C rise in 90 s) |
| `k_loss` envelope + leakage loss | 2 kW/K |
| `q_occ` body heat | 0.1 kW/person |
| `q_spr` sprinkler cooling coefficient | 0.5 kW/K |
| `T_wet` sprinkler water temperature | 15 °C |
| `T_out` | 20 °C |

**Smoke** (mass balance):

```
dK_i/dt = g_s · Q_eff,i / V_i  −  (λ_vent + λ_set) K_i  +  X_ij(K)
```

**CO** (mass balance):

```
dC_i/dt = g_co · Q_eff,i / V_i  −  λ_vent C_i  +  X_ij(C)
```

| Parameter | Meaning | Value |
|---|---|---|
| `g_s` | smoke yield per kW | 0.019 (calibrated, 150 m³ room: medium fire passes 0.1 1/m in about 60 s) |
| `g_co` | CO yield per kW | 5.5 (calibrated: passes 100 ppm in about 90 s) |
| `λ_vent` | ventilation decay (ACH/3600) | 3 ACH → 0.0008 1/s |
| `λ_set` | smoke settling and deposition | 0.0002 1/s |

`g_s` and `g_co` are the main calibration knobs: they set how quickly a fire becomes detectable. Calibration targets are the plausible detector response times we want the scenarios to produce (NFR1).

**Stability.** Explicit Euler needs `Δt · β / V_i < 1`. For `V_i ≥ 20 m³`, `Δt = 1 s`, `β = 1 m³/s` this holds with margin.

## 5. Benign scenarios (D9)

The detector must reject these, so the simulator generates them with the same equations but a different source term:

| Scenario | Source |
|---|---|
| Cooking | `Q` = 5 kW for the window; smoke at 0.5× and CO at 0.05× the fire yield at that heat. Test-checked over 10 min in 150 m³: smoke ≥ 0.05 1/m, CO ≤ 20 ppm, rise ≤ 5 °C |
| Dust / steam | Constant smoke source only (`g_co = 0`, `Q = 0`); CO and temperature unchanged |
| Normal day | `n_i`-driven heat only; no `K`, no CO source |

## 6. Sensor model

```
reading(t) = truth(t − d) + ε,    ε ~ N(0, σ²)
```

- lag `d` ≈ 30 s (assumed), noise σ per sensor type
- fault modes: stuck at last value, offline (no value), drift (slow additive bias)
- units and ranges: °C, 1/m (or % obscuration), ppm

## 7. Closing the loop

1. The sprinkler actuator process moves toward its commanded state at a limited rate and writes `s_i` to BuildSim.
2. The simulator reads `s_i` and door states each tick, so `Q_eff` and `T` respond to the sprinkler within the next tick.
3. A closed fire door lowers `a_ij`, which slows the spread of `K` and `C` to neighbours (contained smoke, D7).

## 8. Limitations (for report §13)

- Single well-mixed volume per room: no hot upper layer, no smoke stratification (CFAST's two-zone model would add it).
- No buoyancy-driven door flow, no stairs or shafts (BuildSim floors are separate walkable graphs).
- Oxygen depletion, flashover and toxic species other than CO are not modelled.
- All parameters are assumptions and are not validated against measured fires. Evidence is relative: detector versus baseline on the same simulator (D9).
- The fire's growth class and location are scenario inputs, not emergent.

## 9. Open points

- ~~Whether BuildSim has smoke and CO sensor types~~ **Resolved 2026-10-07:** it has `smoke_detector`, `co_sensor`, `temperature_sensor` and `sprinkler` icon types (`buildingsim/docs/icons.md`).
- Sensor readings of smoke and CO are clamped at zero after noise (found in the end-to-end run: unclamped noise gave negative concentrations). Temperature is not clamped.
- Room volume: BuildSim gives area; assume a ceiling height (e.g. 3 m).
- Whether the course provides a gateway or physics engine that supplies sensor degradation (L2 slides); if so, §6 reduces to the fire source terms only.
