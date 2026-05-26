# bargeboard

> *Ceci n'est pas un déflecteur latéral.*

A CLI that replays Formula 1 race sessions as OpenTelemetry signals. Designed as a demo companion to [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer).

## What it does

Fetches a historical F1 race from [OpenF1](https://openf1.org/) and emits it as OTLP traces, metrics, and logs to a configurable endpoint (defaults to `localhost:4317`).

OpenF1 was chosen over FastF1 because OpenF1 has full 2026 telemetry the day after each race.

## Signals

### Traces

The whole race is one trace. The session is the root span (a single resource called `race`). Each driver's race span is a child, and each driver's laps / sectors / pit stops nest under that.

```
2026_canada_race                  # service: race
└── NOR_race                      # service: McLaren, instance: NOR
    ├── NOR_L1                    # lap span
    │   ├── NOR_L1_S1             # sector
    │   ├── NOR_L1_S2
    │   ├── NOR_L1_S3
    │   └── NOR_L1_pit            # when applicable
    └── NOR_L2 ...
```

- DNF / retirement: root span closes with `ERROR` status + a `retirement` span event.
- Session-wide events (yellow flags, SC, VSC) are fanned out so they land on each driver's currently-open sector span.

### Metrics

Per-driver gauges (one push per telemetry sample): `f1.car.speed`, `f1.car.rpm`, `f1.car.throttle`, `f1.car.brake`, `f1.car.gear`, `f1.car.drs`, `f1.car.position_{x,y,z}`.

Per-driver counters: `f1.driver.laps_completed`, `f1.driver.pit_stops`, `f1.driver.blue_flags`, `f1.driver.penalties`, `f1.driver.investigations`, `f1.driver.defensive_moves`.

Per-driver histograms: `f1.driver.lap_time`, `f1.driver.sector_time`, `f1.driver.pit_duration` (explicit buckets); `f1.driver.top_speed`, `f1.driver.gap_to_leader` (exponential buckets).

Per-session up-down counter: `f1.session.cars_on_track`.

### Logs

Race-control messages, flags, penalties, investigations, retirements, tyre changes and defensive moves emit log records correlated to the active span. Severity follows the colour / penalty type (yellow = WARN, red = ERROR, blue = DEBUG, etc.).

## Resource model

- Session resource: `service.name=race`, `service.namespace=f1`, `service.instance.id=2026-canada-race`, plus `f1.session.{year,round,type}`.
- Driver resource: `service.name=<team>`, `service.namespace=f1`, `service.instance.id=<code>`, plus `f1.driver.{code,full_name}`, `f1.car.number`.

## CLI

```
bargeboard --session 2026-canada [options]
bargeboard --season 2026 --dump [options]   # (not implemented yet)
```

| Flag | Default | Description |
|---|---|---|
| `--session` | required* | Session identifier (`YEAR-ROUND`) |
| `--endpoint` | `localhost:4317` | OTLP gRPC endpoint |
| `--speed` | `1x` | Playback speed (`0.5x`, `1x`, `5x`, …) |
| `--dump` | off | Emit everything as fast as possible |
| `--dry-run` | off | Skip OTLP export — build everything but make no network calls |
| `--from` | session start | Start point (`HH:MM:SS`) |
| `--to` | session end | End point (`HH:MM:SS`) |
| `--driver` | all | Comma-separated driver codes (`VER,NOR`) |
| `-v / --verbose` | off | Debug logging |

\* Exactly one of `--session` or `--season`.

## Development

```bash
npm install
npm run dev -- --session 2026-canada --dump --dry-run --driver NOR,VER
npm run typecheck
npm run build
```

## Architecture

- `models.ts`: discriminated union of events + the session/driver types.
- `openf1.ts`: typed REST client with date-window pagination and 429/404 handling.
- `extract.ts`: pure transforms from OpenF1 rows to bargeboard events.
- `fanout.ts`: routes session-wide events into per-driver queues.
- `resource.ts` / `providers.ts`: OTel Resource + provider construction.
- `emit/handlers.ts`: pure `(state, event) -> {state, effects}` per event kind.
- `emit/effects.ts`: discriminated union of effects + the only place that touches the mutable OTel SDK.
- `replay.ts`: 100 ms tick loop that drains per-driver queues and applies effects.

The Python prototype lives at the `last-python` git tag if you want to compare.
