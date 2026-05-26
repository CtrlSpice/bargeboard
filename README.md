# bargeboard

> *Ceci n'est pas un déflecteur latéral.*

A CLI that replays Formula 1 race sessions as OpenTelemetry signals. Designed as a demo companion to [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer).

## What it does

Fetches a historical F1 race from [OpenF1](https://openf1.org/) and emits it as OTLP traces, metrics, and logs to a configurable endpoint (defaults to `localhost:4317`). Spans are stamped with the actual historical race timestamps so backends with a time selector navigate to the real race window.

OpenF1 was chosen over FastF1 because OpenF1 has full 2026 telemetry the day after each race.

## Signals

### Traces

The whole race is **one trace**. The session is the root span (its own resource called `race`); a per-session `formation` span covers the pre-race period (formation laps, grid procedures); each driver's race span opens at lights-out as a child of the session root, with their laps / sectors / pit stops nesting under that.

```
2026_canada_race                  # service: race
├── formation                     # pre-race phase, child of session root
│   ├── span_event: race_control "EXTRA FORMATION LAP"
│   ├── span_event: flag.yellow (sector 15)
│   ├── span_event: flag.double_yellow (sector 15)   # stalled car
│   ├── span_event: race_control "EXTRA FORMATION LAP"
│   └── span_event: flag.clear (sector 15)
├── ANT_race                      # service: Mercedes, instance: ANT (P1 on the grid)
│   ├── ANT_L1                    # lap span
│   │   ├── ANT_L1_S1             # sector
│   │   ├── ANT_L1_S2
│   │   ├── ANT_L1_S3
│   │   └── ANT_L1_pit            # when applicable
│   └── ANT_L2 ...
├── VER_race                      # service: Red Bull, instance: VER (P2)
└── ... (one subtree per driver, in grid order)
```

**Race lifecycle.** Driver race spans open at the lights-out moment detected from OpenF1's `SESSION STARTED` race-control message, not at session start. They close at either:
- The chequered-flag crossing (`RaceFinish`, derived from each driver's final lap completion) → `OK` status.
- The retirement moment (`Retirement`, from OpenF1's `/session_result` `dnf` flag) → `ERROR` status. Any still-open lap / sector / pit children at the moment of retirement also close with `ERROR`, so the failure cascade is visible end-to-end from the race subtree down to the specific sector.

**Grid ordering.** Driver race spans are staggered by 1 ms × (grid position − 1) so a trace UI sorting rows by start time displays the field in starting-grid order (pole at top, P22 at the bottom). Invisible in the timeline view; useful in the row layout.

**Session-wide events** (yellow flags, SC, VSC, race-control announcements, penalties, investigations) fan out so they land on each driver's currently-open sector span, with structured attributes (`f1.flag.color`, `f1.penalty.type`, etc.) plus the typed event name (`flag.yellow`, `penalty.5_second`, `investigation.under_investigation`).

### Metrics

All per-driver instruments produce one time series per driver (≈ 22 per instrument total). `f1.driver.code` is auto-stamped on every datapoint as a metric attribute in addition to the resource hierarchy, so backends that don't filter well by resource can still split by driver.

| Instrument | Type | Notes |
|---|---|---|
| `f1.car.speed`, `rpm`, `throttle`, `brake`, `gear`, `drs` | gauge | per-tick telemetry; coalesced to one flush per tick |
| `f1.car.position_{x,y,z}` | gauge | track-local position from `/location` |
| `f1.driver.standings_position` | gauge | declared, currently unfed |
| `f1.driver.laps_completed`, `pit_stops`, `blue_flags`, `penalties`, `investigations`, `defensive_moves` | counter | monotonic |
| `f1.driver.championship_points` | up-down counter | survives DSQ point revocations; currently unfed |
| `f1.driver.lap_time`, `sector_time`, `pit_duration` | histogram | explicit buckets |
| `f1.driver.top_speed`, `gap_to_leader` | exponential histogram | wide range, no fixed bucket layout makes sense |
| `f1.session.cars_on_track` | up-down counter | session-scoped; +1 at lights-out per driver, −1 per retirement |

Per-lap drill-down lives on lap spans (each lap *is* a span), not on metric attributes — keeps cardinality bounded.

### Logs

`RaceControl`, `Flag`, `Penalty`, `Investigation`, `Retirement`, `TyreChange`, `DefensiveMove` each emit a log record correlated to the currently-active span via trace context. Severity follows the kind (yellow = `WARN`, red / DSQ / retirement = `ERROR`, blue / no-action = `DEBUG`, etc.).

## Resource model

- **Session resource:** `service.name=race`, `service.namespace=f1`, `service.instance.id=<year>-<round>-<type>` (e.g. `2026-canada-race`), `service.version`, plus `f1.session.{year,round,type}`.
- **Driver resource:** `service.name=<team>`, `service.namespace=f1`, `service.instance.id=<code>`, `service.version`, plus `f1.driver.{code,full_name}`, `f1.car.number`. Teammates share `service.name` and differ on `service.instance.id`.

Querying examples:
- `f1.car.speed{f1.driver.code IN [ANT, RUS]}` → overlay teammates.
- `service.name="Mercedes"` → aggregate both Mercedes drivers.
- Same trace_id across all 22 drivers → "the race" is one queryable distributed trace.

## CLI

```
bargeboard --session 2026-canada [options]
```

| Flag | Default | Description |
|---|---|---|
| `--session` | required | Session identifier (`YEAR-ROUND`, e.g. `2026-canada`) |
| `--endpoint` | `localhost:4317` | OTLP gRPC endpoint |
| `--speed` | `1x` | Playback speed (`0.5x`, `1x`, `60x`, …) |
| `--dump` | off | Emit everything as fast as possible (event loop still yields between ticks so the OTel batch processors keep up) |
| `--dry-run` | off | Skip OTLP export — build and process everything but make no network calls |
| `--from` | session start | Start point (`HH:MM:SS`) |
| `--to` | session end | End point (`HH:MM:SS`) |
| `--driver` | all | Comma-separated driver codes (`ANT,RUS`) |
| `-v / --verbose` | off | Debug logging |

`--season` is scaffolded but not implemented; raises an error if used.

## Quick start

```bash
npm install
# Whole race, paced 60× (~2 minutes wall time):
npm run dev -- --session 2026-canada --speed 60x

# Two drivers, dry-run, no OTLP export:
npm run dev -- --session 2026-canada --dump --dry-run --driver ANT,RUS

# Smoke test the OTLP pipe with a tiny span + gauge + log:
npx tsx scripts/smoke.ts
```

## Architecture

Light-functional, no classes outside the OTel-SDK boundary. Each handler is pure `(state, event) → { state, effects }`; effects are a discriminated union; one interpreter applies them to the live OTel SDK.

- `models.ts` — discriminated union of events + session/driver types.
- `openf1.ts` — typed REST client with date-window pagination, jittered exponential backoff, `Retry-After` support.
- `extract.ts` — pure transforms from OpenF1 rows to bargeboard events. Detects lights-out for the formation/race phase split, pulls grid order from `/position`, surfaces DNFs from `/session_result`.
- `fanout.ts` — splits the event stream into a formation queue (pre-race, session-scoped) plus per-driver queues (race-phase). Session-wide events (`driver_code === "*"`) duplicate onto every driver's queue.
- `resource.ts` / `providers.ts` — OTel Resource + TracerProvider + MeterProvider + LoggerProvider construction. View promotes `top_speed` / `gap_to_leader` to exponential-bucket histograms.
- `emit/state.ts` — per-driver and session emitter state. Pure data; the interpreter never mutates it.
- `emit/handlers.ts` — pure event handlers per kind. `flushTelemetryGauges` batches gauge effects once per tick rather than 9 per telemetry sample (the naive shape is 4.5M gauge calls per race).
- `emit/effects.ts` — discriminated union of effects + `DriverInterpreter` / `SessionInterpreter`. Only place that touches the mutable OTel SDK.
- `replay.ts` — 100 ms tick loop with a formation → race phase machine. `await setImmediate()` between ticks so the SDK's `setTimeout`-driven batch exporters get to drain.

The Python prototype lives at the `last-python` git tag if you want to compare.

## Acknowledgements

- [OpenF1](https://openf1.org/) for free public access to historical F1 timing + telemetry data from 2023 onwards.
- [@anthropic-ai/claude-code](https://docs.claude.com/claude-code) for being the world's most patient pair-programmer through this rewrite.
