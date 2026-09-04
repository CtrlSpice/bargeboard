# bargeboard

> *Ceci n'est pas un déflecteur latéral.*

A custom OpenTelemetry Collector distribution for Formula 1 telemetry. Designed as a demo companion to [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer).

## Active implementation

The Go Collector distribution is the active implementation. Its baseline accepts OTLP traces, metrics, and logs, batches them, and writes them to the Collector's `debug` exporter. The compiled F1 Live Timing receiver authenticates, subscribes, reconnects, decodes, validates, and normalizes the live feed; state reduction and OTLP projection are the next behavior slices. A Go OpenF1 receiver does not exist yet.

The canonical design is the evolving [Bargeboard architecture](docs/architecture.md). It records accepted decisions, source limitations, pending candidates, implementation seams, and the required checks for future human and agent contributors.

The TypeScript historical replay CLI remains in `src/` as a working reference until the Go implementation reaches feature parity. Its signal model below documents the prototype only and is not authoritative for new Go work.

## Go quick start

Requires Go 1.26 or newer.

```bash
make check
make run
```

The shipped `config.yaml` enables F1 Live Timing and listens for OTLP/gRPC on
`localhost:4317` and OTLP/HTTP on `localhost:4318`. Run `make components` to
inspect the components compiled into the distribution.

### F1 Live Timing

The Collector reads each user's own F1 TV `subscriptionToken` from
`$HOME/.config/bargeboard/f1tv-token`. The repository configuration contains
only that file reference, never a token. Create the file with owner-only
permissions before running the Collector. After storing the token:

```bash
chmod 600 "$HOME/.config/bargeboard/f1tv-token"
make run
```

The token file should be readable only by its owner. Never put a token directly
in YAML, shell history, logs, issues, or commits. The current receiver connects,
subscribes, validates, and normalizes the feed; F1 state reduction and OTLP
emission remain under development.

## Historical TypeScript prototype

Fetches a historical F1 race from [OpenF1](https://openf1.org/) and emits it as OTLP traces, metrics, and logs to a configurable endpoint (defaults to `localhost:4317`). Spans are stamped with the actual historical race timestamps so backends with a time selector navigate to the real race window.

OpenF1 was chosen over FastF1 because OpenF1 has full 2026 telemetry the day after each race.

## Historical signal model

> This section describes the non-canonical TypeScript prototype. See
> [`docs/architecture.md`](docs/architecture.md) for accepted Go architecture.

### Traces

The whole race is **one trace**, and the trace starts at lights-out — the root span opens at the race start, not at session start, so t=0 in the timeline is the actual start of the race. Pre-race happenings (formation laps, grid procedures, stalled cars) attach to the root as span events whose timestamps precede the span's own start. Each driver's race span opens at lights-out as a child of the session root, with their laps / sectors / pit stops nesting under that.

```
2026_canada_race                  # service: race — opens at lights-out
│     span_event: race_control "EXTRA FORMATION LAP"   # pre-race events
│     span_event: flag.double_yellow (sector 15)
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

**Race lifecycle.** Driver races open at the lights-out moment detected from OpenF1's `SESSION STARTED` race-control message, not at session start, and close at either:
- The chequered-flag crossing (`RaceFinish`, derived from each driver's final lap completion) → `OK` status.
- The retirement moment (`Retirement`, from OpenF1's `/session_result` `dnf` flag) → `ERROR` status. Any still-open lap / sector / pit spans at the moment of retirement also close with `ERROR`, so the failure cascade is visible end-to-end from the race subtree down to the specific sector.

**Grid ordering.** Driver race spans are staggered by 1 ms × (grid position − 1), so a trace UI sorting rows by start time displays the field in starting-grid order (pole at top, P22 at the bottom). Invisible in the timeline view; useful in the row layout.

**Session-wide events** (yellow flags, SC, VSC, race-control announcements, penalties, investigations) fan out so they land on each driver's currently-open sector span, with structured attributes (`f1.flag.color`, `f1.penalty.type`, etc.) plus the typed event name (`flag.yellow`, `penalty.5_second`, `investigation.under_investigation`).

### Metrics

**Metrics are backdated to race time.** The OTel SDK stamps metric datapoints at collection time, which would squeeze a 2-hour race into the couple of minutes the replay takes — so bargeboard bypasses the SDK metric pipeline entirely. A hand-rolled `MetricBank` accumulates values during the replay and emits OTLP datapoints stamped with historical race timestamps (one datapoint per dirty series per 5s of race time), shipped straight through the OTLP exporter. Metrics, traces, and logs all land on the same real-world time axis.

**Histograms are DELTA, counters are CUMULATIVE.** Each histogram datapoint holds only the observations from its own 5-second window, so a heatmap column reads "lap times set *during this window*" and the chart shows the race's texture — stint pace drifting, everyone slowing under a safety car. (Cumulative histograms put every observation since lights-out into every datapoint, which makes every heatmap column identically wide and tells you nothing about *when*.) Counters stay cumulative, so `blue_flags` reads as a running total.

All metrics emit from the single session-level `race` resource — not the per-team driver resources — so every instrument is **one metric** with ≈ 22 series, split by the `f1.driver.code` and `f1.team` attributes auto-stamped on every datapoint. Cross-driver comparisons ("highest top speed in the field", "blue flags per driver") are single group-by queries; no merging across team services required. Traces and logs stay on the per-team resources.

| Instrument | Type | Notes |
|---|---|---|
| `f1.car.speed`, `rpm`, `throttle`, `brake`, `gear`, `drs` | gauge | telemetry from `/car_data` |
| `f1.car.position_{x,y,z}` | gauge | track-local position from `/location` |
| `f1.driver.standings_position` | gauge | running order from `/position` — the bump chart |
| `f1.driver.gap_to_leader`, `interval` | gauge | timing gaps from `/intervals` (~4s cadence); pit stops read as sawteeth |
| `f1.driver.trap_speed` | gauge | speed traps from `/laps`, split by `f1.trap` = `i1`/`i2`/`st` |
| `f1.session.air_temp`, `track_temp`, `humidity`, `rainfall`, `wind_speed` | gauge | from `/weather`, session-scoped (no driver attributes) |
| `f1.driver.laps_completed`, `pit_stops`, `blue_flags`, `penalties`, `investigations` | counter | monotonic, cumulative from lights-out |
| `f1.driver.championship_points` | up-down counter | credited at the chequered flag; survives DSQ revocations |
| `f1.session.cars_on_track` | up-down counter | +1 at lights-out per driver, −1 per retirement |
| `f1.driver.lap_time`, `sector_time`, `pit_duration`, `top_speed` | histogram | custom F1-shaped buckets (below) |
| `f1.driver.interval_distribution` | exponential histogram | "how close was the racing" — sub-tenth resolution in DRS range, coarse at minute-long gaps |

**Histogram buckets are universal across circuits** — every dry race lap on the calendar fits 63–105s, so one layout serves all 24 rounds: `lap_time` in 2.5s steps 60→110 (+ tails to 240s for SC/wet), `sector_time` in 1.5s steps 15→45, `pit_duration` in 1s steps 15→35, `top_speed` in 5 km/h steps 280→360 (+ low tails for SC laps).

Per-lap drill-down lives on lap spans (each lap *is* a span), not on metric attributes — keeps cardinality bounded.

### Logs

`RaceControl`, `Flag`, `Penalty`, `Investigation`, `Retirement`, `TyreChange`, `DefensiveMove` each emit a log record correlated to the currently-active span via trace context. Severity follows the kind (yellow = `WARN`, red / DSQ / retirement = `ERROR`, blue / no-action = `DEBUG`, etc.).

## Historical TypeScript resource model

- **Session resource:** `service.name=race`, `service.namespace=f1`, `service.instance.id=<year>-<round>-<type>` (e.g. `2026-canada-race`), `service.version`, plus `f1.session.{year,round,type}`. Owns the session root span and **all metrics**.
- **Driver resource:** `service.name=<team>`, `service.namespace=f1`, `service.instance.id=<code>`, `service.version`, plus `f1.driver.{code,full_name}`, `f1.car.number`. Teammates share `service.name` and differ on `service.instance.id`. Traces and logs only.

Querying examples:
- `f1.car.speed{f1.driver.code IN [ANT, RUS]}` → overlay teammates.
- `max(f1.driver.top_speed) by (f1.driver.code)` → fastest car in the field, one query.
- `sum(f1.driver.blue_flags) by (f1.team)` → team-level aggregation via the `f1.team` attribute.
- Same trace_id across all 22 drivers → "the race" is one queryable distributed trace.

## Historical TypeScript CLI

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
| `--no-cache` | off | Skip local Parquet cache; always fetch from OpenF1 |
| `-v / --verbose` | off | Debug logging |

`--season` is scaffolded but not implemented; raises an error if used.

## Historical TypeScript Parquet cache

On the first run bargeboard fetches telemetry from OpenF1 (≈14 s for a full field). Subsequent runs for the same session load from a local Parquet cache at `~/.cache/bargeboard/<session_key>/`:

| File | Contents |
|---|---|
| `telemetry.parquet` | All `Telemetry` events (~300k rows per race, column-oriented) |
| `events.json` | All other events: laps, sectors, pits, flags, retirements, etc. |
| `meta.json` | `raceStartT`, starting grid, cache version |

The historical CLI never expires this cache automatically, so it may become stale after OpenF1 backfills or corrects an event. Use `--no-cache` to force a fresh fetch. Driver-filtered runs (`--driver`) are not cached so a subsequent full-field run always fetches cleanly.

## TypeScript quick start

The historical CLI requires Node.js 22 or newer.

```bash
npm install
# Whole race, paced 60× (~2 minutes wall time):
npm run dev -- --session 2026-canada --speed 60x

# Two drivers, dry-run, no OTLP export:
npm run dev -- --session 2026-canada --dump --dry-run --driver ANT,RUS

# Smoke test the OTLP pipe with a tiny span + gauge + log:
npx tsx scripts/smoke.ts
```

## TypeScript architecture

Light-functional, no classes outside the OTel-SDK boundary. Each handler is pure `(state, event) → { state, effects }`; effects are a discriminated union; one interpreter applies them to the live OTel SDK.

- `models.ts` — discriminated union of events + session/driver types.
- `openf1.ts` — typed REST client with date-window pagination, jittered exponential backoff, `Retry-After` support.
- `extract.ts` — pure transforms from OpenF1 rows to bargeboard events. Detects lights-out for the formation/race phase split, pulls grid order from `/position`, surfaces DNFs from `/session_result`.
- `fanout.ts` — splits the event stream into a formation queue (pre-race, session-scoped) plus per-driver queues (race-phase). Session-wide events (`driver_code === "*"`) duplicate onto every driver's queue.
- `resource.ts` / `providers.ts` — OTel Resource + TracerProvider + LoggerProvider construction, plus the raw OTLP metric exporter (no MeterProvider — see `emit/metrics.ts`).
- `emit/metrics.ts` — `MetricBank`: hand-rolled metric aggregation (gauge / cumulative sum / explicit-bucket histogram) emitting OTLP datapoints with historical race timestamps.
- `emit/state.ts` — per-driver and session emitter state. Pure data; the interpreter never mutates it.
- `emit/handlers.ts` — pure event handlers per kind. `flushTelemetryGauges` batches gauge effects once per tick rather than 9 per telemetry sample (the naive shape is 4.5M gauge calls per race).
- `emit/effects.ts` — discriminated union of effects + `DriverInterpreter` / `SessionInterpreter`. Only place that touches the mutable OTel SDK.
- `replay.ts` — 100 ms tick loop with a formation → race phase machine. `await setImmediate()` between ticks so the SDK's `setTimeout`-driven batch exporters get to drain.

The Python prototype lives at the `last-python` git tag if you want to compare.

## Acknowledgements

- [OpenF1](https://openf1.org/) for free public access to historical F1 timing + telemetry data from 2023 onwards.
- [@anthropic-ai/claude-code](https://docs.claude.com/claude-code) for being the world's most patient pair-programmer through this rewrite.
