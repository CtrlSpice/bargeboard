# Bargeboard Architecture

> OpenTelemetry for floor rockets.

This is the canonical architecture document for bargeboard's active Go
Collector distribution. It is written for human contributors and coding
agents. The TypeScript replay CLI remains a useful historical reference, but
its signal model is not authoritative for new work.

The architecture is deliberately evolving. Accepted decisions are precise;
unresolved candidates stay visibly non-binding until they have completed a
review lap.

## Race Control

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY**
describe requirements in this document.

Decision status uses a small racing vocabulary with an explicit technical
meaning:

| Status | Meaning |
|---|---|
| **GREEN** | Accepted and normative. Implementation and tests MUST follow it. |
| **YELLOW** | Provisional or under investigation. It MUST NOT be treated as an implementation contract. |
| **RED** | Considered and rejected. Reintroducing it requires new evidence and review. |
| **FORMATION LAP** | Planned implementation work whose architecture is already GREEN. |

When code, tests, and this document disagree, work MUST stop at that boundary
until the conflict is resolved. A contributor MUST NOT silently choose one as
the truth.

Changes to source semantics, state reduction, OTLP projection, timestamps,
cardinality, or source ownership MUST update this document in the same commit
as the behavior and its tests.

## Goals

- Bargeboard MUST turn racing data into useful OpenTelemetry rather than merely
  wrapping source JSON in OTLP envelopes.
- Traces and metrics SHOULD carry the designed analytical experience.
- Logs SHOULD preserve discrete human-readable facts and diagnostics without
  becoming the default store for every high-frequency source update.
- Every derived fact MUST expose only the certainty supported by its source.
- Racing telemetry and operational receiver telemetry MUST remain distinct.
- The signal model SHOULD be compelling in
  [axolot(e)l](https://github.com/CtrlSpice/otel-desktop-viewer) while remaining
  valid OTLP.
- Deterministic transformation SHOULD remain in pure functions. Network I/O,
  clocks, Collector consumers, and lifecycle belong in the imperative shell.

## Non-Goals

- Bargeboard MUST NOT invent timing precision, incident causality, overtake
  certainty, pit-crew phases, or physical telemetry absent from a source.
- The complete field MUST NOT be represented as one race-long trace.
- High-frequency samples MUST NOT become one span or log each.
- Driver, lap, stint, event, timestamp, trace, and media identifiers MUST NOT
  create unbounded metric series.
- The historical TypeScript model MUST NOT be copied into Go without a fresh
  architecture decision.

## Current Grid

The Go distribution currently compiles:

- The `f1livetiming` traces, metrics, and logs receiver factory.
- The standard OTLP receiver.
- The batch processor.
- The debug exporter.

The default `config.yaml` intentionally runs only the standard OTLP receiver.
It is a credential-free validation and smoke-test configuration.

The F1 Live Timing receiver currently authenticates, negotiates SignalR,
subscribes, reconnects, decodes records, distinguishes feed updates from
subscription snapshots, validates timestamps and JSON, and inflates compressed
telemetry. Its reducer and OTLP projector are not implemented yet, so its
normalized-update consumer is currently a no-op.

No Go OpenF1 receiver exists yet.

## Source Ownership

**Status: GREEN**

Each source owns a defined domain. Bargeboard MUST NOT silently emit duplicate
versions of the same fact from both sources.

### F1 Live Timing

F1 Live Timing owns provisional, low-latency live data:

- Car telemetry and position samples.
- Live timing, sectors, positions, gaps, and intervals.
- Session, track, weather, race-control, and radio updates.
- Live driver-session traces and their stint, lap, and sector structure.

Its protocol is unsupported and season-dependent. Schema changes MUST be
captured with fixtures before semantic promotion.

### OpenF1

OpenF1 owns normalized and post-session domains:

- Historical backfill when no live projection for that session is active.
- Final results, starting grids, championship standings, and points.
- Normalized pit-lane and stationary-stop durations where documented.
- Post-session facts unavailable from the subscribed Live Timing topics.

Historical OpenF1 access is public. Live OpenF1 access requires sponsor
authentication and MUST remain optional.

### Overlap

Source authority MUST be selected before a pipeline starts. Running both
sources for the same semantic domain MAY be supported as an explicit audit
mode later, but MUST NOT be the default.

Every projected signal SHOULD identify its source using a bounded source value
such as `livetiming` or `openf1`. Source identity MUST NOT include endpoint,
token, cookie, or connection values.

## Time Model

**Status: GREEN**

All OTLP timestamps MUST use Unix nanoseconds and UTC.

Nanosecond representation does not imply nanosecond source precision. A
millisecond source timestamp remains millisecond-precision after conversion.
Bargeboard MUST preserve supplied precision and MUST NOT manufacture additional
accuracy.

Timestamp precedence is:

1. Inner measurement time from `CarData` or `Position`.
2. Payload event time from race control, radio, or session series.
3. SignalR feed-envelope time.
4. Collector observation time for untimestamped snapshots and periodic state
   observations.

Metric datapoints, exemplars, spans, span events, and logs MUST follow this
precedence.

Durations MUST be calculated as integer nanoseconds internally. Decimal source
seconds MUST be parsed without an intermediate binary floating-point
conversion. Metric duration values MAY be exported as `float64` seconds after
the integer duration is known.

Subscription snapshots initialize current state. They MUST NOT synthesize a
historical transition time or emit transition events merely because a state was
present in the snapshot.

Derived boundaries MUST carry a bounded quality description. Exact attribute
names remain **YELLOW**, but the model MUST distinguish at least observed,
publication-time, and estimated boundaries.

## Trace Model

**Status: GREEN**

### Trace Boundary

One trace represents one driver's entry in one session, including an entry that
does not start.

```text
driver.session
├── stint
│   ├── lap
│   │   ├── sector 1
│   │   ├── sector 2
│   │   └── sector 3
│   └── lap
└── stint
```

This creates roughly one trace per entered driver, preserves progression
through the session, and keeps the waterfall at a useful scale. A lap is a span,
not a trace.

Span names MAY include bounded racing context such as driver acronym, stint
number, lap number, or sector number when that materially improves the local
viewer. Canonical identity MUST remain in attributes rather than relying on the
display name.

### Status

- A finish without DNF, DNS, or DSQ MUST set the driver-session root to `OK`.
- DNF, DNS, and DSQ MUST set the driver-session root to `Error`, even when a DNF
  is classified, with a short result message.
- A stint MAY be `Error` when direct evidence says it ended through a puncture,
  collision, or mechanical retirement.
- An invalidated, deleted, aborted, or retirement lap MAY be `Error`.
- Slow pace, position loss, strategy, weather, and ordinary penalties MUST NOT
  become errors solely because the result was undesirable.
- Racing incidents MUST NOT use the semantic `exception` event name. That name
  remains reserved for software exceptions.

A DNS trace MUST be a zero-duration driver-session root at the observed session
start, or at the scheduled session start when no observed start exists. It has
no stint, lap, or sector children, uses an estimated time-quality marker when
necessary, and carries `Error` status with a `did not start` message.

### Events

Instantaneous or insufficiently bounded racing facts SHOULD be span events.
The pit event names are accepted; the remaining names are candidates pending
their domain review:

- `f1.pit.entry`
- `f1.pit.stop`
- `f1.pit.exit`
- `f1.position.changed`
- `f1.position.exchange`
- `f1.fastest_lap`
- `f1.personal_best`
- `f1.track_status.changed`
- `f1.penalty.noted`
- `f1.penalty.applied`
- `f1.team_radio`
- `f1.retirement`

Pit entry, stop, and exit MUST be represented by the accepted lap event names
above. Pit activity belongs on the lap containing the observed timestamp.
Events from one visit MUST share a bounded pit-visit identifier in event
attributes. A stationary stop MAY additionally become a child span when
reliable start and end times exist. A reported duration without reliable
placement MUST remain an event attribute and metric observation.

### Links

Links SHOULD represent relationships that are real but not hierarchical:

- Concurrent driver laps involved in a position exchange or incident.
- A race trace and the qualifying lap that established grid position.
- A racing span and a separate operational ingestion trace.

Links MUST be selective. Global flags MUST NOT create all-to-all links between
the field.

### Live Assembly

Completed child spans MAY be exported before their still-open parent stint or
session span. Parent and trace identifiers MUST remain stable for the session.
The exact deterministic identity algorithm is **YELLOW** and MUST be decided
before trace projection is implemented.

A short finalization delay MAY retain completed laps long enough to attach
normally late timing facts. Later race-control or radio records SHOULD use trace
and span correlation rather than forcing a rewrite of an exported span.

## Metric Race Control

### Delta Interval Contract

**Status: GREEN**

All accepted Delta histograms use deterministic event-time intervals:

- Ten-second windows are aligned to Unix-epoch boundaries in UTC.
- The first interval starts at the observed session start and ends at the first
  aligned boundary strictly after that start. An observation exactly at session
  start is included in this first interval by convention.
- Every later source observation at time `t` belongs to the interval
  `(start, end]` whose end is the smallest aligned boundary greater than or equal
  to `t`.
- `StartTimestamp` MUST be `start`; `Timestamp` MUST be `end`.
- The first interval MUST clip `StartTimestamp` to the observed session start
  when the session begins inside an aligned window.
- A session-ending partial interval MUST use the observed session end as its
  `Timestamp` rather than extending racing data beyond the session.
- Empty intervals MUST NOT emit datapoints.
- Lap-derived observations use the completed-lap timestamp for window
  assignment.

Inner samples in one source batch MUST be stably ordered by source timestamp
before windowing. Equal timestamps preserve original wire order, and later wire
order wins when one resampling tick has multiple candidates. The greatest
accepted source timestamp is the stream watermark. A window is final when the
watermark passes its end. An observation for an
already-final window MUST NOT revise or overlap that Delta datapoint; it is a
late observation and MUST be excluded with a bounded operational diagnostic.

Window state resets at the start of each session. Reconnect snapshots MUST NOT
reset a session's Delta start time or replay prior observations.

### General Rules

**Status: GREEN**

- A metric MUST answer a specific racing or operational question.
- Gauge MUST represent a meaningful sampled state or computed last value.
- Sum MUST represent something meaningfully additive.
- ExplicitHistogram and ExponentialHistogram MUST be chosen per candidate, not
  by blanket policy.
- Summary MUST NOT be emitted because the target viewer does not ingest its
  datapoints.
- Histogram observations MUST describe the population named by the metric.
- Delta histograms SHOULD use non-overlapping windows so the viewer can merge
  them into exact selected-range distributions.
- Exemplars SHOULD connect useful outliers to driver-session spans without
  adding trace or lap identity to the metric series.

The accepted high-rate telemetry pattern is one field-level series per session,
not one histogram per team or driver. Ten-second Delta windows retain temporal
texture while the viewer can merge the full race into one distribution.

One valid observation per active driver per second gives each active-car second
equal weight. Sampling ticks are whole Unix-second boundaries in UTC. For each
driver and tick, the reducer selects the latest valid source sample at or before
the tick, provided it is no more than one second old. It MUST NOT carry a value
across a longer gap. A driver is active for this sampling purpose when such a
sample exists; lifecycle labels do not manufacture observations.

Full source cadence MAY be used internally for time-weighted lap summaries.
Piecewise-constant intervals longer than one second MUST be excluded. A lap
summary requiring telemetry coverage MUST be suppressed below 90% observed
coverage unless its candidate contract explicitly chooses a stricter threshold.

Accepted ExponentialHistograms use scale `4` and zero threshold `0`. Exact zero
belongs to the zero bucket. Negative speed or engine-speed observations are
invalid. A future scale change is an architecture change because it affects
merge resolution and tests.

### Series Cardinality

Metric series MAY use bounded identity needed for selection:

- Session key.
- Driver number and acronym for explicitly per-driver metrics.
- Data source.

Metric series MUST NOT use:

- Lap or stint number.
- Pit-visit, message, media, trace, or span ID.
- Timestamp.
- Tyre age or another continuously changing state.

Lap, stint, and correlation identity belongs in spans and exemplar filtered
attributes.

Exact resource and instrumentation-scope attributes remain **YELLOW**. A driver
or team MUST NOT become `service.instance.id`, because that would fragment
metric resource identity. The emitting service identity MUST remain stable.

## Accepted Metric Candidates

### Field Speed

**Status: GREEN**

```text
Name:        f1.field.speed
Type:        Delta ExponentialHistogram
Unit:        km/h
Series:      one per session
Window:      10 seconds
Observation: one valid speed per active driver per second
```

The full-window histogram represents the speed distribution of all active-car
seconds in the session. A retired car contributes fewer observations because it
participated for less time.

Lap spans SHOULD contain minimum, time-weighted mean, maximum, p95, and
telemetry coverage. Raw per-driver speed Gauges are **RED** by default.

### Field Engine Speed

**Status: GREEN**

```text
Name:        f1.field.engine_speed
Type:        Delta ExponentialHistogram
Unit:        {revolution}/min
Series:      one per session
Window:      10 seconds
Observation: one valid RPM reading per active driver per second
```

Lap spans SHOULD contain time-weighted mean, maximum, p95, and coverage. Raw RPM
Gauges and Sums are **RED**.

### Field Gear

**Status: GREEN**

```text
Name:        f1.field.gear
Type:        Delta ExplicitHistogram
Unit:        {gear}
Series:      one per session
Window:      10 seconds
Population:  neutral and gears 1 through 8
```

Explicit upper bounds are `0.5`, `1.5`, `2.5`, `3.5`, `4.5`, `5.5`, `6.5`,
`7.5`, and `8.5`. The first bucket represents neutral (`0`), and the next eight
buckets represent gears 1 through 8. Values outside 0 through 8 MUST be rejected
before aggregation. Lap spans SHOULD contain the
maximum and time-weighted dominant gear. Gear-change counts are **RED** because
the source cadence can miss rapid shifts.

### Field Throttle

**Status: GREEN**

```text
Name:        f1.field.throttle
Type:        Delta ExplicitHistogram
Unit:        1
Series:      one per session
Window:      10 seconds
Range:       0.0 through 1.0
```

Accepted explicit upper bounds are 0%, 5%, 10%, 20%, 30%, 40%, 50%, 60%, 70%,
80%, 90%, 95%, 99%, and 100%. Source values MUST be normalized into the closed
interval. Invalid or sentinel values MUST be excluded rather than clamped.

Lap spans SHOULD contain time-weighted mean, full-throttle time ratio,
off-throttle time ratio, and coverage. Off throttle MUST NOT be labelled
coasting because the driver may be braking.

### Field Lap Brake Duty Cycle

**Status: GREEN**

Raw brake values are effectively binary. Raw brake Gauges, Sums, and histograms
of zero/one samples are **RED**.

The source adapter MUST explicitly distinguish inactive, active, invalid, and
stale values. The exact accepted raw-value mapping is **YELLOW** until compact
archive fixtures cover it; this blocks implementation but not the agreed
time-weighted metric semantics.

```text
Name:        f1.field.lap.brake_duty_cycle
Type:        Delta ExplicitHistogram
Unit:        1
Series:      one per session
Observation: one time-weighted duty cycle per valid completed driver lap
```

For timestamped brake states `b_i` and valid intervals `dt_i` within a lap:

```text
braking_time = sum(dt_i when b_i is active)
observed_time = sum(valid dt_i)
brake_duty_cycle = braking_time / observed_time
```

Intervals crossing lap boundaries or source gaps longer than one second MUST be
excluded. The observation MUST be suppressed below 90% coverage. Lap spans
SHOULD contain braking time, duty cycle, and coverage.

Accepted explicit upper bounds are 0%, 5%, 10%, 15%, 20%, 25%, 30%, 40%, 50%,
75%, and 100%.

### DRS

**Status: GREEN with season limitation**

Confident 2023-2025 Live Timing categories are:

| Raw code | Meaning |
|---:|---|
| `0` | Closed |
| `1` | Unavailable or disabled |
| `8` | Detected and eligible |
| `10`, `12`, `14` | Confirmed active |
| Other | Unknown |
| Missing | Not reported |

Unknown codes MUST remain unknown. Missing data MUST NOT become zero. Channel 45
was absent from the inspected 2026 Canadian Grand Prix race archive, and the
maintained FastF1 2026 compatibility path reports that DRS is no longer included.
DRS metrics MUST NOT be emitted whenever the channel is not reported, regardless
of season.

The current mapping was checked against these public sources:

- [2023 Singapore Grand Prix race CarData](https://livetiming.formula1.com/static/2023/2023-09-17_Singapore_Grand_Prix/2023-09-17_Race/CarData.z.jsonStream)
- [2024 British Grand Prix race CarData](https://livetiming.formula1.com/static/2024/2024-07-07_British_Grand_Prix/2024-07-07_Race/CarData.z.jsonStream)
- [2025 British Grand Prix race CarData](https://livetiming.formula1.com/static/2025/2025-07-06_British_Grand_Prix/2025-07-06_Race/CarData.z.jsonStream)
- [2025 Abu Dhabi Grand Prix race CarData](https://livetiming.formula1.com/static/2025/2025-12-07_Abu_Dhabi_Grand_Prix/2025-12-07_Race/CarData.z.jsonStream)
- [2026 Canadian Grand Prix race CarData](https://livetiming.formula1.com/static/2026/2026-05-24_Canadian_Grand_Prix/2026-05-24_Race/CarData.z.jsonStream)
- [OpenF1 car-data documentation](https://openf1.org/docs/#car-data)
- [FastF1 DRS investigation](https://github.com/theOehrly/Fast-F1/issues/44)

Before semantic DRS projection is implemented, compact sanitized records from
the cited archives MUST be added under receiver test data. Documentation
research is evidence for the decision; fixture tests are the implementation
gate.

```text
Name:        f1.field.drs.active_time_ratio
Type:        Gauge
Unit:        1
Series:      one per session
Window:      10 seconds
```

This Gauge uses the same aligned, session-clipped ten-second windows and
watermark/finalization policy as Delta metrics, but it has no aggregation
temporality. `StartTimestamp` is unset and `Timestamp` is the finalized window
end, including a clipped session-ending partial window.

For each window:

```text
eligible_time = valid active-driver CarData time, whether or not DRS is reported
known_time = eligible_time carrying a known DRS code
active_time = known_time carrying code 10, 12, or 14
known_state_coverage = known_time / eligible_time
active_time_ratio = active_time / known_time
```

Unknown and missing DRS time reduce coverage. Source gaps longer than one second
MUST be excluded. The Gauge MUST be suppressed when `eligible_time` is zero or
known-state coverage is below 90%. Late observations MUST follow the same
exclude-and-diagnose policy as finalized Delta windows.

```text
Name:        f1.field.lap.drs_active_time_ratio
Type:        Delta ExplicitHistogram
Unit:        1
Series:      one per session
Observation: one time-weighted confirmed-active ratio per valid lap
```

Accepted explicit upper bounds are 0%, 1%, 2%, 5%, 10%, 15%, 20%, 30%, 50%,
and 100%. Lap observations MUST be suppressed below 90% known-state coverage.

Global DRS enable/disable messages belong to race-control events, not these
telemetry metrics.

### Driver Classification Position

**Status: GREEN**

```text
Name:        f1.driver.position
Type:        Int64 Gauge
Unit:        {position}
Series:      one per driver per session
Temporality: none
Monotonic:   not applicable
Cadence:     on change plus a 10-second current-state observation
```

Position can improve or worsen. It MUST NOT be represented as a monotonic Sum,
and it MUST NOT be negated to manipulate chart direction. Missing position MUST
NOT become zero.

Gauge datapoints leave `StartTimestamp` unset. A source-driven change uses its
source publication timestamp. A periodic current-state observation uses the
Collector observation timestamp and does not claim that the position changed at
that time.

Position changes SHOULD become lap events with previous, current, delta, and
honest cause information. A state change is not necessarily an on-track
overtake. A field position histogram is **RED** because a healthy classification
is already approximately one car in every position.

## Pending Metric Candidates

**Status: YELLOW**

The following domains require the same candidate-by-candidate review before
implementation:

- Timing gap and interval, including lapped-driver values.
- Lap, sector, speed-trap, and personal-best timing.
- Tyre compound, age, and stint state.
- Pit entry, exit, lane duration, stop duration, and visit counts.
- Physical X/Y/Z position.
- Session clock, lap count, and track state.
- Weather.
- Race-control, penalty, radio, result, grid, and championship facts.
- Explainable pace, consistency, degradation, and pit-loss analysis.
- Receiver lag, payload size, reconnects, and validation failures.

Pending candidates MUST NOT be inferred from the historical TypeScript metrics.

## Logs

**Status: GREEN**

Default logs SHOULD be curated:

- Race-control messages.
- Team-radio metadata or transcripts when available.
- Session and track transitions that benefit from a textual record.
- Data-quality, decoding, and receiver failures.

Redacted normalized source payload logging MUST be opt-in. High-frequency car
and position samples MUST NOT be raw logs by default. Raw capture is never a
bypass around security rules.

Log source timestamps and observed timestamps MUST remain distinct. Logs SHOULD
carry trace and span IDs when a relevant driver lap is known.

Before capture, URL user-info, query strings, and fragments MUST be removed, and
credential-shaped fields such as authorization, access token, cookie, secret,
signature, and API key MUST be omitted. `AudioStreams`, `ContentStreams`, and
`TeamRadio` capture MUST contain metadata and safe media paths only. Audio
bytes, base64 media, waveforms, credentials, cookies, connection tokens, and
signed URL secrets MUST NOT be emitted.

## State Reduction

**Status: FORMATION LAP**

The receiver has one shared instance across traces, metrics, and logs. That
instance SHOULD own one session state machine on its read goroutine.

The functional core SHOULD expose deterministic transformations with no
network, Collector consumer, logger, context, or wall-clock dependency.

Reducer requirements:

- Subscription snapshots replace or initialize applicable topic state.
- Feed updates apply source-specific sparse patch semantics.
- Arrays, numeric-key maps, and `_deleted` instructions MUST be handled where
  the source uses them.
- Batched telemetry entries MUST remain observations; reducing to only a latest
  value would lose data.
- Unknown topics MUST remain capturable and MUST NOT crash known-topic state.
- Semantic validation errors MUST use bounded diagnostics without payload data.

The imperative shell MUST copy registered consumer pointers under the consumer
mutex, release the mutex, and only then call downstream consumers.

Downstream consumer failure policy remains **YELLOW** and requires an explicit
decision before fan-out is implemented.

## OTLP Projection

**Status: FORMATION LAP**

Projection from reduced observations to Collector `pdata` SHOULD be
deterministic and tested without I/O.

Projectors MUST:

- Set exact resource, scope, signal identity, timestamps, units, and
  temporality.
- Avoid emitting empty signal batches.
- Keep one stable emitting service identity.
- Attach sparse, useful exemplars rather than one exemplar per high-rate sample.
- Keep racing outcome errors separate from software processing errors.

Exact resource attributes, scope name/version, deterministic trace/span IDs,
and the final data-quality attribute vocabulary remain **YELLOW**.

## Track Map

Current implementation seams:

| Area | Path |
|---|---|
| Collector component registration | `components.go` |
| Default credential-free pipeline | `config.yaml` |
| Live Timing factory | `receiver/f1livetimingreceiver/factory.go` |
| Auth and endpoint configuration | `receiver/f1livetimingreceiver/config.go` |
| Connection and SignalR transport | `receiver/f1livetimingreceiver/connection.go` |
| Wire record decoding | `receiver/f1livetimingreceiver/protocol.go` |
| JSON and compressed normalization | `receiver/f1livetimingreceiver/normalize.go` |
| Shared receiver lifecycle and consumer seam | `receiver/f1livetimingreceiver/receiver.go` |
| Historical TypeScript reference | `src/` |

Suggested future Go files such as `state.go` and `projection.go` are not
normative until their behavior slice begins.

## Security

- Subscription tokens MUST be read from a local file reference.
- Configured endpoints MUST reject URL user-info, query parameters, and
  fragments to remove common credential-bearing URL vectors.
- Secrets MUST NOT appear in configuration examples, tests, logs, status events,
  spans, metrics, documentation, commits, or pull requests.
- Configured endpoint paths MAY contain opaque proxy routing values. HTTP
  request/body and WebSocket dial failures MUST be sanitized rather than
  wrapping errors that can echo a configured or internally constructed URL.
  Transport errors MUST NOT include endpoint paths, authorization headers,
  cookies, connection tokens, or internally constructed credential parameters.
  Sanitization MUST preserve canonical cancellation and deadline classification.
- Live smoke tests MAY use a user-provided token-file path. Token contents MUST
  never be requested or printed.
- Workspace-local `AGENTS.md`, personality files, and tool configuration MUST
  NOT be added to the repository.

## Verification

Every Go change MUST run:

```bash
make check
npm run typecheck
git diff --check
```

Changes to the F1 Live Timing receiver MUST also run:

```bash
go test -race -count=1 ./receiver/f1livetimingreceiver
```

Dependency changes SHOULD also run:

```bash
go mod tidy -diff
go mod verify
```

Reducer tests SHOULD call pure functions directly. Projection tests SHOULD
inspect exact `pdata` resources, scopes, metric types, timestamps, values,
temporality, spans, events, links, logs, and exemplars. Transport integration
tests SHOULD use local HTTP and WebSocket servers rather than live credentials.

Archived fixtures SHOULD identify their public session source and MUST NOT
contain credentials or downloaded media.

## Pit Wall Checklist

Before committing a behavior slice:

- [ ] The implementation follows every GREEN decision it touches.
- [ ] New design choices have completed review and are recorded here.
- [ ] Tests cover the documented behavior and failure policy.
- [ ] Metric cardinality was calculated, not guessed.
- [ ] Timestamp source and precision were verified.
- [ ] Derived facts state their evidence and limitations.
- [ ] Logs and errors were checked for payloads and credentials.
- [ ] This document changed in the same commit when architecture changed.
- [ ] Required Go, race, TypeScript, config, and diff checks passed.
- [ ] Applicable dependency checks passed.

## Decision Ledger

| Decision | Status | Consequence |
|---|---|---|
| Hybrid signal model led by traces and metrics | GREEN | Logs remain curated and raw capture is opt-in. |
| Per-domain source authority | GREEN | Live Timing owns live data; OpenF1 owns historical/final domains. |
| One driver-session trace | GREEN | Stints, laps, and sectors form the waterfall hierarchy. |
| Pit activity as lap events | GREEN | No separate pit trace by default. |
| DNF, DNS, and DSQ as span errors | GREEN | Failed participation is visible in trace error navigation. |
| Unix-nanosecond OTLP timestamps | GREEN | Source precision is preserved without invention. |
| One field telemetry distribution per session | GREEN | Teams and drivers do not split histogram series. |
| Raw high-frequency telemetry Gauges | RED | They are noisy and duplicate better distributions and span summaries. |
| Whole-field race trace | RED | Driver progression would become a crowded cross-field waterfall. |
| Canonical cross-source reconciler | RED | Source domains remain independently owned. |
| Resource and deterministic ID scheme | YELLOW | Must be resolved before projection implementation. |
| Remaining metric candidates | YELLOW | Must complete candidate review before implementation. |
