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

The shipped `config.yaml` runs both the F1 Live Timing and standard OTLP
receivers. It contains no credentials: the F1 receiver resolves each user's
token file through `${env:HOME}/.config/bargeboard/f1tv-token`.

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
- Provisional pit-lane and stationary-stop durations when the dedicated live
  topics are enabled.
- Session, track, weather, race-control, and radio updates.
- Live driver-session traces and their stint, lap, and sector structure.

Its protocol is unsupported and season-dependent. Schema changes MUST be
captured with fixtures before semantic promotion.

### OpenF1

OpenF1 owns normalized and post-session domains:

- Historical backfill when no live projection for that session is active.
- Final results, starting grids, championship standings, and points.
- Final or historical normalized pit-lane and stationary-stop durations where
  documented.
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

## Session Coverage

**Status: GREEN**

The canonical model covers each session in an F1 weekend:

| Source session | Canonical type | Canonical name |
|---|---|---|
| Practice 1 | `practice` | `practice_1` |
| Practice 2 | `practice` | `practice_2` |
| Practice 3 | `practice` | `practice_3` |
| Qualifying | `qualifying` | `qualifying` |
| Sprint Shootout or sprint-format qualifying | `sprint_qualifying` | `sprint_qualifying` |
| Sprint | `sprint` | `sprint` |
| Grand Prix | `race` | `race` |

Historical display names MUST be classified by their documented format rather
than string alone. In particular, the 2021 name `Sprint Qualifying` described a
race-like sprint, while later seasons use similar wording for a qualifying-like
session. Unknown formats MUST remain unknown until fixture-backed classification
exists.

Each session creates a separate set of driver-session traces. Practice,
qualifying, sprint qualifying, sprint, and race use one common trace contract;
they are not separate trace types. Q1/Q2/Q3 and SQ1/SQ2/SQ3 are phases within
one qualifying-like session and MUST NOT become separate traces.

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

A domain contract MAY reconstruct the measurement boundary of a completed
interval from accepted source durations and publication times. That boundary is
an estimated inner measurement time under item 1, not an arbitrary replacement
for a source timestamp, and MUST satisfy the domain's chronology checks.

Durations MUST be calculated as integer nanoseconds internally. Decimal source
seconds MUST be parsed without an intermediate binary floating-point
conversion. Metric duration values MAY be exported as `float64` seconds after
the integer duration is known.

Subscription snapshots initialize current state. They MUST NOT synthesize a
historical transition time or emit transition events merely because a state was
present in the snapshot.

An applicable subscription snapshot MUST replace topic state atomically. A
field absent from an authoritative snapshot is absent; it MUST NOT retain stale
pre-reconnect state. Valid hydrated current-state Gauges MAY emit once at the
Collector observation time and begin their freshness interval there. Snapshots
MUST NOT replay historical histogram observations, Sum deltas, span events,
logs, laps, or state transitions. Indexed event collections MUST seed their
seen identities so later replayed keys do not become new events.

A candidate MAY explicitly hydrate an absolute Cumulative Sum from coherent
snapshot state. That is a current cumulative value, not replay of historical
Sum deltas; Delta Sum candidates remain snapshot-suppressed.

Derived span, event, and log boundaries MUST use `f1.time.quality` with one of
these bounded values:

- `observed` for a direct source event or measurement boundary.
- `publication_time` when only publication placement is known.
- `estimated` for a bounded derivation from source facts.

The delivery mode `feed` or `snapshot` is not time quality and MUST NOT become a
metric dimension. Metric datapoint attributes all participate in series
identity, so metrics MUST NOT add `f1.time.quality`; each metric candidate
instead fixes its accepted timestamp and derivation contract.

## Trace Model

**Status: GREEN**

### Trace Boundary

One trace represents one driver's entry in one session, including an entry that
does not start.

```text
driver.session
├── qualifying.phase (qualifying-like sessions only)
│   └── stint
│       └── lap
│           ├── sector 1
│           ├── sector 2
│           └── sector 3
└── stint (all other sessions)
    └── lap
        ├── sector 1
        ├── sector 2
        └── sector 3
```

This creates roughly one trace per entered driver, preserves progression
through the session, and keeps the waterfall at a useful scale. A lap is a span,
not a trace.

Span names MAY include bounded racing context such as driver acronym, stint
number, lap number, or sector number when that materially improves the local
viewer. Canonical identity MUST remain in attributes rather than relying on the
display name.

`TimingData.SessionPart` is the primary qualifying-phase identity. Session
status cycles MAY provide fallback boundary evidence, but `Started` after
`Aborted` resumes the same phase and MUST NOT create another phase. Drivers
knocked out in an earlier phase MUST NOT receive synthetic later phase spans.
Practice, sprint, and race traces omit the phase layer.

Stint spans are lap-aligned, pit-separated strategic runs so every lap remains
inside its parent. The old stint owns its in-lap and the new stint owns its
out-lap. Exact pit entry, stop, and exit times remain lap events. A drive-through
starts a new driving stint even when the tyres remain fitted.

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

Race and sprint roots start at the observed competitive start and close at the
individual driver's confirmed finish crossing or final-authority retirement.
The provisional Live Timing `Retired` field MUST NOT close a root because the
source can reverse it. Practice and qualifying-like roots start at the first
canonical `Started` state and normally close at `Finalised`; elimination closes
the driver's last phase span, not the root.

`Finished` records a competitive-stop transition but MUST leave roots open for
trailing timing facts. `Finalised` begins final-result waiting, and `Ends` is a
terminal feed boundary. A completed live root remains unexported until the first
of an authoritative final result, `Ends`, five minutes after `Finalised`, or
receiver shutdown. The five-minute default SHOULD be configurable. Without
final authority, the root closes as provisional or incomplete rather than
inventing DNF, DNS, DSQ, or a classified finish. A later final result MUST use a
correlated log and result metrics; an exported root MUST NOT be resent.

### Events

Instantaneous or insufficiently bounded racing facts SHOULD be span events.
The accepted event names are:

- `f1.pit.entry`
- `f1.pit.stop`
- `f1.pit.exit`
- `f1.pit.visit.incomplete`
- `f1.position.changed`
- `f1.lap_time.deleted`
- `f1.lap_time.reinstated`
- `f1.personal_best`
- `f1.personal_best.revised`
- `f1.fastest_lap`
- `f1.session.status.changed`
- `f1.track_status.changed`

`f1.gap.lap_deficit.changed`, `f1.position.exchange`, team-radio, typed penalty
and investigation, and final retirement events remain **YELLOW** until their
domain contract is recorded below.

When a legal trace owner remains available, pit entry, stop, exit, and
incomplete visit use the accepted event names above. Pit activity belongs on the
lap containing the observed timestamp when that lap remains available. Events
from one visit MUST share Int64 `f1.pit.visit.number`. A stationary stop MAY
additionally become a child span only when an accepted future source supplies
reliable start and end times. A reported duration without those boundaries MUST
NOT create a span; its domain contract decides whether it is an event attribute
or metric observation.

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
The deterministic identity algorithm is defined under OTLP Projection and MUST
be shared by every projector.

A completed lap MUST remain mutable for five seconds of source time so normally
late lap, sector, and speed-trap facts can attach before its single export. Hard
lifecycle closure MAY finalize it sooner. Later race-control or radio records
MUST use trace and span correlation rather than forcing a rewrite of an exported
span.

## Metric Race Control

### Delta Interval Contract

**Status: GREEN**

All accepted Delta metrics use deterministic event-time intervals:

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
- Receiver shutdown first finalizes candidate-specific buffers, then flushes a
  non-empty partial interval at that logical metric stream's greatest accepted
  source watermark. Collector shutdown time MUST NOT extend the interval. A
  partial interval whose end is not after its start is suppressed.
- Empty intervals MUST NOT emit datapoints unless a candidate explicitly
  requires a coverage-proven zero.
- Lap-derived observations use the completed-lap timestamp for window
  assignment.

Inner samples in one source batch MUST be stably ordered by source timestamp
before windowing. Equal timestamps preserve original wire order, and later wire
order wins when one resampling tick has multiple candidates. The greatest
accepted source timestamp is the watermark for one logical metric stream,
identified by session, authoritative source, and instrument. It is shared
across that instrument's series so an inactive driver cannot prevent
finalization. Candidate-specific buffering, such as the five-second lap timing
delay, MUST occur before advancing this watermark. A window is final when the
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

Racing metric datapoints MUST have exactly these common identity attributes:

| Attribute | Type | Contract |
|---|---|---|
| `f1.session.key` | Int64 | Positive Live Timing `SessionInfo.Key` or matching OpenF1 `session_key`. |
| `f1.season.year` | Int64 | Four-digit session season. |
| `f1.session.type` | String | Canonical broad type from Session Coverage. |
| `f1.session.name` | String | Canonical specific name from Session Coverage. |
| `f1.data.source` | String | `livetiming` or `openf1`. |

An OpenF1 handoff MUST verify that its `session_key` equals the Live Timing key
before sharing trace identity. A missing or conflicting key blocks cross-source
correlation.

Instruments add only their declared conditional attributes:

| Instrument population | Additional attributes and types |
|---|---|
| Per-driver | `f1.driver.number` Int64, `f1.driver.acronym` String |
| Phase-scoped qualifying timing | `f1.session.phase` String |
| Sector timing | `f1.sector.number` Int64 |
| Speed-trap timing | `f1.speed_trap.location` String |
| Lap-time histogram | `f1.tyre.compound` String |
| Constructor standings | `f1.constructor.name` String |

Driver acronym and constructor name MUST be normalized and frozen before their
first metric datapoint for the session. A driver acronym is the validated
uppercase ASCII source acronym; a constructor name is the first non-empty
validated source display name. Session key, season, canonical type, canonical
name, and selected source authority also freeze at first racing signal. A later
conflict updates unexported trace or log state where safe and emits a bounded
diagnostic, but MUST NOT split an existing metric series.

Metric series MUST NOT use:

- Lap or stint number.
- Pit-visit, message, media, trace, or span ID.
- Timestamp.
- Tyre age or another continuously changing state.

Lap, stint, and correlation identity belongs in spans and exemplar filtered
attributes.

Meeting and circuit display metadata MUST NOT be copied into every metric series.
Uncertain identity MUST delay projection rather than emitting a label correction
that creates another series. State received before identity resolution MAY be
retained, but observations MUST NOT be queued without bound or replayed after
identity appears. A complete staged snapshot resolves identity before projecting
its current state; unresolved feed observations are excluded with a bounded
diagnostic. Collector-internal receiver metrics do not use racing identity and
are outside this table.

All F1-produced signals use one stable Bargeboard emitter resource:

```text
service.namespace = github.com/CtrlSpice
service.name      = bargeboard
service.version   = distribution build version, when available
```

The receiver MUST preserve a deployment-provided `service.instance.id` but MUST
NOT synthesize one from a session, driver, constructor, or another racing fact.
It MUST NOT overwrite resource attributes supplied by a later deployment
resource processor. Other receiver-created resource attributes are forbidden;
racing identity belongs on signal attributes. A key MUST NOT be duplicated at
resource and signal scope. Resource and scope schema URLs are empty until an
explicit semantic-convention migration defines them.

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

### Driver Timing Gap And Interval

**Status: GREEN for elapsed Gauges and lap boundary attributes; YELLOW for
lap-deficit projection**

Timing strings use a versioned, exact grammar. Existing spellings MUST NOT
change meaning when later evidence expands the grammar. Parsers MUST NOT trim,
case-fold, substring-match, manufacture seconds for lap deficits, or use numeric
sentinels.

```ebnf
value               = elapsed | live_lap_deficit | leader_lap |
                      terminal_lap_token | clear ;
elapsed             = "+", digit, { digit }, ".", digit, digit, digit ;
live_lap_deficit     = positive_integer, " L" ;
leader_lap           = "LAP ", nonnegative_integer ;
terminal_lap_token   = positive_integer, "L" ;
clear                = "" ;
nonnegative_integer  = "0" | positive_integer ;
positive_integer     = nonzero_digit, { digit } ;
digit                = "0" | nonzero_digit ;
nonzero_digit        = "1" | "2" | "3" | "4" | "5" |
                       "6" | "7" | "8" | "9" ;
```

The parser returns one of `ElapsedMilliseconds`, `LiveLapDeficit`, `LeaderLap`,
`TerminalLapToken`, `Cleared`, or `Unknown`. `+44.163`, `1 L`, `LAP 22`, `1L`,
and the empty string are distinct. `TerminalLapToken` remains quarantined from
canonical projection until its business semantics are proven. A missing field
is feed-patch absence, not a grammar value; snapshot absence follows the
authoritative replacement contract. `Catching` remains internal because its
business meaning is undocumented.

Values that exceed `time.Duration` after exact conversion or signed Int64 lap
range classify as `Unknown` with a bounded overflow diagnostic. Elapsed source
milliseconds MUST convert exactly to integer nanoseconds before entering
reducer state; binary floating point is used only at OTLP projection.

```text
Name:       f1.driver.gap_to_leader
Type:       Double Gauge
Unit:       s
Series:     one per driver per race or sprint session

Name:       f1.driver.interval_to_ahead
Type:       Double Gauge
Unit:       s
Series:     one per driver per race or sprint session

Name:       f1.driver.laps_behind_leader
Type:       Int64 Gauge
Unit:       {lap}
Status:     YELLOW

Name:       f1.driver.lap_interval_to_ahead
Type:       Int64 Gauge
Unit:       {lap}
Status:     YELLOW
```

Practice and qualifying-like `TimeDiffToFastest` fields are a different domain
and are not projected by this contract. Elapsed values are non-negative.
Leader state is confirmed only when one driver has `Position == 1`, its parsed
gap is `LeaderLap(N)` with positive `N`, and `LapCount.CurrentLap == N`.
Confirmed leader state emits zero gap and clears retained interval state even
when its sparse patch omits the interval, because the leader has no car ahead.
The zero uses the confirming feed publication timestamp, or Collector
observation time for a coherent snapshot, and follows the same 15-second
freshness and ten-second observation rules as elapsed values. Unknown, stale,
cleared, and unsupported values remain absent.

Archive evidence proves numeric seconds and `N L` are display domains rather
than stable completed-lap deficit. They can alternate while leader and driver
lap counts remain unchanged. Numeric seconds MUST NOT independently emit zero
laps behind, and one `N L` MUST NOT independently emit a lap-deficit Gauge or
event. Future promotion requires agreement between the parsed token,
`LapCount.CurrentLap`, a stable position-one driver, and comparable driver
`NumberOfLaps` boundaries. Change events require consecutive confirmed
boundaries. Lap interval additionally requires stable ahead-car identity. These
rules remain non-binding until fixtures and review make them GREEN.

Each elapsed field has independent freshness. A valid feed publication emits at
the source publication timestamp. Freshness expires 15 seconds after the
receiver observed that publication, using a monotonic Collector clock rather
than source timestamp arithmetic. While fresh, a ten-second current-state
observation MUST emit at Collector observation time. An explicit empty string
invalidates the field immediately; patch omission retains it only until expiry.
Snapshot hydration emits one valid current-state observation and starts
freshness at Collector observation time. Clear and expiry stop future
datapoints; they emit neither zero nor a synthetic tombstone. Gauge
`StartTimestamp` is always unset.

Every elapsed value is bound to a referent at publication. Gap requires one
uniquely identified current leader. Interval requires one uniquely identified
car immediately ahead in the current classification. A leader change
invalidates every retained gap until each driver receives a fresh value. A
classification change invalidates each interval whose ahead-car identity
changed. Cleared leader interval state MUST NOT reappear when that driver later
loses the lead; a fresh source interval is required. Invalidated referent state
emits no heartbeat.

Lap spans record fresh elapsed boundary state when available:

```text
f1.lap.gap_to_leader.start_seconds
f1.lap.gap_to_leader.end_seconds
f1.lap.gap_to_leader.change_seconds
f1.lap.interval_to_ahead.start_seconds
f1.lap.interval_to_ahead.end_seconds
f1.lap.interval_to_ahead.change_seconds
```

Boundary selection occurs once at lap finalization. For each finalized lap
start or end timestamp, select the latest valid source publication at or before
that timestamp from bounded timing history; equal timestamps use later wire
order. Publications after the boundary do not revise it. The selected value
must have been fresh at that boundary and have a known leader or ahead-car
referent. A change is present only when both boundaries use elapsed seconds and
the referent identity is unchanged. Sparse official timing MUST NOT become a
time-weighted mean. A seconds/laps transition is retained internally, not
coerced into one numeric delta.

The YELLOW `f1.gap.lap_deficit.changed` candidate would carry Int64
`f1.gap.previous_laps_behind`, `f1.gap.current_laps_behind`, and signed
`f1.gap.lap_delta`, plus `f1.time.quality=publication_time`. Stale previous
state, numeric/token domain switches, leader changes, first confirmation, and
snapshot hydration MUST NOT create the event.

`LAP N` supports leader timing semantics and consistency checks but MUST NOT
project a lap-count metric. The dedicated `LapCount` topic owns session lap
state.

Implementation is blocked until compact public fixtures cover snapshots and
feed patches; every grammar variant; clear, missing, and overflow cases; leader
confirmation agreement and mismatch; leader suppression; independent field
freshness; leader and ahead-car identity invalidation; interval
non-resurrection; same and changed lap-boundary referents; and numeric/lap
display-domain alternation. The fixture suite MUST include a stable lapped
driver that temporarily receives numeric seconds.

### Driver Lap And Sector Timing

**Status: GREEN**

`TimingData.NumberOfLaps` is the driver's current lap index and live lap
boundary marker, not a completed-lap counter. A validated forward increment
closes the prior lap and opens the next. The completed lap remains buffered for
five seconds of source time because `LastLapTime`, sector 3, and speed values
normally arrive late. A hard lifecycle boundary MAY finalize it sooner.

The per-session lap-finalization watermark is the greatest accepted timestamp,
after normal timestamp precedence, of any `TimingData` feed patch processed in
wire order. It never moves backward. A pending lap with observed boundary `T`
finalizes when that watermark is greater than or equal to `T + 5s`, or at a
hard phase/session boundary or receiver shutdown. Patches from other topics do
not advance this watermark. Reducer tests inject timestamps; the functional
core never reads a clock. A late patch can enrich only a still-pending lap and
cannot reopen an exported one.

When one driver patch advances the current index from `N` to `N+1`,
`LastLapTime` and prior-lap timing fields changed by that same atomic patch are
first assigned to lap `N`; the boundary is then applied and lap `N+1` opens.
During the buffer, a changed `LastLapTime` belongs to the most recently pending
completed lap. A late sector 3 value belongs there only when its slot is empty
and its accepted duration can end no later than the observed lap boundary while
preserving chronology. Otherwise sector changes belong to the open lap. A
second boundary finalizes any older pending lap before ownership moves forward.

Snapshot state MUST NOT create historical laps. A backward count, unexplained
jump, pre-pit pseudo out-lap, or phase reset that cannot be reconciled with
session state is quarantined instead of rewriting exported spans. The current
index projects as:

```text
Name:       f1.driver.current_lap
Type:       Int64 Gauge
Unit:       {lap}
Series:     one per driver per session, plus phase in qualifying-like sessions
Cadence:    on valid source change
```

The Gauge uses source publication time, hydrates once from a coherent snapshot,
and leaves `StartTimestamp` unset. Bargeboard MUST NOT subtract one and label
the result laps completed.

A cold snapshot count `N` establishes only the current-state baseline and emits
the Gauge; it creates no lap span. An unchanged first feed value creates no
transition. The first validated feed increment to `N+1` provides a real
boundary and opens lap `N+1`, but MUST NOT synthesize or close lap `N`. A direct
feed count received without a snapshot baseline MAY open that current lap at
its publication boundary. On reconnect, a coherent snapshot with the same
count preserves an already observed open lap. A different count closes only an
already observed child at its last known boundary as incomplete due to a
coverage gap, establishes the replacement baseline, and synthesizes no missed
laps. Subsequent processing follows the cold-baseline rule.

Prior-lap timing changed in that first `N+1` feed patch MAY produce the
last-value Gauges and exactly one histogram observation for lap `N`: the feed
has observed its completion even though its start was outside coverage. This is
a metric-only completed record with no synthetic lap/sector spans, trace
events, or deterministic lap identity. Timing present only in the preceding
snapshot remains hydration-only and MUST NOT enter histograms.

Each accepted completed duration projects into both current state and a pace
population:

```text
Name:       f1.driver.last_lap_time
Type:       Double Gauge
Unit:       s
Series:     one per driver per session, plus phase in qualifying-like sessions
Cadence:    one accepted completed-lap duration

Name:       f1.driver.lap_time
Type:       Delta ExplicitHistogram
Unit:       s
Series:     one per driver per session, qualifying phase when applicable, and
            normalized tyre compound
Window:     10 seconds
Population: accepted completed-lap durations
```

The finalized completed-lap boundary is the Gauge `Timestamp` and assigns the
histogram observation to its aligned Delta window; histogram datapoints retain
the window start and end required by the Delta interval contract. Gauge
`StartTimestamp` is unset. Snapshot hydration MAY emit only the latest-lap
Gauge at Collector observation time; it MUST NOT replay histogram history. The
Gauge does not heartbeat because the last completed duration remains valid
until replaced, explicitly cleared, or removed by an authoritative snapshot. A
lap without an accepted duration emits neither value but still has a span.

Lap-time histogram upper bounds are:

```text
45, 50, 55, 60,
61 through 120 in 1-second steps,
125, 130, 135, 140, 150, 165, 180, 210, 240, 300
```

Underflow and overflow remain valid observations. Zero and negative durations
are invalid. Exact sum, count, minimum, and maximum preserve source values. The
histogram describes accepted physical durations, whether source-reported or
strictly reconstructed, not only finally classified valid laps; a later
sporting deletion cannot retract a finalized Delta observation.

#### Lap Compound Dimension

**Status: GREEN**

Only the lap-time histogram adds `f1.tyre.compound`. It is the current coherent
`TimingAppData.Stints[n].Compound` from the source run paired to the owning
trace stint when the lap finalizes. This is a provisional live classification,
not a claim that the source assignment can no longer be corrected. The
dimension is mandatory and uses exactly one of:

```text
soft, medium, hard, intermediate, wet, test, unknown, other
```

Normalization trims ASCII surrounding whitespace and compares ASCII
case-insensitively. `SOFT`, `MEDIUM`, `HARD`, `INTERMEDIATE`, and `WET` map to
their lower-case values. Literal `UNKNOWN`, missing, and explicitly empty
compound map to `unknown`. `TEST` and values beginning with `TEST_` or `TEST-`
map to `test`; any other non-empty source value maps to `other`. Display
spelling, source stint index, tyre-set identity, and inferred
slick/intermediate/wet class MUST NOT become metric attributes.

Compound assignment is mutable only while the completed lap is pending. If no
source run is paired uniquely, its entry is incoherent, or its compound is
missing or a placeholder, the histogram uses `unknown` and does not wait beyond
normal lap finalization. A later correction MUST NOT revise an exported
observation. A cold metric-only completed record uses a uniquely paired
coherent source run from the replacement snapshot and otherwise uses `unknown`;
it MUST NOT infer compound from another driver or adjacent run. Compound has no
effect on the last-lap-time Gauge or lap span identity.

Completed sectors follow the same dual representation:

```text
Name:       f1.driver.last_sector_time
Type:       Double Gauge
Unit:       s
Series:     one per driver, session, phase when applicable, and sector 1..3

Name:       f1.driver.sector_time
Type:       Delta ExplicitHistogram
Unit:       s
Series:     one per driver, session, phase when applicable, and sector 1..3
Window:     10 seconds
Population: accepted completed-sector durations
```

Sector-time histogram upper bounds are:

```text
10, 12.5, 15,
15.5 through 50 in 0.5-second steps,
55, 60, 75, 90, 120
```

`f1.sector.number` is Int64 and exactly 1, 2, or 3. Snapshot hydration MAY emit
latest-sector Gauges but no historical histogram observations. A completed
sector's finalized boundary is the Gauge `Timestamp` and assigns its histogram
observation to an aligned Delta window. Sector Gauge `StartTimestamp` is unset
and the Gauges do not heartbeat.

### Timing Repair And Boundaries

**Status: GREEN**

Timing duration strings use this exact ASCII grammar:

```ebnf
duration     = [ minutes, ":" ], seconds, ".", milliseconds ;
minutes      = digit, { digit } ;
seconds      = digit, { digit } ;
milliseconds = digit, digit, digit ;
digit        = "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
```

When minutes are present, seconds MUST be in `0..59`; otherwise seconds are an
unbounded decimal component subject to integer-nanosecond overflow checks.
Whitespace, signs, exponents, commas, alternate fractional precision, and
trailing characters are invalid. The duration MUST be positive and fit in an
Int64 nanosecond count. A missing field is no update, an explicit empty `Value`
is no reported duration, and `PreviousValue` is reference state only. Parsed
values MUST use integer arithmetic without an intermediate binary float.

Every new lap starts with empty sector slots. Prior values are references, not
new observations. At lap finalization, exactly one missing sector MAY be
reconstructed only when the other two sectors and official `LastLapTime` are
present and their positive integer-nanosecond remainder exactly equals the
prior accepted value for that same sector. Blind carry-forward is forbidden.

`f1.timing.value_quality` is a bounded trace attribute with values `reported`,
`reconstructed`, or `conflict`; it MUST NOT be a metric attribute. A strictly
reconstructed duration is accepted for its Gauge, histogram, and span exactly
once and carries `reconstructed` on the owning span. A direct accepted value
carries `reported`. Thus the metric populations include both accepted forms
without splitting their series.

Lap-duration precedence is:

1. A valid explicit `LastLapTime.Value` is the preferred candidate.
2. Explicit empty `Value` plus three accepted sectors MAY reconstruct the lap
   by exact integer-nanosecond sum.
3. An omitted `LastLapTime` plus three sectors MAY reconstruct only when their
   sum exactly equals the prior accepted lap duration, proving a suppressed
   repeat.
4. If an explicit lap duration and three sectors disagree exactly, the lap-time
   conflict rule overrides item 1: neither lap-duration Gauge nor histogram is
   emitted. The lap span carries `f1.timing.value_quality=conflict` and MAY keep
   the reported duration as a Double `f1.lap.reported_duration` in seconds for
   diagnosis. Individually valid reported sector Gauges, histograms, and spans
   survive; the conflict does not prove those measurements false.

Wire timestamps are publication times, not transponder crossings. During the
five-second buffer, the reducer forms candidate lap ends from the validated
`NumberOfLaps` publication, sector 3 publication, sector 2 publication plus
sector 3 duration, and sector 1 publication plus sectors 2 and 3. It selects the
earliest candidate that preserves chronology and reconstructs contiguous
sector boundaries from accepted durations. Reconstructed boundaries use
`f1.time.quality=estimated`. Without sufficient durations, publication
boundaries remain `publication_time`. A boundary MUST NOT move before lap start,
overlap siblings, or revise an exported span.

Live lap deletion and reinstatement are later sporting decisions. They emit
correlated race-control logs and `f1.lap_time.deleted` or
`f1.lap_time.reinstated` driver events when parsing is confident, but MUST NOT
rewrite an exported lap or its histogram observation. Historical final
projection MAY mark a lap deleted after resolving reinstatements.

The indexed race-control record is the sole live event source and deduplication
identity; timing rollback MUST NOT emit a duplicate event. The event timestamp
is the record's payload event time, falling back through normal precedence. If
the referenced lap is still buffered, the event belongs to that lap. Otherwise
it belongs to the driver's active lap at the decision timestamp, then the open
driver-session root. If no owning span remains open, only the correlated log is
emitted. Reinstatement carries the deleted event's bounded source identity when
the source provides one; otherwise canonical session, driver, phase-or-`none`,
and lap number form the correlation. If the qualifying phase cannot be resolved
uniquely, only the race-control log is emitted.

Implementation requires compact public fixtures for pseudo out-laps, normal
count increments, backward and jumped counts, phase resets, five-second late
facts, exact repeated sectors and laps, explicit clears, timing conflicts,
estimated boundaries, deletion and reinstatement ownership, and reconnect
deduplication. Fixtures MUST distinguish cold and reconnect snapshots followed
by unchanged, incremented, jumped, and phase-reset counts; missing, empty, and
`PreviousValue` fields; every accepted lexical form; malformed, zero, negative,
and overflowing durations; inclusive watermark finalization; late ownership;
metric-only cold completion; shutdown partial-window flushing; reported versus
reconstructed metric populations; compound normalization and ownership; and
phase-safe deletion/reinstatement correlation.

### Best Timing State

**Status: GREEN**

Live Timing owns provisional personal-best state. It is correction-tolerant
current state, not a monotonic record:

```text
Name:       f1.driver.best_lap_time
Type:       Double Gauge
Unit:       s
Series:     one per driver per session, plus phase in qualifying-like sessions
Cadence:    on accepted source value change
```

For practice, sprint, and race, `TimingData.BestLapTime.Value` is the sole
source. For qualifying-like sessions, the singular value owns the resolved
current phase and is authoritative over that phase's corresponding
zero-indexed `TimingData.BestLapTimes` entry. Every valid sparse plural snapshot
or feed entry MUST update its resolved phase's retained state. A feed change
emits the Gauge, and a coherent snapshot MUST hydrate it at Collector
observation time, except that a conflicting current-phase entry cannot override
a present singular value. Indexes 0, 1, and 2 map exactly to
`q1`, `q2`, `q3` or `sq1`, `sq2`, `sq3`; other indexes are invalid. A
qualifying phase transition clears the singular active-phase baseline before
accepting values for the new phase. A plural entry that conflicts with a
present singular value for the current phase is retained only as a diagnostic;
the singular value wins.

Duration parsing reuses the timing duration grammar. `BestLapTime.Lap` and the
plural entry's `Lap` MAY correlate trace state only when the positive lap number
resolves uniquely in the same phase; lap number is not a metric attribute.
`TimingStats.PersonalBestLapTime` MUST NOT duplicate this Gauge because it loses
the qualifying phase reset semantics.

The Gauge accepts both faster values and explicit slower rollbacks after a lap
deletion. Feed datapoints use publication time. A coherent snapshot MUST hydrate
each retained phase at Collector observation time. `StartTimestamp` is unset,
there is no heartbeat, and a repeated identical value emits nothing. Missing
feed fields retain state. An empty `Value`, an applicable `_deleted`, or
authoritative snapshot absence clears state without a zero-valued datapoint.

`TimingStats` owns two session-wide ranked best tables:

```text
Name:       f1.driver.best_sector_time
Type:       Double Gauge
Unit:       s
Series:     one per driver per session and sector 1..3
Source:     TimingStats.BestSectors[0..2].Value

Name:       f1.driver.best_speed_trap
Type:       Int64 Gauge
Unit:       km/h
Series:     one per driver per session and speed-trap location
Source:     TimingStats.BestSpeeds.{I1,I2,FL,ST}.Value
```

These Gauges are cumulative across qualifying phases and deliberately omit
`f1.session.phase`; eliminated drivers remain represented. `BestSectors`
indexes map 0, 1, and 2 to `f1.sector.number` 1, 2, and 3. Speed keys map `I1`,
`I2`, `FL`, and `ST` to `f1.speed_trap.location` values `i1`, `i2`, `fl`, and
`st`. Ranking `Position`, lap number, and source index are supporting state and
MUST NOT become metric attributes.

A best-table Gauge emits only when its valid `Value` changes, including a
slower sector or lower speed correction. A position-only patch emits nothing.
Feed timestamps are publication time; coherent snapshots hydrate at Collector
observation time. `StartTimestamp` is unset and there is no heartbeat. Empty
values and authorized deletion clear state without a tombstone. Snapshot
absence removes stale state. Sector values use the timing duration grammar.
Speed values use one or more ASCII decimal digits, no sign or whitespace, and
must be positive and fit Int64; they are exported without binary float parsing.

### Speed-Trap Observations

**Status: GREEN**

`TimingData.Speeds.{I1,I2,FL,ST}.Value` owns live trap observations:

```text
Name:       f1.driver.speed_trap
Type:       Int64 Gauge
Unit:       km/h
Series:     one per driver, session, phase when applicable, and location
Cadence:    every explicit accepted source observation
```

Location and speed parsing use the exact best-speed rules above. Every explicit
valid `Value` is a measurement and emits once, including the same numeric value
at a later publication time. Missing fields retain patch state but do not emit;
empty values and authorized deletion clear without emitting zero. Feed
publication time is the datapoint timestamp, `StartTimestamp` is unset, and
there is no heartbeat. Subscription snapshots seed patch state but emit no trap
observation because they do not establish a trustworthy lap or phase placement.

Trap values participate in the completed-lap five-second buffer. In a patch
that advances `NumberOfLaps` from `N` to `N+1`, every explicit `I1`, `I2`, and
`FL` value attaches to pending lap `N` before the boundary, while every explicit
`ST` value belongs to open lap `N+1`. During the five seconds after a boundary,
explicit `I1`, `I2`, and `FL` values attach to the pending lap; `ST` attaches to
the open lap. Outside that interval all four attach to the open lap. For the lap
selected by these location-specific rules, a later explicit value replaces an
occupied slot while its owning lap is unexported, whether that lap is open or
pending, and still emits a new Gauge observation. An omitted field never
populates a lap slot merely because reduced topic state retains an older value.

A `TimingData` feed patch is a phase-transition patch when any applicable
`SessionPart` entry changes the canonical session phase. Trap observations from
all `Lines` entries in that atomic patch are suppressed because their phase
ownership is ambiguous: they emit no Gauge, populate no lap slot, and create no
span attribute. Raw patch state still reduces for later sparse updates, but an
omitted value in a later patch cannot resurrect the suppressed observation.
Conflicting `SessionPart` entries suppress all phase-scoped projection until a
later coherent patch.

When an owning lap span exists, accepted values also become bounded Int64 span
attributes in km/h named `f1.lap.speed_trap.i1_kph`,
`f1.lap.speed_trap.i2_kph`, `f1.lap.speed_trap.fl_kph`, and
`f1.lap.speed_trap.st_kph`. They do not alter metric series identity. A
metric-only cold completion can emit an associated Gauge but creates no span
attributes. Bargeboard MUST NOT infer one trap from sector number, use trap
values as high-rate vehicle speed, construct a trap histogram, or derive a
per-lap maximum.

### Best-Timing Events

**Status: GREEN**

Only `TimingData.LastLapTime` source flags create best-lap events. Sector and
speed flags remain display state and emit no events.

An accepted completed-lap duration with explicit `PersonalFastest=true` emits
`f1.personal_best`. Explicit `OverallFastest=true` emits `f1.fastest_lap`.
`f1.fastest_lap` means that Live Timing announced an overall-fastest lap at
that moment; it is not the final classified fastest lap. A later driver can
therefore emit another event. False clears flag state without an event, and an
omitted flag retains sparse patch state only within its owning lap.

Each event is deduplicated by canonical session, driver, phase-or-`none`, lap
number, and event name. A true flag split from its `Value` MAY emit only while
both facts resolve to the same pending lap; flags are never carried across a lap
boundary. Snapshot flags seed current state but emit no events. A timing
integrity conflict suppresses both events.

A phase-transition `TimingData` patch first clears every driver's lap-local best
flags and suppresses all `LastLapTime` best flags across its atomic `Lines`
entries. They are not attributed to either phase. Later explicit flags belong
to the newly resolved phase. This rule also applies when the transition patch
carries empty lap values.

`BestLapTime` has one candidate-specific sparse rule: when `Value` changes and
the same patch omits `Lap`, the previous lap number MUST NOT carry onto the new
value. The new value's lap correlation becomes absent. A supplied positive
`Lap` can update the current value's correlation without emitting a Gauge; an
empty `Value` clears both fields.

A feed update that replaces an already observed current `BestLapTime` with a
slower valid duration emits `f1.personal_best.revised` once for that source
transition. It carries Double `f1.personal_best.previous_duration` and
`f1.personal_best.current_duration` in seconds. It adds Int64
`f1.personal_best.previous_lap` and `f1.personal_best.current_lap` only when the
respective number is source-supplied and phase-resolved; the attributes are
independently optional. Faster normal progression, snapshot replacement,
clears, plural `BestLapTimes` maintenance, and repeated values do not emit the
revision event.

Revision events use a separate source-transition identity: canonical session,
driver, phase-or-`none`, previous duration and lap-or-`none`, current duration
and lap-or-`none`, and source publication Unix nanoseconds. The session reducer
retains this bounded seen set across reconnect. A snapshot establishes the
current baseline but seeds no revision identity and emits no event; its first
unchanged feed value therefore remains silent.

A personal-best or fastest-lap event belongs to its still-pending completed lap
at that lap's finalized boundary. If the observed lap was already exported, it
belongs to the still-open driver-session root at the same boundary. A
metric-only cold completion emits no best event because no lap trace identity
was observed. The revision event belongs to the active lap at its publication
timestamp, then the open root. If no legal owner remains open, the event is
suppressed with a bounded diagnostic rather than reopening a trace.

Implementation requires compact public snapshot and patch fixtures for the
singular and plural best-lap forms; qualifying phase reset and retained phase
entries; best rollback and deletion; all three best-sector indexes; all four
speed locations; value-only versus position-only updates; repeated equal trap
values; same-patch and five-second lap association including the `ST` exception;
split true/value flags; true-to-false holder turnover; snapshot suppression;
same-patch phase transition suppression; clear, delete, malformed, zero,
negative, and overflow cases; cold metric-only completion; revision-transition
identity; reconnect deduplication; and valid siblings beside invalid entries.

### Tyre And Source-Run State

**Status: GREEN**

Internally, each `TimingAppData.Lines[driver].Stints[n]` entry is a **source
run**: a mutable source description associated with one pit-separated driving
run. It is not a tyre-set identity and its publication timestamp is not a stint
boundary. A source key alone MUST NOT open or close a trace span, emit a pit or
tyre event, or prove that tyres changed.

Source keys are canonical, zero-based, non-negative decimal integers. Snapshot
`Stints` MAY be a complete array or numeric-key map and replaces the driver's
source-run state. Feed `Stints` is a sparse numeric-key map; a feed array remains
invalid until a fixture-backed patch contract exists. The current source run is
the greatest key in a coherent reduced sequence. An invalid or deleted current
entry makes current tyre state unavailable; the reducer MUST NOT fall back to
an older completed run.

Known source-run fields are:

| Field | Accepted state meaning |
|---|---|
| `Compound` | Mutable source compound normalized by the Lap Compound Dimension contract. |
| `TotalLaps` | Non-negative integral total use of the fitted tyres, including use in earlier sessions. |
| `StartLaps` | Non-negative integral tyre age when fitted; corroborating state only. |
| `New` | Exact string `"true"` or `"false"`; whether the source currently reports the fitted tyres as new. |
| `TyresNotChanged` | Exact string `"0"` or `"1"`; provisional source classification retained as evidence only. |
| `LapNumber` | Sparse source association metadata, not a canonical lap boundary. |

`New` and `TyresNotChanged` are strings in the observed protocol. Unknown
lexical values remain bounded invalid evidence and cannot drive projection.
Source-run entry failures quarantine only that entry when valid siblings can
still be reduced. Gaps, aliased numeric keys, or conflicting current entries
make current-run selection incoherent.

The active coherent source run's `TotalLaps` projects as:

```text
Name:       f1.driver.tyre.age
Type:       Int64 Gauge
Unit:       {lap}
Series:     one per driver per session
Cadence:    on valid value or active-source-run change
```

Zero is valid. The Gauge is explicitly non-monotonic: tyres may have prior
session use, a used set may be fitted, and source corrections may decrease or
reset the value. A feed datapoint uses its `TimingAppData` publication time. A
coherent snapshot hydrates once at Collector observation time. `StartTimestamp`
is unset, repeated unchanged patches emit nothing, and there is no heartbeat.
Changing to a new active source run emits even when its numeric `TotalLaps`
equals the prior value. Empty/deleted current state, invalid current state, or
authoritative snapshot absence clears the Gauge without a zero tombstone.

The metric has only common racing and driver identity. Compound, phase, lap,
trace stint, source-run key, `New`, `StartLaps`, and `TyresNotChanged` MUST NOT
be attributes. Bargeboard MUST NOT add a current-compound Gauge, source-run
count, stint count, stint length, or stint-age metric. Compound remains a
dimension only on `f1.driver.lap_time`.

### Source Runs And Trace Stints

**Status: GREEN**

`TimingData.InPit` transitions own observed pit visits; source-run keys do not.
The first valid `InPit` value establishes a baseline without an event. A fully
observed visit requires the feed sequence `false → true → false`. Snapshot state
cannot manufacture either edge, and `PitOut` is corroborating state only.

The trace remains lap-aligned. Pit entry leaves the in-lap in the current trace
stint. The first validated `NumberOfLaps` boundary after that entry opens the
next trace stint and that stint owns the out-lap. A drive-through creates a new
trace stint even when the source says `TyresNotChanged="1"` or no coherent
source run exists. Without a later lap boundary, no empty post-pit stint is
created. Ambiguous source-run correlation suppresses only tyre enrichment; it
MUST NOT damage pit, lap, or trace-stint assembly.

Pairing is by exact ordinal, never by nearest publication time. Source run `n`
can enrich only trace stint ordinal `n+1`. It pairs when that stint exists, the
run is coherent, and any source `LapNumber` does not contradict the stint's lap
range. A premature source run remains unpaired until the matching observed
stint exists; a missing run leaves that stint's compound `unknown`. A skipped,
extra, invalid, or deleted key MUST NOT shift later pairings. If an earlier
missing key arrives later, only still-unexported matching laps can gain its
enrichment.

The zero-based source key MAY initialize the one-based current trace-stint
ordinal from a cold coherent snapshot as defined by deterministic identity, but
it cannot create historical spans. A reconnect snapshot with the same source
run can preserve a previously feed-observed pairing. A higher key first
discovered in a snapshot seeds current state and the current ordinal while
suppressing missed transitions; it never pairs historical laps. A disconnect
spanning either pit edge cannot produce a complete visit. Only `InPit` edges
after the canonical driver-session root starts participate in live visit
correlation.

### Tyre-Change Event Candidate

**Status: YELLOW**

`f1.tyre.changed` is not accepted for implementation. Live assignments are
correctable and have no source settlement marker. Pit exit, first known
compound, first post-pit lap, elapsed time, and `TotalLaps` progression MUST NOT
be treated as finality. In public evidence, `Compound`, `New`, and
`TyresNotChanged` were corrected seconds or minutes after a visit.

A candidate source-announcement event requires all of:

1. A complete feed-observed pit visit.
2. One uniquely paired non-initial source run.
3. Final `TyresNotChanged="0"` on that run.
4. A non-placeholder compound assignment.
5. Settlement by a later coherent source-run key or synchronized session
   `Ends`.

The settlement rule is non-binding until compact public fixtures demonstrate
that no assignment field changes after the next coherent key. `Finalised`, a
fixed timeout, and a completed lap are not substitutes. `New` may corroborate
but cannot decide because a used set can be fitted. Same-compound replacements
remain possible.

If promoted later, the candidate would use the correlated pit-exit publication
time with `f1.time.quality=publication_time`, deduplicate by canonical session,
driver, and pit-visit ordinal, and emit at most once. Owner precedence would be
the still-buffered out-lap, its post-pit stint, then the open driver-session
root. A snapshot would seed state and seen identities but emit nothing. A
correction before settlement would replace pending state; a contradiction after
emission could only create a bounded diagnostic, never a retraction. Exact
physical tyre-fitting time is unsupported.

Implementation of the GREEN source-run and tyre-age contracts requires compact
public fixtures for complete list/map snapshots; sparse numeric-key feed
patches; fresh, used, same-compound, unknown, and unchanged runs; non-monotonic
`TotalLaps`; active-run deletion; malformed entries beside valid siblings;
pit-entry/exit ordering; drive-throughs; cold starts; reconnect overlap and
missed edges; snapshot hydration; and absence of every forbidden metric
attribute. Event-candidate fixtures must additionally include short and
multi-minute assignment corrections, source runs before and after pit exit, and
the proposed next-key settlement invariant.

### Pit Visits

**Status: GREEN**

Only feed-observed `TimingData.InPit` edges own broad pit-lane visits. The first
valid Boolean establishes a baseline. Thereafter `false → true` opens a visit
and emits `f1.pit.entry`; the matching `true → false` closes it and emits
`f1.pit.exit`. Repeated values emit nothing. A drive-through is a complete visit
even without stationary-stop detail or changed tyres.

`PitOut`, source-run changes, coordinates, and line or sector `Stopped` values
MAY corroborate diagnostics but MUST NOT open, close, or classify a pit visit.
In particular, `Stopped=true` can describe an on-track stop or retirement and
MUST NOT create a pit-stop event, duration, or span.

An event-capable visit has a canonical positive one-based ordinal equal to the
valid `NumberOfPitStops` explicitly present in the same atomic driver patch as
its `false → true` edge. Sparse retained count is insufficient. The value MUST
be greater than the prior accepted visit-count high-water mark; a larger jump is
valid and represents missed coverage. After an observed out-of-pit baseline, an
absent high-water mark compares as zero, so an explicit count of one can own the
first covered visit. If count is omitted or malformed, the
edge may still separate trace stints but opens an unnumbered visit that emits no
pit events and cannot later be renumbered. A valid equal or regressed count is a
replayed or stale edge: it establishes current `InPit` baseline state without
opening any visit. A later correction MUST NOT change an assigned ordinal or
exported correlation.

When an atomic driver patch contains a lap boundary and an `InPit` edge, apply a
`false → true` entry and its count before the lap boundary, but apply a
`true → false` exit after the lap boundary. Thus entry remains on the closing
in-lap and exit on the opened out-lap when both facts share one publication
timestamp. Other fields retain normal sparse patch semantics.

Every event in one visit carries Int64 `f1.pit.visit.number` and
`f1.time.quality=publication_time`. Entry and exit use their respective
`TimingData` feed publication timestamps. Event identity is canonical session,
driver, visit ordinal, and event name; repeated sparse state and reconnect
replay cannot emit duplicates. Owner precedence is the unexported lap
containing the timestamp, then the nearest still-open ancestor whose interval
contains that timestamp, then the open driver-session root. A post-entry stint
cannot own an event backdated before its start. If no legal span remains open,
the event is suppressed with a bounded diagnostic; state reduction still
succeeds.

The first valid `InPit` observation records a chronology watermark as well as a
Boolean baseline. Every later edge, numbered or unnumbered, MUST have a
publication timestamp strictly later than the greatest accepted `InPit`
observation timestamp. Repeated state at a later timestamp advances that
watermark without an event. An equal or earlier edge still updates
arrival-ordered source state, but opens or closes no visit, separates no trace
stint, discards any open visit with a bounded diagnostic, and marks continuity
unsynchronized. The next valid observation later than the watermark establishes
a new baseline without an edge. This prevents late wire patches from creating a
chronologically reversed visit.

A snapshot cannot create either edge. `NumberOfPitStops=C` seeds the projection
high-water mark, and `InPit=true` means count `C` already includes the
snapshot-visible unobserved visit. A snapshot-baseline `true` followed by a feed
`false` establishes the out-of-pit baseline but emits no unmatched exit.

Before closing or exporting any possible owner, an observed open numbered visit
becomes incomplete on connection loss, authoritative driver removal, actual
driver-root/session closure, or receiver shutdown. Simultaneous causes choose
`session_end`, then `driver_removed`, then `shutdown`, then `disconnect`. The
event emits exactly once at the original entry publication timestamp, with the
same visit number and String `f1.pit.visit.incomplete_reason` using exactly
those four values. It invents neither exit timestamp nor duration. `Finalised`
waiting alone is not closure. Shutdown is not also disconnect. Event emission
precedes child and root export so owner selection remains legal.

An invalid `InPit` field is quarantined under normal reducer rules and emits no
racing signal. It invalidates visit continuity; the next valid value establishes
a new baseline and any abandoned open visit is discarded with a bounded
operational diagnostic, not an incomplete racing event. A reconnect snapshot
cannot resume an incomplete or invalidated visit. Unnumbered visits likewise
close or discard without pit events.

### Pit-Lane Visit Count

**Status: GREEN**

`TimingData.NumberOfPitStops` owns the source's absolute broad-visit count:

```text
Name:           f1.driver.pit_lane_visits
Type:           Int64 Sum
Unit:           {visit}
Temporality:    Cumulative
Monotonic:      true
Series:         one per driver per session
Source value:   TimingData.NumberOfPitStops
```

The accepted JSON token is exactly `0` or a non-zero ASCII digit followed by
zero or more digits, with no sign, decimal point, exponent, or leading zero. It
must fit Int64. The value counts broad pit-lane visits, not tyre changes or
stationary service.

`StartTimestamp` is the feed-observed canonical session start and remains stable
across reconnect. Until that start exists, count state is retained but no Sum
datapoint is legal; neither a scheduled start nor snapshot observation time may
replace it. A retained pre-start value MAY emit at the observed start timestamp.

Feed datapoints use publication time. On cold projection, a coherent snapshot
MUST hydrate the absolute value, including zero, at Collector observation time
only when the observed session start is known. The accepted source high-water
mark, last exported value, last datapoint timestamp, and deferred value are
distinct projection state and survive reconnect topic replacement.

A reconnect snapshot lower than the source high-water mark is retained as
current source state but suppressed. An equal or higher value emits at Collector
observation time only when it is greater than the last exported value, restores
availability, or is the deferred unexported value, and only when that observation
time is later than the prior datapoint. Otherwise it is silent or remains
deferred. Snapshot hydration is current cumulative state, not replayed deltas.

A forward jump emits the new absolute value but MUST NOT synthesize intermediate
pit events. A decrease violates monotonicity and is suppressed; later values do
not emit until they exceed the prior accepted high-water mark. Topic deletion,
authoritative snapshot absence, and invalid current state make the source value
unavailable without clearing the metric high-water mark or emitting a
tombstone. Restoration at the same high-water value emits only when availability
changed and its timestamp is later than the prior datapoint.

Per series, one atomic normalized batch coalesces updates with the same source
timestamp and later wire order wins. A higher arrival-ordered count whose
publication timestamp is not later than the prior datapoint updates source and
high-water state but is deferred rather than emitted out of chronological order.
The next valid same-or-higher value at a later timestamp emits the current
absolute count, even when its numeric value repeats. Ordinary repeated values
emit nothing.

The metric has only common racing and driver identity. Phase, lap, trace stint,
source run, and visit number MUST NOT become attributes.

Implementation requires compact public fixtures for initial false and true
baselines; normal visits; drive-throughs; repeated edges; count zero, normal
increments, jumps, regressions, deletion, and recovery; missing count on an
edge; snapshot hydration; disconnect during each half of a visit; driver
removal; invalid topic state; lifecycle and shutdown closure; `Stopped=true`
outside the pit; deterministic ordinal assignment; event ownership and dedupe;
and exact OTLP Sum temporality, start timestamps, attributes, and independent
trace/metric failure.

### Dedicated Pit Topics

**Status: GREEN**

When pit-duration projection is implemented, the Live Timing subscription MUST
also request these exact topics:

```text
PitLaneTimeCollection
PitStop
PitStopSeries
```

An omitted topic in a successful subscription response means unavailable data,
not a transport failure. `PitLaneTimeCollection` is the live lane-duration
source. `PitStop` is the sole direct stationary-stop report source.
`PitStopSeries` is persistent history for snapshot seeding, deduplication, and
corroboration; it MUST NOT independently emit an event or histogram observation.
This authority split prevents the direct record and its mirrored or duplicated
series entry from counting twice.

The subscription decoder MUST preserve the requested-versus-present topic
manifest for each completed snapshot. Successful omission marks that dedicated
topic unavailable, clears its current and pending report state, preserves
session-scoped consumed signatures, and emits nothing. Unavailable is not
unsynchronized. A later snapshot containing the topic performs normal atomic
replacement. The current decoder must gain this manifest before dedicated-topic
projection is enabled; synthesizing an empty payload is forbidden.

`PitLaneTimeCollection.PitTimes` is a sparse map keyed by canonical driver
number. The map key owns driver identity; a present `RacingNumber` MUST agree.
An explicit valid `Duration` forms a lane report. Missing fields retain feed
state but create no observation. `_deleted` removes transient display state and
emits nothing. An authoritative snapshot replaces the map and seeds report
signatures without replaying durations.

`PitStop` is one recursively merged sparse current record, but a signal candidate
is deliberately stricter: `RacingNumber` and valid `PitStopTime` MUST both be
explicit in the same feed patch. `Lap` and `PitLaneTime` enrich that candidate
only when valid and explicit in that patch; omission does not inherit them from
the prior report. An invalid optional field is diagnosed and omitted without
invalidating an otherwise valid stop candidate. A coherent snapshot seeds its
current semantic signature without an event or histogram observation. Split
required fields remain state only and produce no candidate.

`PitStopSeries.PitTimes` is an outer map keyed by canonical driver number. Each
driver value is an indexed collection whose record has `Timestamp` and nested
`PitStop` fields. In a snapshot, either an array or numeric-key map is a complete
replacement for that driver. In a feed patch, an array also replaces that
driver's complete indexed state, while a numeric-key map patches only its named
indexes. Nested `PitStop` objects reduce recursively. The outer key owns driver
identity and any nested `RacingNumber` MUST agree. Applicable outer, indexed,
or nested `_deleted` instructions remove state and emit nothing.

Series indexes and record `Timestamp` values are retained only for reduction and
diagnosis because duplicates can use new indexes and timestamps. One invalid
indexed record quarantines only that record. A feed series record may
corroborate a direct report but cannot pre-empt it, emit it, or mark its direct
signature consumed.

Accepted lexical forms are:

| Source field | Grammar |
|---|---|
| `PitLaneTimeCollection.Duration` | Positive decimal seconds with exactly one fractional digit. |
| `PitStop.PitLaneTime` | Positive decimal seconds with exactly three fractional digits. |
| `PitStop.PitStopTime` | Positive decimal seconds with exactly one fractional digit. |
| `RacingNumber` | Positive canonical ASCII decimal integer string. |
| `Lap` | Positive canonical ASCII decimal integer string, or empty when unknown. |
| `PitStopSeries.Timestamp` | Strict RFC3339 normalized to UTC, retained as publication metadata only. |

Canonical integers have no sign, whitespace, exponent, decimal point, or
leading zero. Empty duration means no reported duration. Missing and empty are
distinct; unsupported `null` is invalid. Durations parse directly to positive
Int64 nanoseconds without an intermediate binary float. There is no semantic
upper cutoff because explicit histogram overflow represents valid outliers.

### Pit Duration Histograms

**Status: GREEN**

Dedicated reports project two session-level populations:

```text
Name:       f1.pit.lane_duration
Type:       Delta ExplicitHistogram
Unit:       s
Series:     one per session
Population: one accepted PitLaneTimeCollection duration per traversal report

Name:       f1.pit.stop_duration
Type:       Delta ExplicitHistogram
Unit:       s
Series:     one per session
Population: one accepted direct PitStop stationary duration per report
```

Lane-duration upper bounds are:

```text
10, 12, 14, 16, 18,
18.5 through 40 in 0.5-second steps,
45, 50, 60, 90, 120
```

Stationary-stop upper bounds are:

```text
0.5, 1.0,
1.25 through 4 in 0.25-second steps,
4.5, 5, 6, 7, 8, 10, 12, 15, 20, 30, 60, 120
```

Both histograms use only the common session attributes. Driver, lap, visit,
source index, and report identity MUST NOT split the series. A selectively
attached exemplar MAY carry driver and visit correlation when both are known.
Source report publication time assigns the observation to the aligned
ten-second Delta window; the datapoint retains the window timestamps. Snapshot
records never replay observations. Exact count, sum, minimum, and maximum are
required, and positive underflow or overflow remains valid.

The reducer keeps each driver's completed visits in source completion order,
including unnumbered visits under a coverage-local identity that is never
exported. Lane and stationary correlation slots are independent. A lane report
pairs to the earliest visit whose lane slot is empty and whose canonical
out-lap agrees when the report supplies one. A direct stop report similarly
pairs to the earliest empty stop slot with compatible out-lap and lane duration
when supplied. Nearest timestamp is never identity.

For this correlation, the canonical in-lap is accepted `NumberOfLaps` state
when `false → true` entry is applied. If it is `N`, the canonical out-lap is
`N+1` only when the first validated post-entry boundary opens `N+1` and that
value remains current at exit. If the in-lap is unknown, the first valid
post-entry value `M` establishes out-lap `M` only when it remains current at
exit; this synthesizes neither in-lap zero nor a prior boundary. Otherwise the
out-lap is unknown. A non-empty dedicated report `Lap=L` denotes this out-lap,
not the in-lap, and is compatible only with equal known `L`. Missing or empty
`Lap` adds no constraint and a report never creates or revises lap identity.

Cross-topic lane durations are compatible only when truncating the
three-decimal `PitStop` or `PitStopSeries` nanosecond value toward zero to a
100-millisecond boundary exactly equals the one-decimal
`PitLaneTimeCollection` value. There is no rounding tolerance. A missing value
does not conflict; an explicitly different value makes that visit ineligible.

Pairing first checks occupied same-kind slots for the same normalized source
identity: driver, report kind, and out-lap-or-`none`. A match is a duplicate or
correction and MUST NOT move to another visit. Only then is correlation attempted
once against empty slots. If no visit qualifies, the report remains
uncorrelated permanently. If several qualify, completion order selects the
first. A later-arriving visit cannot re-key or re-emit an uncorrelated fact.
Numbered and unnumbered visits can consume their independent metric-correlation
slots, but only a numbered visit can own a pit event. The conservative `none`
identity can collapse distinct missing-lap reports from one driver; undercount
is preferred to double counting an indistinguishable correction.

One visit contributes at most one lane duration and one direct stationary
duration. The first accepted report wins. A later differing report correlated
to the same visit is a correction diagnostic and MUST NOT replace an event or
Delta observation, even while its window remains open. `PitStop.PitLaneTime`
MAY enrich its stop event but MUST NOT enter the lane histogram, whose sole
owner is `PitLaneTimeCollection`.

Report identities survive reconnect and reset with the canonical session. A
correlated identity is session, driver, visit ordinal-or-coverage-local visit,
and report kind. An uncorrelated identity is session, driver, report kind, and
lap-or-`none`; its first accepted duration is payload, not identity. This
deliberately collapses indistinguishable missing-lap reports rather than risk
overcounting. Snapshots from `PitLaneTimeCollection` seed lane identities.
Coherent `PitStop` and `PitStopSeries` snapshot records seed direct-stop
identities and, when lane time is present, the compatible lane identity after
100-millisecond truncation. Series-only feed records never consume either
identity until a matching direct report arrives.

### Stationary-Stop Event

**Status: GREEN**

A valid direct `PitStop` feed report correlated to a numbered observed visit
emits `f1.pit.stop` once at the report's feed-envelope publication timestamp.
`PitStopSeries.Timestamp` is not a physical edge and MUST NOT override that
timestamp. The event has `f1.time.quality=publication_time`, Int64
`f1.pit.visit.number`, Double `f1.pit.stop_duration` in seconds, optional Double
`f1.pit.lane_duration` in seconds, and optional Int64 `f1.lap.number`.

Owner selection follows the pit-event rule: the unexported lap containing the
publication timestamp, then a containing open ancestor, then the open root. If
no legal owner or numbered visit exists, the event is suppressed but an
otherwise accepted independent histogram observation MUST still emit. A
reported stationary duration never proves
stationary start/end boundaries, MUST NOT be subtracted from a timestamp, and
MUST NOT create or resize a span. `Stopped`, `PitOut`, coordinates, telemetry
speed, and tyre state cannot fill a missing direct report.

Implementation requires compact public fixtures for normal and drive-through
visits; transient lane add/delete/key reuse; strict direct stop patches; nested
driver/index series arrays and maps; empty and missing lap; exact duration
grammars and 100-millisecond compatibility; malformed optional and required
fields; reports before and after correlation; numbered and unnumbered visit
queues; same-visit corrections before and after window close; exact and
semantic series duplicates; cold and reconnect identity seeding; unavailable
subscription topics; late finalized-window reports; event ownership; histogram
bounds, window timestamps, exemplars, and cardinality; and independent
trace/metric failure. Authenticated live availability of `PitStop` and
`PitStopSeries` MUST be fixture-verified before enabling their projector path.

`PitStopSeries` fallback emission, `PitStop.PitLaneTime` fallback into the lane
population, historical OpenF1 reconciliation, physical stationary placement,
and stationary-stop spans remain **YELLOW**. They MUST NOT be implemented from
the GREEN contracts above.

### Session Clock

**Status: GREEN**

`ExtrapolatedClock` owns the displayed session countdown:

```text
Name:       f1.session.clock.remaining
Type:       Int64 Gauge
Unit:       s
Series:     one per session
Cadence:    each accepted anchor and each newly reached extrapolated second
```

A complete source anchor has explicit `Utc` and `Remaining` in the same feed
patch plus coherent reduced `Extrapolating` state. `Utc` uses strict RFC3339 and
normalizes to UTC. `Remaining` uses this exact ASCII grammar:

```ebnf
remaining = digit, digit, ":", minute, ":", second ;
minute    = digit, digit ;
second    = digit, digit ;
digit     = "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
```

Minutes and seconds MUST each be in `0..59`; the two-digit hour component is
in `0..99`. Conversion to non-negative Int64 seconds uses integer arithmetic.
`Extrapolating` is a JSON Boolean. Missing feed fields retain reduced state,
but a sparse patch without explicit `Utc` and `Remaining` does not create a new
projection anchor. An authoritative snapshot requires all three fields.

For accepted anchor time `A`, remaining seconds `R`, and integer `n >= 0`:

```text
timestamp = A + n seconds
value     = R             when Extrapolating is false
value     = max(0, R - n) when Extrapolating is true
```

The anchor itself emits once at `A`. While extrapolating, the imperative shell
feeds timer observations into the single-owner reducer, which emits at most the
latest newly reached source second. Delayed scheduling, reconnect, or process
suspension MUST NOT backfill skipped seconds. Zero emits once and then stops
until another complete anchor. A paused anchor emits its source value once and
has no periodic heartbeat.

Extrapolation runs while connected, topic-synchronized, and
`Extrapolating=true`. Session status MUST NOT override that source bit:
`Inactive`, `Aborted`, `Finished`, and `Finalised` do not themselves pause or
resume the clock. Disconnect, receiver shutdown, and `Ends` suspend the schedule
without emitting zero. A coherent reconnect snapshot can resume immediately
from its current complete anchor; skipped disconnect seconds are represented
only by the one computed snapshot value.

A coherent snapshot computes exactly one current value at Collector observation
time: if extrapolating, subtract the whole non-negative seconds elapsed since
`Utc`, clamp at zero, and replay no intermediate points. If `Utc` is in the
future, retain state and delay projection until that instant rather than
inventing a clock-skew tolerance. The first post-snapshot timer timestamp MUST
be later than the snapshot datapoint.

Gauge `StartTimestamp` is unset. Full anchors sharing one timestamp coalesce in
wire order only within one normalized callback batch and flush at that batch's
end. If a later callback carries the same anchor timestamp, its first point
cannot revise the emitted datapoint; it updates schedule state and affects the
next derived second. An anchor whose derived datapoint timestamp is earlier than
the prior emitted point likewise may replace source/schedule state, but cannot
emit backward. Projection resumes only at a derived second later than the prior
timestamp. The `A+n` tick is an accepted reconstructed inner measurement
boundary under the Time Model, not Collector observation time. Snapshot
hydration remains the explicit exception at observation time. There is no
arbitrary freshness expiry because sparse anchors are normal source behavior.
Scheduled session time and `Heartbeat` MUST NOT fill a missing clock anchor.

Sparse `Utc`/`Remaining` re-anchoring, an isolated `Extrapolating` transition,
and an arbitrary source-versus-Collector skew tolerance remain **YELLOW**. They
MUST NOT create datapoints until fixture-backed.

### Session Lap State

**Status: GREEN**

`LapCount` owns race-like session lap state. Projection is allowed only for
canonical `race` and `sprint`; endpoint presence cannot enable it in practice or
qualifying-like sessions.

```text
Name:       f1.session.current_lap
Type:       Int64 Gauge
Unit:       {lap}
Series:     one per session
Cadence:    source change plus 10-second current-state heartbeat

Name:       f1.session.intended_total_laps
Type:       Int64 Gauge
Unit:       {lap}
Series:     one per session
Cadence:    source change only
```

`CurrentLap` and `TotalLaps` are JSON integer tokens using `0` or a non-zero
ASCII digit followed by digits, with no sign, leading zero, fraction, exponent,
string coercion, or Int64 overflow. Feed objects reduce sparsely; a snapshot
independently replaces or removes each field. Both Gauges are
correction-tolerant and MAY decrease. Neither is a counter.

A reduced `0/0` pair, whether reached in one or several patches, is the source's
unavailable marker and clears both projected values without zero datapoints.
With only one known field, a positive value can project independently;
`CurrentLap=0` alone and `TotalLaps=0` alone are unavailable. When both are
known, `CurrentLap=0, TotalLaps>0` is valid, while `CurrentLap>0,
TotalLaps=0` or `CurrentLap>TotalLaps` suppresses both until corrected. A
forward jump emits only new current state and MUST NOT create intermediate laps,
lap spans, or completed-lap observations.

Feed changes use the `LapCount` publication timestamp. A coherent cold or
reconnect snapshot hydrates each currently available field at Collector
observation time. Gauge `StartTimestamp` is unset. Equal-timestamp changes
coalesce only within one normalized callback batch and flush at batch end. A
feed, snapshot, or heartbeat timestamp not later than that series' prior emitted
timestamp updates current state but is deferred. The next later source
publication, snapshot observation, or current-lap heartbeat emits the latest
deferred value; intended total waits because it has no heartbeat. Collector UTC
moving backward never changes the monotonic timer schedule and cannot produce a
backward Gauge timestamp.

The current-lap heartbeat starts from either a feed-observed first `Started` or
a coherent `SessionStatus` snapshot of `Started` or `Aborted`. It continues
through later `Aborted` and `Inactive` interruptions and stops on
disconnect, topic unsynchronization, `Finished`, `Finalised`, `Ends`, or session
replacement. A cold `Inactive` snapshot does not prove prior start and cannot
enable the schedule. Reconnect preserves prior started state and resumes the
schedule from coherent current snapshot state without replay.

The heartbeat emits one current value every ten seconds of Collector monotonic
time, uses Collector wall time as the datapoint timestamp, and never backfills
missed ticks. A source change resets the next deadline. Intended total has no
heartbeat.

Both metrics have only common session identity. Driver, leader, phase, status,
heartbeat, and source delivery mode MUST NOT become attributes. Snapshot
absence or authorized deletion clears current state without a tombstone. This
topic MUST NOT be derived from driver `TimingData.NumberOfLaps` or
`SessionData.Series`.

Implementation requires compact public fixtures for complete and sparse clock
anchors; pause, resume, zero, future/backward/equal `Utc`, scheduler delay,
disconnect, snapshot extrapolation, and malformed or overflowing remaining
time; plus race and sprint lap state, the stale qualifying endpoint, normal
increments, `0/0` reset/restore, current and total regressions, incoherent
pairs, jumps, snapshots, reconnects, equal timestamps, exact ten-second
heartbeats, lifecycle suspension, and every forbidden synthetic lap.

### Session And Track Transitions

**Status: GREEN**

`SessionData.StatusSeries` owns session-status and track-status transitions.
Each indexed record has strict RFC3339 `Utc` and may carry `SessionStatus`,
`TrackStatus`, or both. Direct `SessionStatus` and `TrackStatus` topics are
current-state mirrors for snapshot reconciliation and timer activation; their
feed patches MUST NOT independently duplicate logs or span events.

`StatusSeries` keys are canonical non-negative decimal indexes. A snapshot array
or numeric-key map completely replaces indexed state and seeds each record's two
independent consumed bits. A feed array completely replaces state without
replaying existing records; a feed numeric-key map patches named indexes.
Records reduce recursively. New or newly completed feed records process in
ascending index order, with wire order deciding later patches to one index. If
one record completes both kinds, TrackStatus processes first and SessionStatus
second so a terminal lifecycle action cannot remove the track event's legal lap
owner.

A transition candidate requires an explicit accepted status field and coherent
record `Utc`; a later patch that supplies the missing half MUST complete it.
Session and track fields in one record are independent candidates. The index
and kind are transport-consumed at most once, while emitted transition identity
is semantic: canonical session, status kind, Unix-nanosecond `Utc`, prior
canonical state-or-`none`, and current canonical state. Snapshots reduce their
indexed history in order to seed both transport and directed-edge identities,
but never emit it.

Each status kind also keeps the greatest accepted series `Utc`. A candidate
older than that watermark is a stale replay: it seeds identity but changes no
canonical current state, continuity, lifecycle, log, or event. Equal timestamps
remain legal and process in index/wire order, which preserves same-time
`Finalised` then `Ends`. Each distinct directed state edge can emit at most once
at one exact UTC. A previously seen edge under a fresh index is semantic replay:
it updates canonical current state and continuity in source order but performs
no lifecycle action, log, or event. This permits a first `A → B → A` burst while
suppressing the same cycle when re-appended. A repeated current state advances
the watermark without becoming an edge. Combined with the older-UTC rule, this
suppresses old transition sequences re-appended under fresh indexes.

A later correction to an already consumed index/kind updates retained
diagnostic state only. The originally consumed UTC and state remain canonical
for current transition state, previous-state attributes, lifecycle, and emitted
telemetry; correction cannot retract or replace them. An unconsumed sibling kind
in the same record can still complete independently from current record state.

The complete subscription snapshot is staged at one Collector observation
time. Direct current mirrors replace atomically and SHOULD agree with the latest
corresponding valid series values. A mismatch suppresses snapshot-derived timer
activation and produces a bounded diagnostic; it does not replay or retract
series history. Feed-series transitions remain authoritative even when mirror
updates arrive before or after them. Direct-topic fallback when the indexed
series is unavailable remains **YELLOW**.

#### Session Status

Accepted `SessionStatus` values are exact and case-sensitive:

```text
Inactive, Started, Aborted, Finished, Finalised, Ends
```

The direct topic's supplemental string field named `Started` is retained only
for diagnosis and MUST NOT own lifecycle. For each accepted state change, the
projector emits one session-level log with body
`f1.session.status.changed`. It also emits an event of that name on every legal
unexported driver-session root whose interval contains the transition. The log
and event carry String `f1.session.status`, optional String
`f1.session.previous_status`, and `f1.time.quality=observed`. The log
`Timestamp` and event timestamp use record `Utc`; the log
`ObservedTimestamp` uses Collector observation time. Session logs have no
arbitrary driver trace correlation. Root-event dedupe extends the semantic
transition identity with driver and root identity.

A repeated canonical state in a new record updates current state but is not a
transition. The first feed record after an empty baseline MUST emit with no
previous-status attribute because its indexed UTC is direct transition
evidence. Unknown values break transition continuity and emit only a bounded
diagnostic; the next known value establishes a baseline without a racing
transition.

Lifecycle ordering is:

- First `Started` opens eligible driver roots at record `Utc`, then attaches the
  status event. A later `Started` resumes the same session and phase.
- `Inactive` is an intermission marker and creates neither root nor phase.
- `Aborted` records interruption but closes no driver root and does not infer an
  outcome or qualifying phase.
- `Finished` records competitive stop while retaining roots for trailing facts.
- `Finalised` attaches its events before practice/qualifying closure or
  race-like final-result waiting begins.
- `Ends` attaches its events before the terminal feed closure and export.

These refine, but do not replace, the existing trace-lifecycle rules.
`TimingData.SessionPart` remains the only qualifying-phase owner. There is no
session-status Gauge, numeric enum metric, or one-hot status metric.

#### Track Status

Accepted series values and their direct mirror pairs are:

| Code | Direct `Message` / series `TrackStatus` | Canonical state |
|---:|---|---|
| `1` | `AllClear` | `all_clear` |
| `2` | `Yellow` | `yellow` |
| `4` | `SCDeployed` | `safety_car` |
| `5` | `Red` | `red` |
| `6` | `VSCDeployed` | `virtual_safety_car` |
| `7` | `VSCEnding` | `virtual_safety_car_ending` |

Direct `Status` is the exact canonical decimal code string. An unknown series
value is not guessed: it advances the series UTC watermark, breaks authoritative
track-transition continuity, and produces a bounded diagnostic. The next known
series value establishes a baseline without log or event; a later change emits
normally. An unknown or mismatched direct code/message pair invalidates only
snapshot mirror coherence and cannot alter authoritative feed-series continuity.
Historical or future code `3` remains unclassified until fixture-backed.

Each accepted state change emits one session-level log with body
`f1.track_status.changed`. It carries Int64 `f1.track_status.code`, String
`f1.track_status.state`, optional Int64 `f1.track_status.previous_code`, optional
String `f1.track_status.previous_state`, and
`f1.time.quality=observed`. Timestamps follow the session-status log contract.
A repeated canonical state is not a transition. The first known feed value
after an empty baseline emits with no previous attributes; after continuity was
broken by an unknown value, the first known value is baseline-only.

The same transition emits `f1.track_status.changed` on each driver's active,
unexported lap only when that lap contains record `Utc`. The event carries the
same current, optional previous, and time-quality attributes as the log. Event
identity extends the semantic transition identity with driver and owning lap
identity. There is no root, phase, or stint fallback and no delayed queue; when
no active lap owns the timestamp, only the global log remains. A global state
MUST NOT create cross-car links. Race-control flag records may emit their own
curated logs but cannot duplicate this transition event. There is no
track-status Gauge.

Implementation requires compact public fixtures for snapshot array/map and feed
array/map forms; partial records; same-index completion/correction; same-time
ordered indexes; all six session states; qualifying and abort/resume cycles;
same-time `Finalised`/`Ends`; direct supplemental `Started`; all six accepted
track pairs; unknown, mismatched, and code-3 values; snapshot current/history
reconciliation; reconnect dedupe; first-known and repeated state; lifecycle
ordering; root and active-lap ownership; no-owner logs; and independent
trace/log delivery failure.

### Cars Running

**Status: GREEN**

For race-like sessions, line-level `TimingData.Lines[driver].Stopped` owns a
reversible live estimate of how many entered cars the timing feed currently
considers running:

```text
Name:       f1.session.cars_running
Type:       Int64 Gauge
Unit:       {car}
Series:     one per session
Cadence:    on coherent aggregate value or availability change
```

The value is the count of frozen canonical entrants whose coherent reduced
line-level `Stopped` value is explicit Boolean `false`. `Stopped=true` removes a
car and a later `false` adds it back. This is provisional current state, not a
terminal classification. Sector-level `Stopped`, `Retired`, `InPit`, `PitOut`,
coordinates, telemetry freshness, and final OpenF1 result state MUST NOT affect
the count. A car in the pit lane remains running when line-level `Stopped` is
false.

The entrant roster comes only from a non-empty authoritative `DriverList`
snapshot staged for the same session. Its container MUST be an object. Every
canonical positive driver-number key is an entrant and any present
`RacingNumber` MUST agree. A malformed positive-key entry makes the aggregate
roster incoherent; it is not silently dropped. Non-canonical keys are retained
as non-driver metadata and excluded, including safety and medical cars.

The roster freezes when an authoritative `SessionData.StatusSeries` `Started`
transition activates the metric. Direct `SessionStatus` feed state cannot
activate it. A coherent cold snapshot may instead freeze and activate only when
the latest indexed series state and direct mirror agree that the session is
currently `Started` or `Aborted`. If no complete roster exists at activation,
the lifecycle becomes active but projection waits; the first later complete
authoritative DriverList snapshot freezes it. Feed-only roster patches cannot
prove completeness. Later metadata changes cannot add or remove entrants.

Every frozen entrant MUST have coherent known `Stopped` state. The aggregate is
all-or-nothing; missing or invalid state for one entrant suppresses projection
rather than silently undercounting. Feed patches retain omitted driver fields.
An authoritative `TimingData` snapshot replaces `Lines`; an absent roster
driver makes the aggregate unavailable until coherent state returns. The
`Lines` key owns driver identity and a present line-level `RacingNumber` MUST
agree. An empty initial feed `Lines` object is not proof of an empty field.

After applying all driver entries in one atomic `TimingData` patch, emit at most
one aggregate datapoint when the resulting count or availability changed. Its
timestamp is the feed-envelope publication time. Feed activation samples
current state at the later of the authoritative `Started` record `Utc` and the
greatest effective timestamp of contributing `Stopped` state. That maximum is
the aggregate's accepted derived measurement boundary under the Time Model.
Cold snapshot activation and snapshot hydration use Collector observation time.
`StartTimestamp` is unset.

The activation timestamp is the lower bound for the series. Equal-time updates
within one callback flush once in wire order. A candidate before activation or
not later than the prior datapoint is not emitted; it replaces one latest
deferred aggregate rather than forming a queue. The next coherent `TimingData`
feed patch with a legal later timestamp or a later coherent snapshot observation
emits that deferred current value, even when its number is unchanged. Other
topic feeds cannot release it. There is no heartbeat or age-based expiry.

Projection continues through `Aborted` and later `Inactive` and ends at the
first authoritative `Finished`, `Finalised`, or `Ends`; later stop flags cannot
decrement finishers. A reconnect snapshot whose latest valid indexed series
state is terminal deactivates before any aggregate hydration, regardless of a
stale or missing direct mirror. This covers a missed terminal transition without
replaying it. A cold terminal snapshot cannot activate.

While active, projection pauses on disconnect or unsynchronized `TimingData`,
`DriverList`, or authoritative `SessionData` lifecycle state. Direct
`SessionStatus` mirror coherence is additionally required for cold activation or
non-terminal reconnect resumption, but not for authoritative terminal
deactivation; a delayed direct feed mirror cannot override an accepted indexed
transition. Reconnect preserves a frozen roster and lifecycle state and
atomically replaces current topic state. A value change or resumed availability
creates one snapshot candidate at Collector observation time, which emits only
when it passes the chronology guard above and otherwise remains deferred. An
unchanged continuously available value emits nothing.

The metric has exactly the common session identity and no driver, phase,
stopped, retirement, roster-size, or result attributes. It never closes traces,
emits retirement events, or assigns DNF, DNS, DSQ, classified finish, or points.
Final result authority remains independent and does not rewrite prior live
Gauge observations.

Implementation requires compact public fixtures for complete and partial
`DriverList` snapshots; non-driver entries; all-driver `TimingData` snapshots;
empty initial `Lines`; one and simultaneous `Stopped` changes; true-to-false
recovery; provisional `Retired`; pit entry/exit; pre-start, active, aborted,
inactive, finished, and cold terminal lifecycle states; DNS-like participants;
missing, invalid, deleted, and restored driver state; snapshot staging;
reconnect roster preservation; equal and backward timestamps; and exact Gauge
cardinality with no final-result side effects.

## Pending Metric Candidates

**Status: YELLOW**

The following domains require the same candidate-by-candidate review before
implementation:

- Tyre-change event settlement.
- Dedicated pit-topic fallbacks and physical stationary spans.
- Physical X/Y/Z position.
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

Feed patches MUST apply strictly in wire order. Source timestamps own OTLP
chronology, while arrival order owns current state. Equal source timestamps use
later wire order. Mixed topic patches MUST NOT be globally event-time sorted.
Only observations inside one batched high-rate payload are stably sorted by
their inner measurement timestamp before resampling.

Sparse object patching is presence-aware and recursive:

- A missing field retains its current value.
- A present scalar replaces its field; `false` and `0` are real updates.
- A present empty timing string clears that timing state.
- `_deleted` removes the named nested state where the source uses it.
- `_deleted` applies before ordinary fields in the same object, so an explicit
  ordinary field can recreate deleted state.
- JSON `null` clears only a field whose topic schema explicitly defines null as
  clear; otherwise it is a semantic validation failure and prior state remains.
- An empty object is a no-op feed patch and empty snapshot state at that object.
- Snapshot arrays replace applicable state. Feed arrays are normalized as
  indexed updates only where a fixture-backed topic contract permits it; an
  unsupported feed-array form is quarantined without mutation.
- Numeric-key maps are sparse indexed patches in feed updates.
- Snapshot absence removes state, while feed-patch absence retains state.

The functional core MUST return reduced state, signal effects, and bounded
diagnostics without calling a clock, logger, network, or Collector consumer.
The imperative shell supplies Collector observation time only where the time
model permits it.

The imperative shell MUST copy registered consumer pointers under the consumer
mutex, release the mutex, and only then call downstream consumers.

Traces, metrics, and logs MUST project and deliver independently. The shell MUST
attempt every configured downstream consumer even when one fails. A projector
or consumer failure in one signal MUST NOT suppress successful sibling signals,
trigger a Live Timing reconnect, or create an unbounded receiver retry queue.
Collector exporters own queue and retry policy. Delivery failures require
Collector-internal telemetry when available and rate-limited component logs;
they MUST NOT recursively emit racing telemetry through the failing pipelines.

Semantic validation uses the narrowest independently valid boundary. A wrong
field type quarantines that indexed entry when the enclosing container and
entry identity remain valid. A malformed topic container quarantines the whole
topic update. Valid sibling entries continue in wire order. Unknown enum or
lexical variants remain safely representable and do not stop the stream.

An invalid authoritative snapshot MUST retain prior topic state only as
non-projectable recovery state, mark that topic unsynchronized, and suppress all
signals derived from it until a later valid authoritative snapshot replaces it.
Sparse feed patches MAY still update the retained recovery state but MUST NOT
declare it synchronized or project it. Invalid SignalR framing, decompression,
size limits, UTF-8, JSON, or feed-envelope timestamp remain permanent
source-protocol failures.

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

All F1 signals use this source-neutral instrumentation scope:

```text
Name:    github.com/CtrlSpice/bargeboard/f1
Version: 1.0.0
```

The scope version represents the signal contract and changes only for
incompatible telemetry semantics. Executable releases MUST NOT create new scope
versions merely because `service.version` changed. Scope attributes are empty.

Every span repeats the minimum independently searchable identity:
`f1.session.key`, `f1.session.type`, `f1.session.name`, `f1.season.year`,
`f1.driver.number`, `f1.driver.acronym`, and `f1.data.source`. Driver-session
roots additionally carry meeting, circuit, driver name, and constructor
metadata when known. Child spans add their local phase, stint, lap, and sector
identity. Logs carry the minimum session and driver identity needed for
independent search and correlation. No signal relies on attribute inheritance,
which OTLP does not provide.

### Deterministic IDs

Trace and span identity uses SHA-256 over unambiguous length-prefixed parts:

```text
LP(value) = uint32 big-endian UTF-8 byte length || UTF-8 bytes
H(parts)  = SHA-256(LP(part_1) || LP(part_2) || ...)
```

Every part, including namespace and marker strings, uses `LP`. Integers use
unsigned base-10 without leading zeros. A positive session key and driver number
are required. The first 16 bytes form the TraceID and the first 8 bytes form the
SpanID.

```text
trace = H("bargeboard.otlp.id/v1", "trace",
          "session", S, "driver", D)[:16]

root = H("bargeboard.otlp.id/v1", "span",
         "session", S, "driver", D, "driver.session")[:8]

phase = H("bargeboard.otlp.id/v1", "span",
          "session", S, "driver", D,
          "qualifying.phase", P)[:8]

stint = H("bargeboard.otlp.id/v1", "span",
          "session", S, "driver", D,
          "phase", P_OR_NONE, "stint", N)[:8]

lap = H("bargeboard.otlp.id/v1", "span",
        "session", S, "driver", D,
        "phase", P_OR_NONE, "lap", L)[:8]

sector = H("bargeboard.otlp.id/v1", "span",
           "session", S, "driver", D,
           "phase", P_OR_NONE, "lap", L,
           "sector", N)[:8]
```

`P_OR_NONE` is the canonical phase value or the literal `none`. `N` for a stint
is its one-based canonical pit-separated ordinal. The exact phase mapping is:

| Session type | `SessionPart` | `P` |
|---|---:|---|
| `qualifying` | 1, 2, 3 | `q1`, `q2`, `q3` |
| `sprint_qualifying` | 1, 2, 3 | `sq1`, `sq2`, `sq3` |

`SessionPart` zero or an unknown part is unresolved and blocks phase-scoped
export. Non-qualifying spans encode `P_OR_NONE` as `none`. Sector `N` is exactly
1, 2, or 3.

The reducer freezes a stint ordinal when the stint is created and never
renumbers an exported hierarchy. A cold mid-session snapshot MUST initialize
the current ordinal from a coherent normalized zero-based source stint key plus
one, while creating no historical stint spans. Without a coherent source key,
the current stint remains unexportable until an observed boundary establishes
its ordinal. A late boundary that would insert or renumber a frozen stint is
quarantined and diagnosed rather than rewriting identity. Lap numbers are
positive and unique within `P_OR_NONE`; a source reset requires a resolved phase
before export.

Source, names, constructor, timestamps, and mutable result state MUST NOT enter
identity. An unresolved session, driver, phase, or lap identity blocks export of
the affected span. IDs MUST remain stable across reconnect, early child export,
and the handoff from Live Timing to final OpenF1 facts.

An all-zero output MUST be rehashed by appending the length-prefixed parts
`"retry"`, `"1"`, then increasing the canonical decimal counter until non-zero.
A detected collision between distinct canonical entities within receiver state
MUST suppress the later entity and its dependent subtree and emit a bounded
operational diagnostic; processing order MUST NOT be used as a collision salt.

## Track Map

Current implementation seams:

| Area | Path |
|---|---|
| Collector component registration | `components.go` |
| Collector pipeline and F1 token-file reference | `config.yaml` |
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
| Stable emitter resource and deterministic IDs | GREEN | Racing identity stays on signals and survives reconnects. |
| Remaining metric candidates | YELLOW | Must complete candidate review before implementation. |
