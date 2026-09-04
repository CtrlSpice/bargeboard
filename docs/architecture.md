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
- Bargeboard MUST NOT describe an observed result response as legally final or
  infer when a retirement, disqualification, or correction took effect.
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
- Correction-aware observed race and sprint results under the contract below.
- Starting grids, championship standings, and points after separate review.
- Final or historical normalized pit-lane and stationary-stop durations where
  documented.
- Post-session facts unavailable from the subscribed Live Timing topics.

Historical OpenF1 access is public. Live OpenF1 access requires sponsor
authentication and MUST remain optional.

### Overlap

Source authority MUST be selected before a pipeline starts. Running both
sources for the same semantic domain MAY be supported as an explicit audit
mode later, but MUST NOT be the default.

The accepted OpenF1 result handoff is a one-way enrichment seam, not duplicate
authority: Live Timing retains root chronology and identity while OpenF1 owns
only observed result outcome and published position. A standalone OpenF1
projector cannot mutate or re-emit an already exported Live Timing root.

Every projected signal SHOULD identify its source using a bounded source value
such as `livetiming` or `openf1`. Source identity MUST NOT include endpoint,
token, cookie, or connection values.

## Session Coverage

**Status: GREEN**

The canonical model covers these fixture-backed Live Timing session formats:

| Context | Source `Type` | Source `Name` | Canonical type | Canonical name |
|---|---|---|---|---|
| Seasons 2021-2022 and `Meeting.Name="Pre-Season Test"` | `Practice` | `Practice N` | `testing` | `testing_day_N` |
| Seasons 2023-2024 and `Meeting.Name="Pre-Season Testing"` | `Practice` | `Practice N` | `testing` | `testing_day_N` |
| Seasons 2025-2026 and `Meeting.Name="Pre-Season Testing"` | `Practice` | `Day N` | `testing` | `testing_day_N` |
| Grand Prix context | `Practice` | `Practice 1` | `practice` | `practice_1` |
| Grand Prix context | `Practice` | `Practice 2` | `practice` | `practice_2` |
| Grand Prix context | `Practice` | `Practice 3` | `practice` | `practice_3` |
| Season 2020 and `Meeting.Key=1057` | `Practice` | `Practice` | `practice` | `practice_1` |
| Grand Prix context | `Qualifying` | `Qualifying` | `qualifying` | `qualifying` |
| Grand Prix context | `Qualifying` | `Sprint Shootout` | `sprint_qualifying` | `sprint_qualifying` |
| Grand Prix context | `Qualifying` | `Sprint Qualifying` | `sprint_qualifying` | `sprint_qualifying` |
| Season 2021 and Grand Prix context | `Race` | `Sprint Qualifying` | `sprint` | `sprint` |
| Grand Prix context | `Race` | `Sprint` | `sprint` | `sprint` |
| Grand Prix context | `Race` | `Race` | `race` | `race` |

In the testing rows, `N` is exactly one ASCII digit `1`, `2`, or `3`, copied to
the canonical name. Grand Prix context requires `Meeting.Name` to end with the
exact case-sensitive suffix ` Grand Prix`. Every other comparison is also exact
and case-sensitive; whitespace trimming, case folding, broad `Type` fallback,
`Number`, path parsing, and fuzzy matching are forbidden. In particular, the
2021 name `Sprint Qualifying` described a race-like sprint, while 2024 uses that
same name for qualifying-like sessions. Unknown year-constrained combinations,
meeting contexts, and field combinations remain unresolved until fixture-backed
classification exists.

Each accepted session creates a separate set of driver-session traces. Testing,
practice, qualifying, sprint qualifying, sprint, and race use one common trace
contract; they are not separate trace types. Q1/Q2/Q3 and SQ1/SQ2/SQ3 are
phases within one qualifying-like session and MUST NOT become separate traces.

## Live Timing Session Identity

**Status: GREEN**

`SessionInfo` is the sole owner of canonical Live Timing session identity and
session-generation replacement. It is a complete descriptor, not a sparse
event stream. Every root object delivered in a subscription snapshot, ordinary
feed invocation, or feed keyframe completely replaces the prior descriptor.
Missing fields never inherit values from an earlier `SessionInfo` object.
`_kf` is transport metadata only and does not change these semantics.

As of 2026-09-04T10:45:44Z, an index-plus-schedule scan retrieved 652
`SessionInfo.json` objects and 651 `SessionInfo.jsonStream` files containing 953
records across seasons 2021 through 2026. F1 origin supplied 546 objects and 545
streams; 106 of each used FastF1 mirror fallback after origin 403 responses.
Seven objects still reported `ArchiveStatus=Generating`; these are current
snapshots, not claims of finality. Every retrievable stream record was a complete
descriptor. The unavailable 2022 index and partial 2024 index do not authorize
a broader mapping. One material correction was observed: 2021 Abu Dhabi
Practice 1 used source session key `6594` in its stream and `7165` in its later
snapshot while every logical-session field remained the same. A source session
key therefore MUST NOT independently select a generation or OTLP identity. The
exact 2020 Imola practice row is an additional direct official fixture outside
that corpus.

### Descriptor Grammar

One JSON object contains independently validated logical identity, routing, and
schedule bundles:

| Source field | Contract |
|---|---|
| `Key` | Routing member; a positive canonical JSON integer fitting Int64. |
| `Meeting` | JSON object containing the required meeting fields below. |
| `Meeting.Key` | Positive canonical JSON integer fitting Int64. |
| `Meeting.Name` | String used by the exact testing and Grand Prix context rules. |
| `Type` | String matched exactly by Session Coverage. |
| `Name` | String matched exactly by Session Coverage. |
| `StartDate` | Valid Gregorian local time in exact `YYYY-MM-DDTHH:MM:SS` form. |

A canonical JSON integer is an unquoted base-ten token with no sign, fraction,
exponent, whitespace, or leading zero. The `StartDate` year is four digits from
1000 through 9999. It becomes the canonical season before any UTC-offset
calculation. Fractions, `Z`, numeric offsets, whitespace, and leap-second `60`
are invalid in this local-time field. Missing, invalid, or duplicate logical
members make `E` unresolved. An unknown classification combination is a valid
source shape but likewise leaves `E` unresolved and non-projectable. A missing,
invalid, or duplicate `Key` makes only `K_route` unavailable and emits a bounded
diagnostic; coherent `E` remains synchronized for Live Timing projection.

`EndDate` and `GmtOffset` form optional schedule metadata; they do not decide
identity. A present `EndDate` uses the same local-time grammar. A present
`GmtOffset` uses exact `[-]HH:MM:00`: positive values have no plus sign, the
absolute value is at most `14:00:00`, `14` requires zero minutes, and negative
zero is invalid. When both are valid, UTC is local time minus the offset and the
end MUST be later than the start. Absence, invalidity, or a duplicate at either
optional path clears the complete schedule bundle and emits a bounded diagnostic
without invalidating an otherwise coherent identity candidate.

Embedded `SessionStatus`, `ArchiveStatus`, `_kf`, `Number`, `Path`, meeting and
circuit display metadata other than `Meeting.Name`'s classification role, and
unknown members do not enter identity. They cannot repair an unresolved
candidate. `Path` MUST NOT be used to construct an archive URL, provide season
or session identity, or enter a signal or diagnostic. The only accepted `_kf`
value is Boolean `true`; another present value emits a bounded diagnostic but
does not change logical, routing, or replacement state.
The relative `HH:mm:ss.SSS` prefixes in static `SessionInfo.jsonStream` files are
per-stream archive publication coordinates. They preserve order within that
file but are not RFC3339 feed-envelope time, cross-topic wire order, or sporting
lifecycle boundaries.

Acceptance of the date and offset grammar does not authorize a scheduled start,
DNS time, lifecycle transition, trace boundary, polling gate, or result time.
Embedded status and archive-completion fields likewise have no lifecycle
authority. Descriptor metadata and a same-tuple correction create no
current-generation metric, span, event, log, link, or exemplar. A new-tuple
replacement may create old-generation terminal effects under existing lifecycle
rules.

### Logical And Routing Identity

The reducer keeps these separate values:

```text
T       = (season, Meeting.Key, canonical session name)
K_route = latest accepted SessionInfo.Key for the current T
E       = (season, Meeting.Key, canonical type, canonical session name)
```

`T` is the logical session tuple and the only SessionInfo-based generation
selector. Canonical name also determines one canonical type under Session
Coverage. Installing `T` also installs immutable emitted identity `E` for that
generation. Every common OTLP session identity, metric series, deterministic
trace or span ID, emitted semantic deduplication identity, and OpenF1-derived
signal uses `E`. Snapshot-seeded transport identities remain generation-local
until an effect is created; they do not require a racing signal or deterministic
ID.

`K_route` is correction-aware internal routing state used only to query and
validate source data. It MUST NOT become an OTLP attribute, metric dimension,
deterministic-ID input, or semantic deduplication input. In particular,
`f1.session.key` is not emitted: the source corrected that value in place, so it
cannot provide replay-stable session identity. Projection requires synchronized
`E`; new OpenF1 route establishment additionally requires synchronized
`K_route`.

### Corrections And Replacement

A complete coherent descriptor reduces as follows:

| Incoming descriptor | Required behavior |
|---|---|
| No current candidate | Install `T` and `E`, plus valid `K_route` or unavailable routing, without emitting. |
| Same `T`, same key | Refresh permitted non-identity metadata and restore SessionInfo synchronization. |
| Same `T`, different or restored valid key | Preserve or restore `E`, replace `K_route` and its routing epoch, and re-resolve OpenF1 routing as defined below. |
| Same `T`, missing or invalid key | Preserve or restore `E`; make routing unavailable and invalidate prior OpenF1 routing only if route availability changed. |
| New unseen `T` | Perform canonical generation replacement with valid or unavailable routing, regardless of source-key equality or reuse. |
| A retained retired `T` | Reject it as stale replay; it cannot become current again. |

A routing transition is a same-tuple routing-key change, loss, or restoration.
It never closes a root, resets topic state, changes generation, rewrites an
attribute or deterministic ID, or reopens an exported effect. Every routing
transition increments an internal OpenF1 routing epoch, returns cancellation
for work issued under the old route, and clears the frozen OpenF1 identity
bundle. A valid new route requires `/sessions` and
`/drivers` coherence before result polling resumes; an unavailable route blocks
new OpenF1 dispatch. Previously accepted result value, local revision, request
budget, deadline, and global request-spacing state remain intact. A completion
from an older routing epoch may release receiver-global request ownership and
update global spacing. A stale HTTP 429 with a valid positive `Retry-After` MUST
monotonically extend the receiver-global rate-limit deadline. No stale completion
can mutate session or result state or create a racing effect.
Repeated descriptors with unchanged routing value or unavailability do not
advance the routing epoch or restart work.

Source-key reuse across different tuples is not an identity collision and does
not block Live Timing replacement. OpenF1 remains unavailable unless its
response matches both current `K_route` and `E`. Retaining old source keys would
add no safety and is forbidden.

At most 256 retired logical tuples are retained in FIFO retirement order. Within
that explicit replay-defense horizon, a retired tuple cannot be resurrected.
Retiring the 257th generation evicts the oldest tuple; no stronger replay claim
is made after eviction or process restart. Because `E` and deterministic IDs
derive from the logical tuple rather than source key, replay after that horizon
reuses the same semantic identity instead of colliding with another session.

An accepted new tuple replaces the generation atomically in this order:

1. Create any old-generation terminal effects allowed by existing lifecycle
   rules using only old state and `E`.
2. Advance the generation token and return commands that cancel or invalidate
   timers, HTTP requests, and other asynchronous work so late completions cannot
   mutate session state.
3. Retire the old logical tuple.
4. Clear every session-scoped reducer, registry, candidate, baseline,
   watermark, deduplication set, diagnostic latch, and unresolved staging area.
5. Install the new `T`, `E`, and `K_route` under the advanced generation.
6. Reduce remaining topics from the same atomic snapshot into the new
   generation.
7. Derive new-generation hydration effects only after the complete batch is
   coherent.

Old-generation effects precede new-generation effects in reducer output, while
their trace, metric, and log delivery remains independent. Receiver-global
transport state and the OpenF1 request-spacing or rate-limit gate survive
replacement. Replacement is an export gate, not evidence of `Finished`,
`Finalised`, `Ends`, or a sporting outcome.

### Snapshot And Reconnect Reduction

Every successful subscription completion MUST reach the reducer as one explicit
batch, including a `null`, empty-object, or partial response. The batch contains
its `snapshot` delivery kind, the complete requested-topic set, exact
present-topic set, normalized updates, and one Collector observation time. The
protocol boundary validates framing, the top-level result and manifest, and
payload normalization all-or-nothing. Semantic validation remains topic-local
at the narrowest independently valid boundary. Synthesizing empty payloads for
absent topics is forbidden.

Snapshot reduction proceeds as one transaction:

1. Decode and resolve `SessionInfo` before mutating any session-scoped topic,
   independent of JSON map or sorted decoder order.
2. Apply any same-session recovery, key correction, or old/new generation
   replacement.
3. Only when identity remains synchronized, stage every other present topic's
   complete replacement or semantic failure and every requested omission
   without mutating current state.
4. Reconcile cross-topic dependencies from that complete staged state, then
   commit topic-local states and availability atomically.
5. Derive immutable signal effects and bounded operational commands only after
   the complete batch commits.

This ordering lets a new-session snapshot bind `DriverList`, status, timing, and
other state to the new generation while returning old closure effects first.
Snapshot state and effects MUST be invariant to JSON member order and decoder
ordering. Feed invocations remain strict wire-order updates; they are never
globally sorted by source time.

A successful snapshot that omits `SessionInfo`, a present null or non-object
value, an empty object, an unresolved logical bundle, or a retained stale tuple
retains prior state only as non-projectable recovery state and marks session
identity unsynchronized. Steps 3 through 5 do not run; every other present or
omitted topic outcome in that batch is discarded without mutating a generation.
None of these cases is termination or replacement. A later complete coherent
object may restore the same tuple, apply a key correction, or establish a new
generation.

When `E` becomes unsynchronized, every session-scoped Live Timing topic other
than `SessionInfo` also becomes unsynchronized and recovery-only. Restoring `E`
alone does not make stale topic state projectable. Each topic requires a later
identity-synchronized authoritative snapshot before its feed patches can resume
projection; successful omission then follows that topic's normal unavailable
rule.

Feed omission of the `SessionInfo` topic is no update. Any present feed value
that is null, non-object, empty, has an unresolved logical bundle, or names a
retained stale tuple follows the same unsynchronized recovery rule as an invalid
snapshot. A routing-only failure follows the independent `K_route` rule and does
not unsynchronize `E`. Because SessionInfo feed values are complete replacements,
generic sparse-feed recovery does not apply. A later coherent feed descriptor
can restore identity but cannot replay intervening updates.

While no synchronized identity exists, unbound Live Timing snapshot and feed
updates MUST NOT mutate a current generation or create racing effects,
deterministic IDs, durable signal candidates, or delayed observation queues. A
coherent `SessionInfo` in the same atomic snapshot allows that snapshot's
current-state hydration and baseline seeding. Identity resolved in a later
callback does not replay or hydrate data from earlier unbound snapshots or
feeds; only observations after the applicable topic resynchronizes may project.
Already-created effects remain deliverable, and generation-stamped terminal
timers or a previously frozen OpenF1 identity bundle may continue to act on
already accepted state in the retained generation.

The current protocol decoder drops successful null and empty subscription
completions and does not retain a requested-versus-present manifest. That seam
MUST be corrected before the SessionInfo reducer or any omission-dependent topic
projection is enabled.

Implementation requires compact public fixtures for every Session Coverage row
and lexical near miss; testing with no phase layer, `Started` root opening,
`Finalised` closure, singular best-lap state, and no race-like lap or gap signal;
the Abu Dhabi 2021 Practice 1 `6594` to `7165` correction before and after signal
creation; exact integer, local-date, calendar, and offset bounds; complete
snapshot, ordinary-feed, and defensive `_kf`-feed replacements;
missing fields, null, empty, embedded-status-only changes, and schedule clearing;
initial, same-tuple, corrected-key, key-reversion, new-tuple with distinct and
reused keys, and retired-tuple cases; 256 and 257 retired generations; reconnect
omission, invalidity, and later restoration; `null`, empty, partial, and permuted
snapshot batches; topic-local semantic failure; cross-topic staged coherence;
pre-identity no-replay behavior; generation effect ordering; OpenF1 route
invalidation and re-resolution; a cold terminal snapshot returning one gated
`/sessions` dispatch command; exact stable OTLP attributes and deterministic IDs
across key correction and process restart; and proof that schedule, embedded
status, archive status, path, and descriptor metadata create no
current-generation racing signal.

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
- `collector_observation` when an untimestamped external snapshot exposes only
  when this Collector observed its current state.

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
Testing, practice, sprint, and race traces omit the phase layer.

Stint spans are lap-aligned, pit-separated strategic runs so every lap remains
inside its parent. The old stint owns its in-lap and the new stint owns its
out-lap. Exact pit entry, stop, and exit times remain lap events. A drive-through
starts a new driving stint even when the tyres remain fitted.

### Status

- A finish without DNF, DNS, or DSQ MUST set the driver-session root to `OK`.
- DNF, DNS, and DSQ MUST set the driver-session root to `Error`, even when a DNF
  is classified, with a short result message.
- A race-like root without an accepted outcome at export remains `Unset`; lack
  of result coverage MUST NOT become a successful finish.
- A stint MAY be `Error` when direct evidence says it ended through a puncture,
  collision, or mechanical retirement.
- An invalidated, deleted, aborted, or retirement lap MAY be `Error`.
- Slow pace, position loss, strategy, weather, and ordinary penalties MUST NOT
  become errors solely because the result was undesirable.
- Racing incidents MUST NOT use the semantic `exception` event name. That name
  remains reserved for software exceptions.

A DNS trace MUST be a zero-duration driver-session root at the observed
competitive `Started` time. It has no stint, lap, sector, or fanned-out session
events and carries `Error` status with a `did not start` message. Without an
observed start, no DNS root is emitted. `SessionInfo` scheduled-start fallback
remains **YELLOW** and MUST NOT be implemented from the accepted descriptor
grammar alone.

Race and sprint roots start at the observed competitive start. Except for DNS,
OpenF1 result state never owns their end: they close at the accepted Live Timing
lifecycle fallback defined under OpenF1 Race-Like Result Observations. The
provisional Live Timing `Retired` field MUST NOT close a root because the source
can reverse it. Testing, practice, and qualifying-like roots start at the first
canonical `Started` state and normally close at `Finalised`; elimination closes
the driver's last phase span, not the root.

`Finished` records a competitive-stop transition but MUST leave roots open for
trailing timing facts. `Ends` is a terminal feed boundary. A testing, practice,
or qualifying-like root remains unexported until the first of `Finalised`,
`Ends`, canonical session replacement, or receiver shutdown. A race or sprint
root remains unexported until the first of `Ends`, five minutes after
`Finalised`, canonical session replacement, or receiver shutdown. The
five-minute default SHOULD be configurable.

For race-like roots, the latest accepted result observation already reduced when
the root effect is created may settle status; a result HTTP response never
triggers export. Without an accepted outcome, the root closes as unresolved
rather than inventing DNF, DNS, DSQ, or a classified finish. A later result
observation or correction uses correlated logs; an exported root MUST NOT be
resent.

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
- `f1.race_control.driver_notice`
- `f1.blue_flag.shown`

`f1.gap.lap_deficit.changed`, `f1.position.exchange`, typed penalty and
investigation, and final retirement events remain **YELLOW** until their domain
contract is recorded below. Team-radio span events are **RED**; its accepted
metadata log is defined under Logs.

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

The common OTLP session identity is exactly these attributes and types. Every
racing metric datapoint, span, and standalone racing log MUST carry all five;
domain contracts may add only their declared local attributes.

| Attribute | Type | Contract |
|---|---|---|
| `f1.season.year` | Int64 | Four-digit session season. |
| `f1.meeting.key` | Int64 | Positive Live Timing `Meeting.Key` from immutable identity `E`. |
| `f1.session.type` | String | Canonical broad type from Session Coverage. |
| `f1.session.name` | String | Canonical specific name from Session Coverage. |
| `f1.data.source` | String | `livetiming` or `openf1`. |

For metric datapoints these are exactly the common identity attributes. A metric
instrument adds only the conditional attributes declared below.

Before an OpenF1 identity bundle freezes, source rows MUST validate against the
synchronized current `K_route` and `E`. A missing routing key or conflicting
logical identity blocks bundle establishment. After bundle freeze, that bundle
remains authoritative until a routing transition or generation replacement;
temporary Live Timing identity or registry unavailability does not rewrite `E`.

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
validated source display name. Session season, meeting key, canonical type, and
canonical name are immutable in `E` from generation installation. `K_route`
remains correction-aware internal state. Selected source authority is fixed by
its domain contract. A later metadata conflict updates unexported state where
safe and emits a bounded diagnostic, but MUST NOT split an existing metric
series.

### Session Driver Registry

**Status: GREEN**

`DriverList` is the sole Live Timing owner of the all-session driver registry.
A non-empty authoritative snapshot object establishes the complete roster for
testing, practice, qualifying-like, sprint, and race sessions. Canonical driver
entry keys are positive decimal numbers without sign or leading zero and must
fit Int64. A present `RacingNumber` must use the same grammar and equal its key.
A canonical entry must contain source `Tla` as one to four uppercase ASCII
letters. More than 32 canonical driver entries makes a snapshot incoherent;
pre-freeze feed state beyond that bound is discarded with one bounded
diagnostic.

The registry freezes at the first coherent non-empty authoritative `DriverList`
snapshot after session identity resolves, including a cold mid-session
snapshot. An empty, malformed, or semantically conflicting snapshot cannot
freeze a registry. After synchronized session identity exists but before
registry freeze, sparse feed updates may enrich staged state but cannot prove
completeness. Unbound DriverList updates are discarded under Session Identity.
After freeze, metadata can fill previously empty non-identity fields, but a
roster, racing-number, or acronym conflict is diagnosed and MUST NOT rewrite
signal identity.

Reconnect to the same session preserves the frozen registry. Only an
identity-synchronized atomic snapshot can establish agreement for projection to
resume; missing or conflicting frozen drivers make registry-dependent signals
unavailable. Session replacement discards it. Non-driver keys are ignored by
the registry and cannot create racing identity. A signal arriving before
required driver resolution emits only its independently valid session-level
form and is never queued for later driver attribution.

Whenever coherent state first establishes both a frozen registry and current
authoritative `Started` or `Aborted` state without an observed root-opening
boundary, enter late-coverage mode at that state-establishing snapshot's
Collector observation time. This covers a registry first received after live
`Started` and a pre-existing registry whose `Started` was missed across a
disconnect. The snapshot itself opens no roots.

In late-coverage mode, each driver may open a root at their first subsequent
accepted driver-specific racing fact whose effective time is not before that
observation boundary. The root starts at the fact's effective time with its time
quality; it is not backdated and no prior metric, event, or lap is replayed. This
is the coverage-safe exception to normal root start boundaries. A driver with
no subsequent participation remains rootless so a later accepted DNS outcome
can create the required zero-duration trace. `Inactive`, `Finished`,
`Finalised`, `Ends`, and a later session-status transition do not themselves
cause late live root creation. A later `Started` resumes the same
first-driver-fact rule.

Registry unavailability after freeze pauses new registry-dependent projection
but never invalidates an already created effect or bounded metric candidate
whose driver attributes froze while the registry was coherent. Terminal state
reduction may close and deliver those prior candidates without waiting for
registry recovery.

Implementation requires snapshot and sparse-feed fixtures for every canonical
session type; matching and conflicting `RacingNumber`; valid and invalid
acronyms; empty, malformed, 32-driver, and 33-driver rosters; pre-freeze feed
state; cold mid-session freeze; agreeing and conflicting reconnect snapshots;
late metadata fill; signals before and after resolution; late root activation
without replay; a reconnect-recovered missed `Started`; terminal non-activation;
and session replacement.

Metric series MUST NOT use:

- Lap or stint number.
- Pit-visit, message, media, trace, or span ID.
- Timestamp.
- Tyre age or another continuously changing state.

Lap, stint, and correlation identity belongs in spans and exemplar filtered
attributes.

Meeting and circuit display metadata MUST NOT be copied into every metric series.
Uncertain identity MUST delay projection rather than emitting a label correction
that creates another series. The Live Timing Session Identity contract owns
snapshot pre-resolution and forbids unbound updates from mutating a generation,
queuing observations, or replaying after identity appears. Collector-internal
receiver metrics do not use racing identity and are outside this table.

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

Global DRS enable/disable messages belong to curated race-control logs, not these
telemetry metrics. A typed DRS race-control event remains YELLOW.

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

An accepted feed-driven change emits `f1.position.changed` with Int64
`f1.position.previous`, Int64 `f1.position.current`, signed Int64
`f1.position.delta = current - previous`, String
`f1.position.change.kind=classification_update`, and
`f1.time.quality=publication_time`. Snapshot hydration, first state without a
prior baseline, periodic Gauge observations, repeated state, and invalid state
emit no event.

The event timestamp is the `TimingData` publication time. Owner precedence is
the unexported lap containing that timestamp, then the open driver-session root;
there is no unrelated active-lap fallback. If no legal owner remains, only the
Gauge update survives. Event dedupe identity is canonical session, driver,
publication Unix nanoseconds, previous position, and current position. A replay
of the same directed change cannot emit twice across reconnect.

`classification_update` deliberately states only what the source proved. Pit
cycles, timing corrections, penalties, retirements, and on-track exchanges can
all change classification. The event MUST NOT claim an overtake, infer another
driver, add a cause from coordinates, or create a position-exchange link. A
field position histogram is **RED** because a healthy classification is already
approximately one car in every position.

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

Testing, practice, and qualifying-like `TimeDiffToFastest` fields are a different
domain and are not projected by this contract. Elapsed values are non-negative.
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
wire order. It never moves backward. A pending lap with observed boundary
`T_lap` finalizes when that watermark is greater than or equal to
`T_lap + 5s`, or at a hard phase/session boundary or receiver shutdown. Patches
from other topics do not advance this watermark. Reducer tests inject timestamps;
the functional core never reads a clock. A late patch can enrich only a
still-pending lap and cannot reopen an exported one.

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
emitted. Reinstatement carries the matched deletion record index under the
Race-Control Driver Events contract. If the qualifying phase cannot be resolved
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

For testing, practice, sprint, and race, `TimingData.BestLapTime.Value` is the
sole source. For qualifying-like sessions, the singular value owns the resolved
current phase and is authoritative over that phase's corresponding zero-indexed
`TimingData.BestLapTimes` entry. Every valid sparse plural snapshot
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

Under the snapshot-manifest contract in Live Timing Session Identity,
successful omission marks that dedicated topic unavailable, clears its current
and pending report state, preserves session-scoped consumed signatures, and
emits nothing. Unavailable is not unsynchronized. A later snapshot containing
the topic performs normal atomic replacement. Dedicated-topic projection MUST
remain disabled until the protocol carries that manifest.

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
canonical `race` and `sprint`; endpoint presence cannot enable it in testing,
practice, or qualifying-like sessions.

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
- `Finalised` attaches its events before testing/practice/qualifying closure or
  the race-like export grace period begins.
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
coordinates, telemetry freshness, and OpenF1 result observations MUST NOT affect
the count. A car in the pit lane remains running when line-level `Stopped` is
false.

The entrant roster is the frozen Session Driver Registry. A malformed canonical
driver entry makes the aggregate roster incoherent; it is not silently dropped.
Non-driver metadata, including safety and medical cars, is excluded.

An authoritative `SessionData.StatusSeries` `Started` transition activates the
metric. Direct `SessionStatus` feed state cannot activate it. A coherent cold
snapshot may instead activate only when the latest indexed series state and
direct mirror agree that the session is currently `Started` or `Aborted`. If no
frozen registry exists at activation, the lifecycle becomes active but
projection waits; the first later coherent authoritative `DriverList` snapshot
may freeze it under the Session Driver Registry contract. Later metadata
changes cannot add or remove entrants.

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
OpenF1 result observation remains independent and does not rewrite prior live
Gauge observations.

Implementation requires compact public fixtures for complete and partial
`DriverList` snapshots; non-driver entries; all-driver `TimingData` snapshots;
empty initial `Lines`; one and simultaneous `Stopped` changes; true-to-false
recovery; provisional `Retired`; pit entry/exit; pre-start, active, aborted,
inactive, finished, and cold terminal lifecycle states; DNS-like participants;
missing, invalid, deleted, and restored driver state; snapshot staging;
reconnect roster preservation; equal and backward timestamps; and exact Gauge
cardinality with no final-result side effects.

### Weather

**Status: GREEN**

Direct `WeatherData` owns seven session-scoped current-state Gauges:

| Source field | Instrument | OTLP type | Unit |
|---|---|---|---|
| `AirTemp` | `f1.session.weather.air_temperature` | Double Gauge | `Cel` |
| `TrackTemp` | `f1.session.weather.track_temperature` | Double Gauge | `Cel` |
| `Humidity` | `f1.session.weather.relative_humidity` | Double Gauge | `%` |
| `Pressure` | `f1.session.weather.air_pressure` | Double Gauge | `hPa` |
| `WindSpeed` | `f1.session.weather.wind_speed` | Double Gauge | `m/s` |
| `WindDirection` | `f1.session.weather.wind_direction` | Int64 Gauge | `deg` |
| `Rainfall` | `f1.session.weather.rainfall_detected` | Int64 Gauge | `1` |

The source pressure value is numerically millibar, which equals hectopascal, so
projection changes the unit label but not the number. Each instrument has one
series per session and exactly the common session attributes. Weather field,
driver, phase, circuit, status, freshness, delivery mode, and time quality MUST
NOT become metric attributes. Gauge `StartTimestamp` is unset.

All seven source fields are JSON strings. Accepted lexical forms are:

```ebnf
digit          = "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
nonzero-digit  = "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
whole          = "0" | nonzero-digit, { digit } ;
tenth          = whole, ".", digit ;
signed-tenth   = [ "-" ], tenth ;
```

`AirTemp` and `TrackTemp` use `signed-tenth`. `Humidity`, `Pressure`, and
`WindSpeed` use `tenth`. `WindDirection` uses `whole` and MUST be in `0..359`.
`Rainfall` is exactly `"0"` or `"1"`. Negative zero is non-canonical;
humidity is in `0.0..100.0`; pressure is positive; wind speed is non-negative.
Temperatures have no invented physical cutoff. Direction `360` and negative
directions are invalid rather than wrapped. Source zero temperature, humidity,
wind speed, and direction remain valid where their field domain permits them.

Whitespace, leading plus, leading zero, exponent, comma, alternate precision,
JSON-number or Boolean coercion, NaN, infinity, and integer overflow are
invalid. Decimal fields parse to signed Int64 tenths without an intermediate
binary float and divide by ten only while projecting Double values. Invalid
values are never clamped, rounded, or converted to zero.

Every explicit valid feed field emits one datapoint at its normalized SignalR
feed-envelope publication timestamp, including an unchanged value at a later
timestamp. A sparse payload emits only explicit fields; retained fields are not
re-emitted. Within one normalized callback, equal-time candidates for one field
coalesce in wire order and flush at callback end. A later callback timestamp not
after that field's prior emitted timestamp is replay-only for projection: source
state still reduces, but the candidate emits nothing, does not refresh metric
freshness, and neither changes nor breaks the last fresh rainfall transition
baseline. A later legal explicit field compares against that untouched fresh
baseline and projects normally from its newly supplied value.

A coherent authoritative snapshot atomically replaces the complete weather
object and emits each present valid Gauge once at Collector observation time.
Absent fields become unavailable. Any semantically invalid known snapshot field
follows the global invalid-authoritative-snapshot rule and makes the whole topic
unsynchronized; valid feed siblings continue independently only outside that
snapshot case. An empty valid object clears all fields without tombstones. A
successful requested-topic omission marks weather unavailable, clears current
state, preserves transition dedupe, and emits nothing using the subscription
manifest contract.

Each projected field independently stores its value, source timestamp,
Collector monotonic observation time, and deadline exactly 125 seconds later.
It is fresh before the deadline and stale at `now >= deadline`. Missing feed
fields do not refresh it. An invalid present feed field emits nothing, does not
refresh, and leaves its prior valid state to expire; invalid rainfall also
breaks rainfall-transition continuity. Expiry makes only that field unavailable
inside the reducer and emits no datapoint, tombstone, zero, NaN, log, or event.
A delayed timer applies all expirations before the next update and never
backfills activity.

The deadline uses monotonic Collector time, never source/Collector wall-clock
subtraction. A snapshot starts new deadlines at observation time because its
measurement age is unknowable. Disconnect makes weather non-projectable while
existing deadlines continue to age. A reconnect snapshot replaces state and
may hydrate fresh current Gauges but replays no missed samples. Direct weather
is accepted before competitive `Started` once canonical session identity is
known and stops at `Ends`; session status does not otherwise gate source
observations. `WeatherDataSeries` MUST NOT backfill, interpolate, or duplicate
this source.

#### Rainfall Transition Log

A fresh, synchronized feed change between accepted rainfall states emits one
session-level log:

```text
Body:              f1.weather.rainfall.changed
SeverityNumber:    INFO (9)
SeverityText:      INFO
Timestamp:         WeatherData feed-envelope publication time
ObservedTimestamp: Collector observation wall time
TraceID/SpanID:    unset
```

The log has the common session identity plus Boolean
`f1.weather.rainfall_detected`, Boolean
`f1.weather.previous_rainfall_detected`, and String
`f1.time.quality=publication_time`. It MUST NOT copy other weather fields or fan
out to driver spans.

First valid state, repeated state, snapshot replacement, stale-to-known,
invalid-to-known, requested-topic restoration, and session replacement establish
baseline without a log. A snapshot baseline followed by a later fresh feed
change is observed coverage and does emit. Semantic dedupe identity is session,
log name, publication Unix nanoseconds, previous Boolean, and current Boolean;
it is consumed when the reducer creates the effect and survives reconnect.
Metric and log projection/delivery remain independent.

Implementation requires compact public fixtures for all seven complete string
fields and sparse single-field feed patches; signed temperature; fractional and
zero humidity; high-altitude pressure; zero wind; directions 0, 359, negative,
and 360; both rainfall transitions; repeated values and equal-time bursts; every
lexical rejection; valid feed siblings beside an invalid field; invalid and
empty snapshots; requested-topic omission; per-field freshness immediately
before and at 125 seconds; delayed timers; disconnect/reconnect; pre-session and
terminal observations; rainfall baseline, replay, dedupe, and continuity breaks;
exact Gauge types/units/cardinality; exact log schema; and proof of no heartbeat,
interpolation, expiry signal, driver fanout, or WeatherDataSeries replay.

### Coordinate Context

**Status: GREEN for internal state and event enrichment; RED for standalone signals**

The wire topic `Position.z` remains compressed and normalizes to semantic topic
`Position`. Its coordinates are bounded internal context only. Position cadence
MUST NOT create Gauges, histograms, sums, logs, spans, events, exemplars, links,
or driver identity. In particular, Bargeboard MUST NOT port the historical
TypeScript coordinate metrics.

The decompressed root has a `Position` array. Each item is one observation with
strict RFC3339 `Timestamp` and object-valued `Entries`; the inner timestamp is
the measurement time and feed-envelope fallback is forbidden. Each driver entry
has `X`, `Y`, and `Z` as canonical signed JSON integer tokens fitting Int64.
Strings, fractions, exponents, leading zeros, `-0`, and `null` are invalid.
Coordinates are atomic per entry: axes MUST NOT merge across observations.

Raw coordinates are track-local decimetres. The reducer retains signed Int64
`x_dm`, `y_dm`, and `z_dm`; event projection converts each independently as
`float64(value) / 10` metres. Negative values and zero on an individual axis are
valid. Exactly `(0,0,0)` is an explicit unavailable sample, not the circuit
origin. Z has an arbitrary track-local origin and MUST NOT be described as GPS
altitude or guaranteed elevation.

Position identity uses the frozen Session Driver Registry for every canonical
session type. Registry freeze does not depend on the race-only Cars Running
activation rule. A later conflicting snapshot makes Position unavailable but
cannot rewrite the registry. Samples received before registry resolution are
dropped rather than queued.

A coherent observation frame contains every frozen roster driver exactly once
and no extra canonical positive keys except non-roster auxiliary keys `241`,
`242`, and `243`, which are ignored. Any other extra positive key, missing
driver, or duplicate/aliased key rejects that whole observation frame because
public startup anomalies can collide with real racing numbers. Position never
creates lasting state for an unknown or auxiliary key.

Each valid frame entry MAY have exact case-sensitive `Status` `OnTrack` or
`OffTrack`. Those values are retained only for bounded validation diagnostics
and neither gates valid XYZ. Missing or unknown strings normalize internally to
`unknown`; a non-string status is invalid only for status and does not invalidate
valid coordinates. Status MUST NOT infer pit state, off-track excursion, crash,
stationary state, retirement, DNF, or span error.

After the key set passes frame validation, a malformed or partial XYZ triplet
inserts an invalid barrier for only that driver at the frame timestamp; valid
siblings continue. After stably ordering each source array by inner timestamp,
retain at most 16 valid, all-zero, or invalid states per resolved driver. Equal
timestamps use later wire order. Out-of-order feed batches can insert within the
retained window; insertion beyond 16 evicts the oldest measurement timestamp.
All-zero and invalid states are unavailable barriers so matching cannot search
past them to an older non-zero location.

#### Event Enrichment

Coordinates MAY enrich only an already accepted, driver-specific trace event
whose existence, driver, timestamp, owner, identity, and semantics were decided
without Position. Currently eligible kinds are:

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
- `f1.race_control.driver_notice`
- `f1.blue_flag.shown`

Session-status and fanned-out track-status events are ineligible because their
driver association is not a driver-specific source fact. Future event kinds
require explicit opt-in in their GREEN domain contract.

For event time `T_event`, select the latest retained state for the same session
and driver with measurement time `P <= T_event`. It is eligible exactly when
`0 <= T_event-P <= 1_000_000_000` integer nanoseconds and its triplet is
available. The one-second boundary is inclusive. A future sample, absolute time
difference, nearest neighbour, interpolation, extrapolation, or sample from
another driver is forbidden. If the selected state is all-zero or invalid, do
not search behind it.

Selection occurs only after the independent event timestamp, owner, and identity
are final, immediately before its effect is created. Normal lap buffering can
therefore make an already received causal sample eligible, but coordinate lookup
adds no further wait. A later Position batch MUST NOT revise, duplicate, delay,
suppress, or re-emit the event. Coordinates do not alter event timestamp, time
quality, owner, identity, severity, deduplication, or span status.

Eligible enrichment adds all four attributes atomically or none:

| Attribute | OTLP type | Unit | Value |
|---|---|---|---|
| `f1.position.x` | Double | m | Track-local X in metres. |
| `f1.position.y` | Double | m | Track-local Y in metres. |
| `f1.position.z` | Double | m | Track-local Z in metres. |
| `f1.position.sample_age` | Double | s | Exact integer-nanosecond `T_event-P` converted to seconds. |

Raw decimetres, sample timestamp, source status, coordinate-system labels, and
Position entry keys are not exported. The attributes never enter resources,
metric series, trace/span IDs, event identity, or links.

#### Snapshot And Lifecycle

A coherent snapshot atomically replaces all prior Position history, processes
its inner samples by measurement time, and retains only the newest bounded
states per resolved driver. Collector observation time does not replace inner
timestamps. The snapshot emits no signal and cannot enrich historical
transitions represented by that snapshot. An empty snapshot array or successful
requested-topic omission clears all Position state without tombstones. A
structurally invalid root, frame key-set failure, or malformed XYZ entry makes
the authoritative snapshot invalid under the global unsynchronized-topic rule;
partial snapshot coordinates are non-projectable. For feed data, a frame
key-set failure rejects that frame without changing older histories, while an
entry-level XYZ failure creates the per-driver barrier above. An empty feed
array is a no-op, not a clear.

On disconnect, Position becomes ineligible before disconnect-triggered
incomplete-visit events are created, so those events receive no coordinate
context. A valid reconnect snapshot must replace state before enrichment
resumes; pre-disconnect samples cannot cross the coverage boundary. For
driver-removal, `Ends`, and shutdown processing, already accepted lifecycle
events perform their final lookup before the applicable Position history is
cleared. Other session statuses neither clear nor validate coordinates. Session
replacement clears old Position state before any new-session event. No
Collector timer, heartbeat, or wall-clock freshness calculation is used.

Implementation requires compact sanitized compressed fixtures for live feed and
snapshot shapes; multi-observation arrays; exact decimetre conversion; positive,
negative, individual-zero, and all-zero coordinates; coherent roster frames,
auxiliary keys, collisions, missing drivers, and startup anomalies; malformed
axes beside otherwise valid entries; status variants with no semantic effects;
stable ordering, equal timestamps, out-of-order insertion, and 16-state
eviction; ages at zero, one second minus one nanosecond, exactly one second, one
second plus one nanosecond, and future time; unavailable barriers; every
eligible/ineligible event class; snapshot replacement/omission; disconnect and
reconnect; zero standalone OTLP signals; and bounded diagnostics without raw
coordinates.

### Race-Control Records

**Status: GREEN**

`RaceControlMessages.Messages` is the sole source for race-control logs and
driver notices. `RcmSeries` MUST NOT emit or seed these signals. Record identity
is only immutable session identity `E`, literal topic `RaceControlMessages`, and
a collection index using exact grammar `0 | non-zero-digit, { digit }` and
fitting Int64. Content is deliberately not identity: a different index with
identical fields is another source record.

The evidence baseline is a 2024-through-2026 review of 309 public official
session streams and their final snapshots, containing 16,575 stream record
occurrences. Final snapshots used arrays, streams began with an array, explicit
feed-map keys were contiguous canonical decimals from 1 through 328, and one
non-initial stream array replayed prior indexes before adding the next. No key
reuse was observed. Two records used three fractional UTC digits; all others
used whole seconds. This supports indexed transport identity and replacement
arrays, not content deduplication or append-only array assumptions.

The same review found 2,203 lap deletion or reinstatement records. Every one was
`Category="Other"`, carried driver identity in `Message` rather than structured
`RacingNumber`, and fit the narrow grammar below. Future unmatched text remains
a curated log until fixtures justify expanding that grammar; it MUST NOT be
accepted by a permissive car-number or `LAP` substring search.

A snapshot `Messages` array or numeric-key map atomically replaces collection
state, using zero-based array position as implicit index, and seeds every
present index as consumed without telemetry. Numeric-key maps are processed in
ascending numeric index order; this source-defined member order is inside one
outer topic update and does not reorder mixed-topic feed updates. An invalid map
key makes an authoritative snapshot incoherent and is ignored with a bounded
diagnostic in a feed patch.

A feed numeric-key map patches records recursively. A fixture-backed feed array
is a complete collection replacement: retained consumed identities remain
consumed, pending retained indexes receive replacement payload, and unseen
indexes are considered in ascending order. `_deleted`, shortening, or
replacement can remove payload state but cannot remove a retained identity or
make a consumed index eligible again. Coherent deletion and reinstatement
history in a snapshot is reduced in numeric order solely for entries that the
snapshot newly consumes; already consumed entries are never folded into
correlation state again.

Successful requested-topic omission clears current and incomplete record
payload but retains same-session index identities, overflow state, and active
deletion correlation. A later feed may emit a coherent unseen index; a recovery
snapshot consumes every present index not already consumed, including a retained
incomplete identity, and seeds correlation only when that record is coherent.
Omission, recovery, and an accompanying `RcmSeries` snapshot cannot replay old
records.

An unseen feed record waits until `Category` and `Message` are strings. The
first patch that makes both coherent freezes all signal content and event time.
A later same-index replay or correction updates bounded diagnostic state only
and MUST NOT retract, replace, or re-emit any signal. Consumption occurs when
effects are created, before downstream delivery. Seen indexes survive reconnect
and reset only with canonical session replacement.

Race-control state retains at most 4,096 distinct indexes total per session,
including consumed tombstones and incomplete records. Each retained record keeps
only normalized bounded fields and a message prefix no longer than the maximum
sanitized output; raw payload text is not retained. Before applying a snapshot
or complete feed array, compute the union of retained identities and all present
indexes. A snapshot whose union exceeds 4,096 is incoherent and cannot replace
collection state. Such a complete feed array, or a 4,097th distinct feed-map
index, latches overflow for that session: the update does not replace state and
that and all later unseen indexes are neither retained nor emitted. Updates to
already retained indexes remain diagnostic-only. One rate-limited bounded
diagnostic reports overflow. No eviction can make an identity reusable. The
observed source maximum is far below this safety bound.

Record `Utc` uses exact zone-less UTC source syntax
`YYYY-MM-DDTHH:MM:SS` with optional exactly three decimal digits. Interpret it
as UTC without applying circuit offset. A valid calendar value is the event
timestamp and uses `f1.time.quality=observed`. Missing or invalid `Utc` falls
back to the feed envelope timestamp with `publication_time` and a bounded
diagnostic. Embedded clock text in `Message` is display text and never owns
chronology. Snapshots cannot use Collector observation time to manufacture
historical record effects.

Every coherent unseen feed record admitted within the identity bound creates
exactly one curated session log. Overflow suppression is the sole resource-safety
exception. Its schema is:

```text
Body:              sanitized source Message
SeverityNumber:    INFO (9)
SeverityText:      INFO
Timestamp:         resolved record event time
ObservedTimestamp: Collector observation wall time
```

Message sanitization is deterministic. For every valid Unicode scalar, any
Unicode `White_Space` scalar or scalar in general category `Cc` or `Cf` becomes
ASCII space; this includes line breaks, tabs, C0/C1 controls, bidirectional
format controls, and zero-width format controls. Consecutive spaces collapse
and leading and trailing spaces are removed. Other printable Unicode is
preserved.

ASCII-case-fold the complete whitespace-normalized body for detection only. If
it contains `http://`, `https://`, `authorization`, `access token`,
`access_token`, `access-token`, `cookie`, `secret`, `signature`, `api key`,
`api_key`, or `api-key` anywhere, replace the complete body with literal
`[redacted-message]`. This intentionally conservative whole-message rule has no
overlap or scan-order behavior and is stronger than preserving a sanitized URL.
Otherwise retain the whitespace-normalized body. Finally truncate the selected
result to at most 4,096 UTF-8 bytes by dropping an incomplete final scalar.
Diagnostics MUST NOT contain body or raw payload text.

Every log carries common session identity, Int64 `f1.race_control.index`, String
`f1.race_control.category`, and `f1.time.quality`. Optional valid source fields
project as Int64 `f1.race_control.lap`, String `f1.race_control.flag`, String
`f1.race_control.scope`, Int64 `f1.race_control.sector`, String
`f1.race_control.status`, and String `f1.race_control.mode`. Body truncation adds
Boolean `f1.race_control.message_truncated=true`; whole-message redaction adds
Boolean `f1.race_control.message_redacted=true`.

Known exact categories `Flag`, `Other`, `Drs`, `SafetyCar`, and `CarEvent`
normalize to `flag`, `other`, `drs`, `safety_car`, and `car_event`. Every other
string normalizes to `unknown`. Optional enum strings are accepted only as one
to 64 ASCII letters, digits, spaces, hyphens, or underscores, with an
alphanumeric first and last character. ASCII letters lowercase and each maximal
separator run becomes one underscore. Optional `Lap` is a non-negative and
`Sector` is a positive canonical decimal JSON number token fitting Int64;
fractions, exponents, signs, leading zeros other than literal `0`, strings, and
`null` are invalid. Invalid optional fields are omitted with a diagnostic and do
not suppress the mandatory log.

A structured `RacingNumber` must be a canonical positive decimal string fitting
Int64 and resolve uniquely in the frozen Session Driver Registry. Outside an
exact repair message, only that structured field can attribute a log. For an
exact repair message, an absent structured field permits its resolved textual
driver; a present structured field must itself be valid and resolve to the same
driver. A present malformed, unresolved, or disagreeing structured field is an
identity conflict. It omits all driver attributes and trace correlation from the
log, suppresses driver event and metric effects, and produces a bounded
diagnostic containing neither identity value nor message text.

A successfully resolved structured or permitted textual identity adds Int64
`f1.driver.number` and String `f1.driver.acronym` to the log. When that record
also creates a driver event, the log MAY share the event owner's TraceID and
SpanID. A log-only record MAY correlate only to an already-projected lap or root
whose final interval contains event time; an open provisional root is not
sufficient. Otherwise the independently valid session log remains
uncorrelated.

### Race-Control Driver Events

**Status: GREEN**

Each race-control record creates at most one driver event under this precedence:

1. Exact `Category="Other"` lap deletion grammar with a resolved target.
2. Exact `Category="Other"` lap reinstatement grammar with a matched deletion.
3. Structured blue flag.
4. Generic structured driver notice.

A blue flag is structurally exactly `Category="Flag"`, `Scope="Driver"`, and
`Flag="BLUE"`. With a resolved structured `RacingNumber`, it emits
`f1.blue_flag.shown`; unresolved identity makes it log-only and MUST NOT
downgrade to a generic notice. A generic notice is an exact `CarEvent`, or an
exact non-blue driver-scoped `Flag`, with resolved structured `RacingNumber`; it
emits `f1.race_control.driver_notice`. Specialized records MUST NOT also emit the
generic event. Arbitrary message text is never scanned for generic car mentions.

Every driver event carries Int64 `f1.race_control.index`, normalized String
`f1.race_control.category`, sanitized String `f1.race_control.message`, all
applicable valid optional normalized source fields, Int64 `f1.driver.number`,
String `f1.driver.acronym`, and `f1.time.quality`. Message truncation also adds
Boolean `f1.race_control.message_truncated=true`; redaction adds Boolean
`f1.race_control.message_redacted=true`. Generic and blue-event owner precedence
is the unexported driver lap containing event time, then the open driver-session
root containing it. If no legal owner remains, only the log and any independently
valid metric emit.

Event identity is record identity plus event name and resolved driver. A
different source index remains a distinct event even when content is identical.
Log, trace, and metric effects are independently projectable and independently
deliverable; failure of one does not suppress another.

Penalty, investigation, track-limit, DRS, safety-car, sector-flag, and other
text remains log-only unless it is an exact lap repair or satisfies the
structured generic rule above. Bargeboard MUST NOT create penalty,
investigation, or track-limit counters; typed penalty events; penalty spans; or
inferred sporting outcomes. Race-control track flags never duplicate
`f1.track_status.changed` events. The pending typed DRS, penalty, and
investigation candidates require their own future GREEN contract before this
prohibition can change.

Every newly consumed coherent resolved structured blue record eligible below
contributes one to:

```text
Name:        f1.driver.blue_flags
Type:        Int64 Sum
Unit:        {flag}
Temporality: Delta
Monotonic:   true
Series:      one per driver per session
Cadence:     terminal fold into non-empty aligned ten-second event-time windows
```

Blue accumulation opens at the first authoritative indexed `Started` UTC,
including one recovered from coherent snapshot history. `Inactive`, `Aborted`,
`Finished`, and `Finalised` neither pause nor close it. A resolved record is
buffered as a metric candidate only when processed after opening and before
`Ends`, and its event time is at or after `Started` UTC. No blue metric datapoint
is projected before terminal folding, so a later cross-topic end timestamp
cannot revise an exported interval.

The first authoritative indexed `Ends` closes candidate admission in source wire
order. A live feed transition immediately sorts admitted candidates by event
time and then record index, discards any whose event time is after `Ends` UTC
with a bounded diagnostic, and folds the remainder through the standard aligned
Delta intervals with the final interval clipped exactly to `Ends` UTC. A
candidate exactly at `Ends` UTC is included only when processed before the
transition.

An `Ends` recovered only from snapshot history records the same closing boundary
but emits no Sum delta under the global snapshot rule; it defers the fold until
receiver shutdown or session replacement. Without any `Ends`, those triggers
instead close at the greatest admitted candidate time. A feed `Ends` and either
deferred terminal trigger fold already admitted candidates even while a missing
or conflicting reconnect registry pauses new projection. Candidate attributes
were frozen while coherent and need no current registry lookup. No candidate
means no datapoint.

Snapshots, replayed or overflow-suppressed indexes, pre-open and post-close
records, and unresolved blue records create no metric candidate. Candidate
storage is bounded by the 4,096-index race-control limit and survives reconnect.
The Sum uses only common session and per-driver metric attributes. A legally
owned blue event MAY supply an exemplar; owner absence does not suppress the
Sum. Trace events remain live and independent of delayed metric folding.

#### Lap Deletion And Reinstatement

Only complete messages matching this case-sensitive grammar are eligible for
the existing specialized events; unsupported variants remain log-only:

```ebnf
message = numbered-time-deletion | numbered-lap-deletion
        | phased-time-deletion | phased-durationless-deletion
        | time-reinstatement | lap-reinstatement ;

numbered-time-deletion =
  "CAR ", car-number, " (", acronym, ") TIME ", lap-duration,
  " DELETED - ", numbered-time-reason ;

numbered-lap-deletion =
  "CAR ", car-number, " (", acronym, ") LAP DELETED - ",
  numbered-lap-reason ;

phased-time-deletion =
  "CAR ", acronym, " TIME ", lap-duration,
  " DELETED - TRACK LIMITS AT TURN ", positive,
  " (NEXT LAP) (", q-phase, ")" ;

phased-durationless-deletion =
  "CAR ", acronym,
  " TIME DELETED - TRACK LIMITS AT TURN ", positive,
  " (NEXT LAP PIT) (Q2)" ;

time-reinstatement =
  "CAR ", car-number, " (", acronym, ") TIME ", lap-duration,
  " WILL BE REINSTATED" ;

lap-reinstatement =
  "CAR ", car-number, " (", acronym, ") LAP ", positive,
  " WILL BE REINSTATED" ;

numbered-time-reason = completed-track-limit | next-lap-track-limit
                     | completed-and-next-lap-track-limit
                     | pit-entry-track-limit | double-yellow
                     | completed-double-yellow ;

numbered-lap-reason = completed-track-limit
                    | completed-track-limit, " (PIT)"
                    | next-lap-pit-track-limit | double-yellow
                    | completed-double-yellow
                    | completed-double-yellow, " (PIT)" ;

completed-track-limit =
  "TRACK LIMITS AT TURN ", positive, " LAP ", positive, " ", clock ;
next-lap-track-limit =
  "TRACK LIMITS AT TURN ", positive, " (NEXT LAP)" ;
next-lap-pit-track-limit =
  "TRACK LIMITS AT TURN ", positive, " (NEXT LAP PIT)" ;
completed-and-next-lap-track-limit =
  "TRACK LIMITS AT TURN ", positive, " LAP ", positive,
  " (NEXT LAP)" ;
pit-entry-track-limit =
  "TRACK LIMITS AT PIT ENTRY LAP ", positive, " ", clock ;
double-yellow = "DOUBLE YELLOW AT TURN ", positive ;
completed-double-yellow =
  "DOUBLE YELLOW AT TURN ", positive, " LAP ", positive, " ", clock ;

q-phase      = "Q1" | "Q2" ;
lap-duration = minutes, ":", second, ".", digit, digit, digit ;
clock        = hour, ":", second, ":", second ;
minutes      = digit, { digit } ;
second       = ( "0" | "1" | "2" | "3" | "4" | "5" ), digit ;
hour         = digit | ( "0" | "1" ), digit
             | "2", ( "0" | "1" | "2" | "3" ) ;
car-number   = positive ;
positive     = non-zero-digit, { digit } ;
acronym      = upper, upper, upper ;
```

`digit` is ASCII `0..9`, `non-zero-digit` is ASCII `1..9`, and `upper` is ASCII
`A..Z`. Every numeric production must fit Int64. `lap-duration` also satisfies
the accepted timing duration grammar. The repair parser runs only for exact
`Category="Other"`. Its exact three-letter acronym must resolve in the frozen
Session Driver Registry. Number and acronym must resolve uniquely to the same
driver when both are present. A control or format scalar anywhere in the
original message, malformed complete message, unresolved driver, or
structured/textual driver conflict makes the record log-only.

At each validated contiguous `NumberOfLaps` boundary `N -> N+1` that closes an
observed canonical lap identity under Driver Lap And Sector Timing, the reducer
retains a correlation summary of driver, canonical phase-or-`none`, canonical
lap `N`, source boundary value `N+1`, and optional currently accepted reported
or reconstructed duration. The metric-only completion after a cold snapshot
creates no summary. During the five-second lap buffer, summary duration follows
the Timing Repair rules: a later accepted value can fill it and a conflict can
clear it. A race-control record matches summary state at its own wire-order
processing point and is never queued to await timing repair. Lap export freezes
the summary. A later summary change cannot revise an already consumed
race-control effect or its target identity.

Buffered and already exported laps remain eligible. Retention is capped at 256
lap summaries per driver per session without eviction; encountering a 257th
latches repair correlation unavailable for that driver and makes later repair
records log-only. Reconnect preserves summaries and session replacement clears
them.

A deletion target is selected by filtering those summaries with every clue in
the exact message: driver always, source boundary value for explicit `LAP n`
from a completed reason, head duration when present, and explicit `Q1` or `Q2`
suffix when present. The source label is never compared directly to canonical
lap `N` and the reducer MUST NOT try both `n` and `n-1`. Durationless messages
without an explicit completed `LAP n` target are log-only. Exactly one summary
must remain. Current receiver phase and outer structured
`RaceControlMessages.Lap` are publication context and MUST NOT enter selection.
Contradictory or ambiguous target lap, duration, or qualifying phase emits only
the log.

An accepted deletion sets that target's active deletion identity to its source
index and stores the deletion message's optional duration and optional source
reason-lap label; a later accepted deletion for the same target replaces that
relation. A time reinstatement must match exactly one active deleted target for
that driver and exact stored deletion duration. A lap reinstatement must match
exactly one active deleted target for that driver and stored source reason-lap
label across all phases; it is not matched against canonical lap number.
Successful reinstatement carries Int64 `f1.race_control.related_index` for the
active deletion and clears that target's active deletion state. No active or
unique match makes the record log-only. Record payload removal and reconnect do
not clear correlation; session replacement does.

A permitted successfully parsed textual driver adds the same driver attributes
to the log even when target selection fails. Deletion and reinstatement events
carry the common driver-event attributes plus Int64 `f1.lap.number`, String
`f1.session.phase`, and optional Double `f1.lap.reported_duration` in seconds
when the message supplies a duration. A message with an explicit source
reason-lap or reinstatement lap also carries Int64
`f1.race_control.reason_lap`. Reinstatement additionally carries
`f1.race_control.related_index`. Their owner precedence, timestamp, immutability,
and fallback rules remain those in Timing Repair And Boundaries. Timing rollback
MUST NOT duplicate the race-control event.

Coherent snapshot repair records that become consumed for the first time seed
active-deletion state in ascending index order when driver and target summaries
are already resolvable, without logs or events. A reinstatement in that same
history applies only to an earlier active deletion. Already consumed indexes,
same-index corrections, shortened snapshots, and removed payload do not refold
or clear correlation. Snapshot payload cannot manufacture missing lap history.

Implementation requires compact public fixtures for array/map snapshots;
keyframe and non-initial feed arrays; numeric-key patches; partial records;
same-key replay/correction; new-key exact content duplicates; deletion and
shortening; invalid, maximum, and overflowing indexes; bounded partial records;
the 4,096-index and 256-lap safety bounds; whole-second and three-digit
fractional zone-less UTC plus publication fallback; all known and unknown
categories; exact numeric and enum validation; body controls, printable Unicode,
redaction, and truncation; every driver-event precedence class; blue,
black-and-white, and CarEvent notices; every accepted and near-miss repair
grammar branch; structured/textual driver conflicts; explicit, duration,
ambiguous, phase-delayed, and unresolved lap targets; repeated deletions and
matched/unmatched reinstatements; snapshot correlation seeding; track-status
coexistence; owner and no-owner paths; Delta blue-flag windows; coordinate
enrichment; requested-topic omission and recovery; `RcmSeries` coexistence and
non-seeding; feed and snapshot-recovered `Ends`; terminal folding during
registry unavailability; and independent log/trace/metric delivery failure.

## OpenF1 Race-Like Result Observations

**Status: GREEN**

This contract applies only to canonical `race` and `sprint` sessions. OpenF1
`/session_result` is a scraped, correction-aware current result snapshot. It
exposes no source publication time, effective time, finality flag, or public
revision identifier. Bargeboard MUST call it an observed result snapshot, never
an FIA-final result. Practice, testing, qualifying-like results, historical
backfill, grids, result metrics, points, and championship state remain outside
this slice.

Live Timing remains the sole owner of trace chronology, participation facts,
children, events, and deterministic trace identity. OpenF1 owns only the latest
accepted race-like outcome and published position. `TimingData.Retired`, any
`Stopped` or status field, telemetry cessation, last completed lap, pit state,
result duration, and result gap MUST NOT infer an outcome or retirement time.
The handoff is implemented beside the Live Timing session reducer because a
standalone result projector cannot enrich an unexported root safely.

### Adapter And Fetch Boundary

The first implementation exposes this receiver configuration and defaults it to
disabled so cross-source network egress is explicit:

```yaml
receivers:
  f1livetiming:
    finalised_grace: 5m
    openf1_results:
      enabled: false
      endpoint: https://api.openf1.org/v1
```

The endpoint must be an absolute HTTPS URL without user-info, query, or fragment.
It must not end in `/`; request construction appends the literal relative
segments `/sessions`, `/drivers`, or `/session_result` to the unchanged endpoint
string before adding the encoded query. A URL-reference resolver that could drop
the configured `/v1` or an opaque proxy prefix is forbidden. Plain HTTP is
accepted only for a loopback integration-test server. Redirects MUST NOT be
followed. This slice has no OpenF1 credential setting, OAuth flow, or live
sponsor authentication. The F1 TV token MUST NOT be attached to an OpenF1
request.

`finalised_grace` is a positive canonical decimal integer followed by exact
suffix `s` or `m`, with no sign, fraction, whitespace, or compound duration. Its
resolved duration must be from one second through 30 minutes inclusive and
defaults to five minutes. It governs the race-like root export timer whether or
not OpenF1 observation is enabled.

Polling first becomes eligible when the adapter is enabled; immutable Live
Timing identity `E` is available; `K_route` is synchronized; the Session Driver
Registry is frozen; and an authoritative indexed `Finished`, `Finalised`, or `Ends` has
been accepted, including terminal state recovered from a coherent snapshot.
`Finished` starts observation eligibility but is not result finality or a root
boundary.

The adapter requests these endpoints sequentially with
`Accept: application/json`, `Accept-Encoding: identity`, and the exact canonical
positive decimal `K_route` captured in that request's routing epoch:

```text
GET /sessions?session_key=<K_route>
GET /drivers?session_key=<K_route>
GET /session_result?session_key=<K_route>
```

It MUST NOT use `latest`, broad meeting/year/name queries, mutable result-field
filters, pagination parameters, or response order as identity. `/sessions` and
`/drivers` establish one frozen OpenF1 identity bundle for that routing epoch and
logical tuple. Once that succeeds, only `/session_result` is polled. A 200
response must have media type
`application/json` after case-insensitive media-type parsing; parameters such as
`charset=utf-8` are allowed. Any content encoding other than absent or exact
`identity` is rejected. The 256 KiB body cap applies to bytes read after HTTP
framing and before JSON decoding; read at most one extra byte to detect overflow.
The body contains exactly one top-level array and no trailing JSON value and is
discarded after reduction. Unknown object members are ignored inside the bound.

The evidence baseline is all 77 completed, non-cancelled Race and Sprint
sessions exposed for 2024 through 2026-09-04: 1,572 result rows. Exact-key
responses had at most 22 rows; maximum observed bodies were 381 bytes for
`/sessions`, 9,168 bytes for `/drivers`, and 4,065 bytes for
`/session_result`. OpenF1 exposes no `ETag`, `Last-Modified`, update time, or
settlement marker, and a later correction replaces public current state rather
than preserving source revision history.

### Cross-Source Identity

A `/sessions` response is coherent only when it contains exactly one object with:

- Positive canonical JSON integer `session_key` equal to the request's
  `K_route`.
- Positive canonical JSON integer `meeting_key` equal to the logical tuple's
  `Meeting.Key`.
- Four-digit canonical JSON integer `year` equal to the frozen season.
- Exact String `session_type="Race"`.
- Exact String `session_name="Race"` for canonical `race`, or `"Sprint"` for
  canonical `sprint`.
- Valid RFC3339 `date_start` and `date_end` with explicit offsets, with end after
  start.
- Exact Boolean `is_cancelled=false`.

Schedule timestamps establish OpenF1 identity coherence only. They MUST NOT gate
polling or result acceptance, replace Live Timing boundaries, create a DNS time,
or be compared to Live Timing with an invented tolerance. Public unauthenticated
OpenF1 may withhold the current session until after root export; a compatible
configured endpoint may expose it earlier. Either case is valid coverage and
MUST NOT delay a root beyond its Live Timing export gate.

A `/drivers` response is coherent only when it contains one to 32 objects. Each
requires canonical Integer `session_key` equal to the request's `K_route`,
`meeting_key` equal to the logical tuple's `Meeting.Key`, a unique positive
canonical Integer `driver_number` fitting Int64, and String `name_acronym` of
one to four uppercase ASCII letters exactly equal to the frozen Live Timing
acronym for that number. Its driver-number set MUST equal the frozen Session
Driver Registry exactly. Missing, additional, duplicate, or conflicting drivers
reject the complete identity response. Names, teams, colours, images, and other
OpenF1 driver fields cannot rewrite Live Timing identity.

Zero or multiple session rows, cancellation, an empty driver response, and any
identity conflict leave the OpenF1 identity bundle unavailable. Once frozen it
survives a same-tuple Live Timing reconnect with the same `K_route` and is not
re-resolved from mutable display metadata. A routing transition invalidates the
bundle and request epoch under Live Timing Session Identity; every stale
completion is discarded before semantic reduction. When eligibility still
holds, bundle establishment restarts at `/sessions` under the existing
request-spacing, backoff, budget, and deadline gates; the transition resets none
of those bounds.

### Result Snapshot Reduction

A non-empty `/session_result` response is accepted only as one coherent whole.
Every frozen driver appears exactly once and no other driver appears. Each row
requires canonical Integer `session_key` equal to the frozen bundle's
`K_route`, `meeting_key` equal to the logical tuple's `Meeting.Key`, a roster
`driver_number`, `position` as null or a positive canonical Integer no greater
than roster size, `number_of_laps` as null or a non-negative canonical Integer
fitting Int64, and exact Boolean `dnf`, `dns`, and `dsq`. Non-null positions are
unique and exactly one row has position 1. At most one status Boolean may be
true. One malformed row rejects the entire response and retains prior accepted
state.

Rows reduce in this precedence-free table:

| Source condition | Outcome | Additional validation |
|---|---|---|
| `dsq=true` | `dsq` | Laps may be null or non-negative. |
| `dns=true` | `dns` | Position is null and laps are exactly zero. |
| `dnf=true`, position present | `classified_dnf` | Laps are non-null and non-negative. |
| `dnf=true`, position null | `unclassified_dnf` | Laps are non-null and non-negative. |
| All flags false, position present | `finished` | Laps are positive. |
| All flags false, position null | `unresolved` | Laps are positive. |

The unique position-1 row must reduce to `finished`. `Classified` means only
that OpenF1 supplied a numeric position; lap percentage and FIA classification
rules are not reconstructed. The 2024-through-2026 baseline contained 19
classified DNF rows, 121 unclassified positive-lap DNF rows, 28 zero-lap DNF
rows, 17 exact DNS rows, nine DSQ rows including eight with null laps, and three
all-false unresolved rows. No row had overlapping true flags.

`points`, `duration`, `gap_to_leader`, and every other result field are ignored
in this slice. They do not affect validation, semantic revision identity,
traces, metrics, events, or logs. A 200 empty array and HTTP 404 both mean result
unavailable at that observation; neither clears prior state nor creates an empty
revision.

The canonical result value is the array sorted by driver number containing only
driver number, position-or-null, and outcome. JSON formatting, object-member or
row order, and ignored-field changes are semantically equal. The first accepted
value is Collector-local revision 1. A later coherent complete value increments
the revision exactly once when it differs from the immediately prior accepted
value; reverting to older content is another revision. This revision is neither
an OpenF1 version nor a finality marker and may restart at 1 after process state
loss.

The reducer retains only the frozen identity bundle, latest canonical result,
local revision, bounded polling state, and diagnostic latches. Raw responses and
prior revision bodies are never retained.

### Root Outcome And Closure

An OpenF1 response never triggers root export. A live accepted `Finalised`
starts the configurable grace timer, default five minutes, at the transition's
Collector monotonic observation boundary. A coherent snapshot that first
recovers current `Finalised` starts it at that snapshot's observation boundary.
The source `Utc` remains the eventual span-end candidate, not the timer origin.
Repeated or replayed `Finalised` state does not restart the timer. Timer actions
carry session generation and are reduced in the same queue as source updates.

The single-owner reducer order decides whether a completed result observation
was accepted before or after the existing `Ends`, Finalised-grace,
session-replacement, or shutdown root effect. Result arrival changes only the
latest retained outcome; it does not reshape a provisional trace tree. At effect
creation, each Race/Sprint root receives:

| Attribute | Type | Rule |
|---|---|---|
| `f1.result.outcome` | String | Effective outcome, or `unresolved` without an accepted row. |
| `f1.result.source` | String | `openf1` only when an accepted row supplied the outcome. |
| `f1.result.snapshot.revision` | Int64 | Present only with an accepted row. |
| `f1.result.position` | Int64 | Present only for a numeric source position. |

The root keeps `f1.data.source=livetiming`; `mixed` is forbidden. Effective root
status is:

| Outcome | OTLP status | Exact description |
|---|---|---|
| `finished` | `OK` | Empty. |
| `classified_dnf` | `Error` | `classified did not finish` |
| `unclassified_dnf` | `Error` | `did not finish` |
| `dns` | `Error` | `did not start` |
| `dsq` | `Error` | `disqualified` |
| `unresolved` | `Unset` | Empty. |

Accepted feed evidence of participation is a post-competitive-start validated
`NumberOfLaps` transition, live-created lap or sector child, or accepted pit
entry or exit boundary. Snapshot hydration, roster membership, raw CarData,
Position, line-level `Stopped` or `Retired`, driver-addressed race-control text,
and TeamRadio metadata are not participation evidence. In particular, a static
non-zero coordinate observed after `Started` cannot override DNS.

A DNS row with no participation evidence changes projection only: an existing
provisional root is emitted as the zero-duration DNS form at observed
competitive start, with all provisional children and fanned-out session/track
events omitted. A rootless late-coverage entry may project the same DNS root.
The reducer need not delete or reconstruct provisional state, so a correction
accepted before export simply changes the later projection decision.

A DNS row conflicting with accepted participation reduces to effective
`unresolved`, preserves the ordinary provisional tree, and emits one
payload-free diagnostic. Without observed competitive start, no DNS root is
emitted. Result state never creates a root for `finished`, either DNF, `dsq`, or
`unresolved`; those outcomes enrich only an already-created Live Timing root.
No result arriving after the export gate creates or resends a root.

Every non-DNS root uses a Live Timing lifecycle fallback end. Accepted indexed
`Ends` uses its record UTC; Finalised-grace expiry uses the accepted `Finalised`
UTC rather than timer time; replacement or shutdown before either uses the
greatest accepted source timestamp already owned by the root or descendants.
The boundary is raised to any later already-owned child/event end and never
precedes root start. These driver-level fallbacks use
`f1.time.quality=estimated` because the session boundary does not identify the
driver's physical finish or retirement.

Collector shutdown time, grace timer time, OpenF1 observation or schedule time,
result duration, last completed lap, telemetry cessation, `Retired`, and
`Stopped` are forbidden non-DNS end candidates. An individual finish-crossing
mapping remains YELLOW until a direct fixture-backed source contract exists.

Once a root effect is created, result attributes, status, and interval freeze.
A later first observation or correction cannot reopen, resize, restatus, or
resend that root or any child.

### Result Observation Logs

Logs are the only standalone result signal in this slice. The first accepted
result revision creates one log per driver:

```text
Body:              f1.driver.result.observed
SeverityNumber:    INFO (9)
SeverityText:      INFO
Timestamp:         response-completion Collector observation time
ObservedTimestamp: same value
```

A later semantic revision creates one log only for each changed driver, with
body `f1.driver.result.revised` and that revision's single response-completion
observation time. Every result log carries common session identity with
`f1.data.source=openf1`, Int64 `f1.driver.number`, String
`f1.driver.acronym`, Int64 `f1.result.snapshot.revision`, String
`f1.result.outcome`, and `f1.time.quality=collector_observation`. A numeric
position adds Int64 `f1.result.position`.

Revision logs additionally carry String `f1.result.previous_outcome` and
optional Int64 `f1.result.previous_position`. Attribute absence represents a
null position; no sentinel is used. Ignored result fields, response hashes,
endpoints, and polling state MUST NOT enter log bodies or other attributes.

A result log MAY use the deterministic TraceID and driver root SpanID whenever
that canonical root has already been created, exported or not. Unlike racing
event ownership, the observation timestamp need not fall inside the root because
the log records later source state about it. No canonical root means no trace
correlation; a log never creates an orphan span solely for correlation.

Logs are consumed when effects are created. Downstream failure cannot cause an
HTTP refetch, duplicate effect generation, root suppression, Live Timing
reconnect, or internal delivery retry. Post-export correction is represented
only by these immutable logs in this slice.

### Polling, Failure, And Bounds

OpenF1 requests have a ten-second timeout, run at most one at a time, and start
at least one second apart. One adapter attempt is exactly one RoundTripper call:
the client and transport disable redirects, automatic retries, and automatic
idempotent request replay. The attempt budget is charged immediately before
dispatch. After the OpenF1 identity bundle freezes, result polls begin at least
30 seconds after the prior request completes. Before bundle freeze, each retry
starts again at `/sessions`, then requests `/drivers` only after a coherent
session row.

Observation stops after the first of one hour of injected monotonic time from
eligibility, 128 total HTTP attempts, revision-overflow latch, session
replacement, or shutdown. These are resource bounds, not settlement heuristics;
equal responses at revision 64 or earlier never prove finality or stop polling
early.

Response handling is:

| Response | Behavior |
|---|---|
| Valid 200 | Validate atomically; reset consecutive transient failures. |
| Empty 200 or 404 | Retain state, reset transient failures, and retry identity or result after the normal 30-second interval. |
| 401 or 403 | Retain state and retry after five minutes within budget. |
| 429 | Honor valid `Retry-After`, with a minimum 30-second delay. |
| Network error, timeout, 408, 425, or 5xx | Retain state and use transient backoff. |
| Oversized, non-JSON, malformed, or semantically invalid 200 | Retain state and use transient backoff. |
| Any non-200 2xx, including 204 or 206 | Retain state and use transient backoff. |
| Any final 1xx, any 3xx, or other 4xx | Stop polling for this session. |

`Retry-After` accepts canonical non-negative decimal seconds or a valid HTTP
date in IMF-fixdate form. Decimal syntax has no sign or leading zero other than
literal `0` and must fit Int64 seconds. An HTTP-date delay is computed from the
injected response-completion wall time and converted immediately to a monotonic
deadline. Missing, malformed, overflowing, zero, or past `Retry-After` uses
transient backoff. A valid positive delay is raised to 30 seconds when shorter;
one beyond the remaining observation budget ends observation.

For transient failures, `n` is the one-based count of consecutive responses or
transport outcomes classified as transient by the table. Wait
`min(30s * 2^(n-1), 5m)` using saturating integer arithmetic. A coherent or
empty 200 and a 404 reset `n` to zero; fixed-delay 401, 403, and valid 429 do not
change it. The observation deadline and request budget always win.

At revision 64, a different coherent 65th value latches revision overflow,
retains revision 64, stops polling, and emits one operational diagnostic. The
maximum racing-log effect count is therefore 32 initial logs plus 63 revisions
times 32 drivers, or 2,048 per session. Operational diagnostics are rate-limited
to once per session for each bounded category: identity, transport, HTTP status,
rate limit, response size, decode, validation, revision overflow, and request
budget. They contain no endpoint, query, header, credential, response body,
name, acronym, driver number, or result value.

A same-tuple, same-route Live Timing reconnect preserves the identity bundle,
current result, revision, polling budget/deadline, and diagnostic latches.
Polling may continue while Live Timing is disconnected. Once cross-source
identity has frozen, a later missing or conflicting Live Timing registry snapshot
does not invalidate it: coherent result completions still create revisions and
logs, and the latest accepted revision may enrich a root effect. A routing
transition is the explicit exception and follows Live Timing Session Identity.
Before bundle freeze, registry or routing unavailability prevents requests from
starting.

Session replacement first creates old root effects from only already accepted
state, then generation-invalidates and returns cancellation for the old HTTP
request, and then clears all result and session polling state. A receiver-global
request-start gate, including a rate-limit deadline, survives session replacement
and prevents the new session from bypassing prior spacing or `Retry-After`.
Every request and timer action carries session generation and routing epoch. A
stale completion releases only receiver-global request ownership, updates
global spacing, and MUST monotonically extend the global rate-limit deadline from
a valid positive HTTP 429 `Retry-After`; it creates no racing effect or
session-state mutation.

Cancellation does not release the one-request slot until RoundTrip returns. New
session dispatch waits for that release. Shutdown begins with a reducer action
that latches HTTP dispatch and timer scheduling disabled before returning
cancellation commands; no later completion may re-enable or return new work.
The shell executes cancellation without a final fetch but keeps the reducer
queue available for the request completion. After the request goroutine joins,
the shell enqueues one FIFO shutdown barrier; reducing that barrier exports from
accepted state and then clears global adapter state.
HTTP completions and timer actions enter the same single-owner reducer queue as
Live Timing updates; an HTTP goroutine MUST NOT mutate session state directly.

Implementation requires compact sanitized fixtures for exact Race and Sprint
session identity; every empty, duplicate, cancelled, malformed, and conflicting
session/driver response; 1-, 32-, and 33-driver rosters; all six outcomes;
classified and zero-lap DNF; null-lap DSQ; unresolved rows; multiple flags; DNS
shape, static post-start coordinates, and real participation conflict; unique
and duplicate positions; whole-snapshot reorder, repeat, correction, and
reversion; invalid-between-valid state; revision
64 and overflow; root creation, conversion, status, hard-end boundaries, and
pre/post-export corrections; exact initial/revision log schemas and correlation;
every HTTP response class and `Retry-After` form; request spacing, timeout,
backoff, deadline, budgets, cancellation, stale completion, reconnect, and
shutdown; independent trace/log failure; and proof of no metric, result event,
retirement time, points, duration, gap, raw response, or credential output.

## Pending Metric Candidates

**Status: YELLOW**

The following domains require the same candidate-by-candidate review before
implementation:

- Tyre-change event settlement.
- Dedicated pit-topic fallbacks and physical stationary spans.
- Typed DRS, penalty, and investigation events.
- Retirement and individual finish-crossing events; result and points metrics;
  grids; historical reconciliation; and championship facts.
- Explainable pace, consistency, degradation, and pit-loss analysis.
- Receiver lag, payload size, reconnects, and validation failures.

Pending candidates MUST NOT be inferred from the historical TypeScript metrics.

## Logs

**Status: GREEN**

Default logs SHOULD be curated:

- Race-control messages.
- Team-radio metadata.
- OpenF1 result observations and revisions.
- Session and track transitions that benefit from a textual record.
- Data-quality, decoding, and receiver failures.

Redacted normalized source payload logging MUST be opt-in. High-frequency car
and position samples MUST NOT be raw logs by default. Raw capture is never a
bypass around security rules.

Log source timestamps and observed timestamps MUST remain separate fields. They
may be equal only for a domain explicitly using
`f1.time.quality=collector_observation`; equality then asserts no source
timestamp. Logs SHOULD carry trace and span IDs when a relevant driver lap is
known.

### Team-Radio Metadata

**Status: GREEN**

`TeamRadio.Captures` is the sole source for team-radio metadata logs. It records
that the source published a relative media capture associated with a
`RacingNumber`; it does not expose the audio contents, transcription, duration,
beginning of speech, lap association, sentiment, strategy, incident cause, or
sporting outcome. Bargeboard MUST NOT fetch the path, construct a media URL,
transcribe audio, or create a metric, span, span event, link, or exemplar from
this topic. A future transcript source requires a separate reviewed contract.

The evidence baseline is a 2024-through-2026 review of 309 indexed official
sessions. Of those, 276 TeamRadio streams were available and 33 returned 403;
the available streams contained 10,436 captures. Every stream began with a
non-empty array, accounting for 514 captures, followed by 9,922 captures in
sparse numeric-key feed maps. Final static snapshots exactly matched reduced
stream state. Observed indexes ranged from 0 through 173. Every observed entry
was complete on first appearance, and no same-index replay, correction,
deletion, or non-initial feed array occurred. These absences constrain fixtures
but do not justify content-based identity or an unbounded reducer.

Record identity is only immutable session identity `E`, literal topic
`TeamRadio`, and a collection index using exact grammar
`0 | non-zero-digit, { digit }` and fitting Int64. Content is not identity. In
particular, `Utc`, `RacingNumber`, `Path`, the filename token, and the filename's
embedded clock MUST NOT deduplicate a record. A different index is a distinct
source publication even when another record has equal metadata.

An authoritative snapshot `Captures` array atomically replaces current capture
payload and assigns zero-based implicit indexes. Every present index is marked
consumed without emitting a log, including an incomplete or invalid entry, so
snapshot history cannot replay later. An empty array is a valid empty
replacement even though none was observed. `null` is invalid. The optional
snapshot member `_kf` is transport keyframe metadata: when present it is the
Boolean `true` beside `Captures`, and it never becomes capture state or an OTLP
attribute. Successful requested-topic omission clears current and incomplete
payload but retains consumed identities and overflow state.

A feed `Captures` object is a sparse numeric-key patch. Keys are processed in
ascending numeric order inside that topic update; this does not reorder other
topics. An invalid key is diagnosed and ignored without suppressing valid
siblings. An empty object is a no-op. A feed array, `null`, or another container
shape is unsupported and quarantined without mutation. `_deleted` and null
entry deletion are unsupported because neither form is fixture-backed.

An unseen feed index may accumulate a sparse entry until `RacingNumber` and
`Path` are both present. The first patch with both fields attempts validation.
Non-string or lexically invalid values, or disagreement between their racing
numbers, consume the index without a log and produce a bounded payload-free
diagnostic; a later correction cannot make that identity eligible. Valid values
freeze all log content and event time, create the effect, and consume the index
before downstream delivery. `Utc` is optional for completion and follows the
fallback below. A later patch for a consumed index is diagnostic-only and MUST
NOT retract, replace, or re-emit the log. Sparse partial entries and corrections
are defensive reducer behavior; neither was observed in the evidence corpus.

`RacingNumber` must be a canonical positive decimal string fitting Int64. It
MUST resolve uniquely in the frozen Session Driver Registry before adding
driver attributes or trace correlation. An unresolved record still emits its
independently valid session log without driver attributes and is consumed; it
is never queued for later attribution. The corpus contained only one- and
two-digit values, and every feed capture resolved against coherent live
DriverList state, but those observations do not narrow the shared registry
grammar.

`Path` must be ASCII and match this complete relative-path grammar, where the
embedded racing number uses the same canonical value as `RacingNumber`:

```ebnf
path          = "TeamRadio/", token, "_", racing-number,
                "_", date-digits, "_", time-digits, ".mp3" ;
token         = upper, upper, upper
              | upper, upper, upper, upper, upper, upper, digit, digit ;
racing-number = non-zero-digit, { digit } ;
date-digits   = digit, digit, digit, digit, digit, digit, digit, digit ;
time-digits   = digit, digit, digit, digit, digit, digit ;
upper         = "A" | "B" | "C" | "D" | "E" | "F" | "G" | "H" | "I"
              | "J" | "K" | "L" | "M" | "N" | "O" | "P" | "Q" | "R"
              | "S" | "T" | "U" | "V" | "W" | "X" | "Y" | "Z" ;
digit         = "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
non-zero-digit = "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" ;
```

The structured and embedded racing numbers must also resolve to that same
registry driver for attribution. Across the corpus, 9,903 tokens used the
six-uppercase-letter/two-digit form and 533 used the three-uppercase-letter
form. The token is opaque and MUST NOT be compared with DriverList `Reference`,
acronym, name, or another identity field. The fixed-width date and clock are
opaque filename components and MUST NOT provide chronology. This allowlist
rejects absolute paths, traversal, alternate extensions, separators, URL
syntax, query strings, fragments, controls, and Unicode before the path reaches
OTLP. The observed paths were 35 through 41 ASCII bytes; accepting a shared
Int64 racing number gives a normative maximum of 58 bytes.

Record `Utc` uses exact UTC syntax `YYYY-MM-DDTHH:MM:SSZ` with an optional
fraction of one through seven decimal digits. A valid calendar value is the log
timestamp and uses `f1.time.quality=observed`. The literal `Z` is required;
numeric offsets are invalid. All fractional precisions from zero through seven
were observed. Missing, non-string, invalid-calendar, or invalid-syntax `Utc`
falls back to the feed-envelope timestamp with
`f1.time.quality=publication_time` and a bounded diagnostic. The filename clock
is never a fallback. Snapshots cannot use Collector observation time to create
historical logs.

Every coherent unseen feed record admitted within the identity bound creates
exactly one curated log:

```text
Body:              f1.team_radio.available
SeverityNumber:    INFO (9)
SeverityText:      INFO
Timestamp:         resolved record event time
ObservedTimestamp: Collector observation wall time
```

The log carries common session identity, Int64 `f1.team_radio.index`, safe
String `f1.team_radio.path`, and `f1.time.quality`. Successful registry
resolution also adds Int64 `f1.driver.number` and String
`f1.driver.acronym`. Only an already-projected driver-session root whose retained
final interval contains the log timestamp may provide log TraceID and SpanID;
an open provisional root, active lap, or stint is never selected because capture
availability does not time the speech. Owner absence does not suppress the log
and MUST NOT cause a span rewrite or TeamRadio span event. A TeamRadio record is
not participation evidence and cannot open a late-coverage root.

Session status does not gate this publication log. The corpus included 485 feed
captures before the first `Started`, 1,443 after the last `Finished`, 554
strictly after `Finalised`, and 584 at the same static archive prefix as `Ends`;
no capture had a later static prefix than `Ends`. Equal prefixes in separate
static topic streams do not establish cross-topic wire order. A live fixture did
establish a TeamRadio update after `Finalised`. The reducer therefore accepts a
coherent current-session feed record independently of `Inactive`, `Started`,
`Aborted`, `Finished`, `Finalised`, or `Ends` and derives no phase from those
states.

TeamRadio state retains at most 4,096 distinct indexes per session, including
consumed identities and incomplete entries. Before applying a snapshot, compute
the union of retained identities and all array positions; a union above 4,096
makes the authoritative snapshot incoherent. A 4,097th distinct feed index
latches overflow for that session: it and all later unseen indexes are neither
retained nor emitted, while retained incomplete entries may still complete and
consumed entries remain diagnostic-only. One rate-limited bounded diagnostic
reports overflow. Identity state is never evicted because eviction could turn a
replay into a new log. Disconnect clears current incomplete payload but retains
consumed identities; reconnect snapshots replace payload and seed their
indexes. Canonical session replacement resets all TeamRadio state.

Diagnostics MUST NOT contain a path, racing number, filename token, raw field
value, or payload fragment. Implementation requires compact sanitized fixtures
derived from public records for snapshots with and without `_kf`; empty and
omitted snapshots; single- and multi-key feed maps; partial completion and
same-index correction; new-index equal content; invalid, maximum, and
overflowing indexes; zero- and seven-digit UTC fractions plus every timestamp
fallback; both token lengths; path traversal and lexical near misses;
embedded/structured number conflicts; resolved and unresolved drivers;
pre-start, post-`Finalised`, equal-`Ends`, and post-`Ends` wire order; reconnect
and session replacement; exact log schema and correlation; and proof of no
audio fetch, transcript, metric, span event, link, or payload-bearing
diagnostic.

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

`SessionInfo` is the topic-specific exception to generic sparse feed patching:
every snapshot, ordinary feed, and `_kf` object is a complete descriptor
replacement under Live Timing Session Identity. Its missing members never
inherit state from an earlier object.

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
- Except for a topic-specific complete-object contract such as `SessionInfo`, an
  empty object is a no-op feed patch and empty snapshot state at that object.
- Snapshot arrays replace applicable state. Feed arrays are normalized as
  indexed updates only where a fixture-backed topic contract permits it; an
  unsupported feed-array form is quarantined without mutation.
- Numeric-key maps are sparse indexed patches in feed updates.
- Snapshot absence removes state, while feed-patch absence retains state.

The functional core MUST return reduced state, immutable signal effects, bounded
operational commands, and bounded diagnostics without calling a clock, logger,
network, or Collector consumer. Commands describe work such as timer scheduling
or cancellation and HTTP dispatch or cancellation; the imperative shell alone
executes them. Returned effects and commands MUST NOT alias mutable state that a
later reduction can clear. The shell supplies Collector observation time only
where the time model permits it.

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
Sparse feed patches MAY still update that topic's retained recovery state but
MUST NOT declare it synchronized or project it. The complete-object SessionInfo
exception instead follows its global unbound-update rule. Invalid SignalR
framing, decompression, size limits, UTF-8, JSON, or feed-envelope timestamp
remain permanent source-protocol failures.

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
`f1.season.year`, `f1.meeting.key`, `f1.session.type`, `f1.session.name`,
`f1.driver.number`, `f1.driver.acronym`, and `f1.data.source`. Driver-session
roots additionally carry meeting and circuit display metadata, driver name, and
constructor metadata when known. Child spans add their local phase, stint, lap,
and sector identity. Logs carry the minimum session and driver identity needed
for independent search and correlation. No signal relies on attribute
inheritance, which OTLP does not provide.

### Deterministic IDs

Trace and span identity uses SHA-256 over unambiguous length-prefixed parts:

```text
LP(value)       = uint32 big-endian UTF-8 byte length || UTF-8 bytes
EID             = LP("season") || LP(Y)
                  || LP("meeting") || LP(M)
                  || LP("session.type") || LP(C)
                  || LP("session.name") || LP(S)
ID(marker, ...) = SHA-256(LP("bargeboard.otlp.id/v1")
                  || LP(marker) || EID || LP(part_1) || LP(value_1) || ...)
```

Every value, including namespace, marker, and part-name strings, uses `LP`. `Y`
is canonical season, `M` is positive meeting key, `C` is canonical session type,
`S` is canonical session name, and `D` is positive driver number. Integers use
unsigned base-10 without leading zeros. Source `K_route` is absent. The first 16
bytes form the TraceID and the first 8 bytes form the SpanID.

```text
trace = ID("trace", "driver", D)[:16]

root = ID("span", "driver", D, "driver.session", "root")[:8]

phase = ID("span", "driver", D, "qualifying.phase", P)[:8]

stint = ID("span", "driver", D,
           "phase", P_OR_NONE, "stint", N)[:8]

lap = ID("span", "driver", D,
         "phase", P_OR_NONE, "lap", L)[:8]

sector = ID("span", "driver", D,
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

Source `K_route`, display names, constructor, timestamps, and mutable result
state MUST NOT enter identity. An unresolved session, driver, phase, or lap
identity blocks export of the affected span. IDs MUST remain stable across
reconnect, source-key correction, process restart, early child export, and the
handoff from Live Timing to OpenF1 result observations.

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
- OpenF1 requests MUST NOT receive the F1 TV token or another credential in the
  first result-observation slice, follow redirects, or expose endpoint/query,
  header, or response-body content through diagnostics.
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
| Per-domain source authority | GREEN | Live Timing owns live chronology; OpenF1 owns explicit observed post-session domains. |
| Logical Live Timing session identity | GREEN | A same-session source-key correction changes routing without replacing the generation or emitted identity. |
| Source `SessionInfo.Key` as OTLP identity | RED | The corrected-in-place value remains internal routing metadata and cannot split telemetry identity. |
| OpenF1 Race/Sprint result observations | GREEN | Current outcomes can settle unexported root status but never claim legal finality or own chronology. |
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
