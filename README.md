# bargeboard

> *Ceci n'est pas un déflecteur latéral.*

A CLI tool that replays Formula 1 race sessions as OpenTelemetry signals. Designed as a demo companion to [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer).

## What it does

Fetches a historical F1 race session via [FastF1](https://github.com/theOehrly/Fast-F1) and emits the data as OTLP traces, metrics, and logs to a configurable endpoint (defaults to axolot(e)l on `localhost:4317`).

## Signals

### Traces

Each driver is a service. Lap spans are siblings under the race root — a race is a sequence, not a tree of nested laps.

```
race (root span, full session duration) [service: ANT]
├── lap 1
│   ├── sector 1
│   ├── sector 2
│   ├── sector 3
│   └── pit stop (if applicable)
├── lap 2
│   ├── sector 1
│   ├── sector 2
│   └── sector 3
└── ...
```

- Each driver gets their own root race span (their own service in axolot(e)l).
- DNF / retirement → root span closes with `error` status + span event with reason.
- VSC / safety car periods → span event fires on all currently open lap spans.
- DRS activation → span event on the lap span.

### Metrics (gauges, emitted per tick per driver)

| Metric | Unit |
|---|---|
| `f1.car.speed` | km/h |
| `f1.car.rpm` | rpm |
| `f1.car.throttle` | 0–100 |
| `f1.car.brake` | 0–100 |
| `f1.car.gear` | 0–8 |
| `f1.car.drs` | 0/1 |

### Logs

| Event | Body |
|---|---|
| Race control message | raw message text (flags, SC/VSC, penalties) |
| Pit entry / exit | `ANT entered pit lane` / `ANT exited pit lane` |
| Tyre fitment | `ANT fitted medium tyres (age: 0 laps)` |
| Retirement | `RUS retired: power unit failure` |

## Resource attributes

A **team** is a service; the **two cars** are instances of that service. The viewer groups by `service.name`, the instance attribute distinguishes the drivers.

```
service.name        = team name (e.g. Mercedes)
service.instance.id = driver code (e.g. ANT)
f1.driver.code      = ANT
f1.driver.full_name = Kimi Antonelli
f1.car.number       = 12
f1.team             = Mercedes
f1.session.year     = 2026
f1.session.round    = Canada
f1.session.type     = Race
```

## State machine (per driver)

| State | Behaviour |
|---|---|
| `RACING` | lap span open, metrics flowing |
| `PIT` | pit child span open, on pit lane |
| `VSC` / `SC` | racing, span event fired, lap times stretched |
| `DNF` | root span closed with error, no further signals |
| `FINISHED` | final lap span closed cleanly, root span closed |

## Tick pattern

- Wall clock ticks every 100ms (10Hz).
- Each tick advances the race clock by `100ms × speed`.
- All events whose timestamp falls within the new race clock window are processed and emitted.
- Pre-sorted event queue per driver; state machine consumes from the front.
- No `sleep`-based pacing — deterministic, pause/resume friendly, speed-change friendly.

Tick rate of 100ms is intentional: FastF1 source telemetry samples at ~4–5Hz, so 10Hz ticks are guaranteed to catch every source update without aliasing.

## CLI

```
bargeboard replay --session 2026-canada [options]
```

| Flag | Default | Description |
|---|---|---|
| `--session` | required | Session identifier (`YEAR-ROUND`) |
| `--endpoint` | `localhost:4317` | OTLP gRPC endpoint |
| `--speed` | `1x` | Playback speed (`0.5x`, `1x`, `5x`, …) |
| `--dump` | off | Emit everything instantly, no pacing |
| `--from` | session start | Start point (`lap 10` or `HH:MM:SS`) |
| `--to` | session end | End point (`lap 30` or `HH:MM:SS`) |
| `--driver` | all | Comma-separated driver codes (`VER,NOR`) |

## Configuration defaults

- OTLP endpoint: `localhost:4317` (gRPC, axolot(e)l)
- Tick interval: 100ms
- FastF1 cache: `~/.cache/bargeboard/`
- CLI framework: `typer`
