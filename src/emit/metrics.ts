/**
 * MetricBank: a hand-rolled metric pipeline with historical timestamps.
 *
 * The OTel SDK stamps metric datapoints at collection time, which squeezes a
 * 2-hour race into the ~2 minutes the replay takes. Spans and logs accept
 * explicit timestamps; metrics don't — so we bypass the SDK entirely.
 * Handlers' metric effects accumulate here, the engine calls `flush(raceT)`
 * every FLUSH_EVERY_S of race time, and each flush appends datapoints
 * stamped `sessionStart + raceT`. Batches ship straight through the
 * OTLPMetricExporter, which faithfully exports whatever timestamps we give
 * it. Result: metrics land on the same historical time axis as the traces.
 *
 * Aggregations:
 *  - gauge      → last value in the flush window (dirty-only: series go
 *                 quiet when their source stops, e.g. a retired car)
 *  - sum / udc  → cumulative running total from lights-out
 *  - histogram  → delta explicit-bucket counts with custom, F1-shaped
 *                 boundaries (universal across circuits — every dry race lap
 *                 on the calendar fits 63–105s). Delta is what gives the
 *                 heatmap its per-window texture.
 *
 * Two histograms also emit a cumulative twin, marked `temporality` in the
 * roster. Cumulative is OTLP's default and the temporality most services
 * actually send, but no capture we had contained one, so the viewer's
 * cumulative merge — which recovers activity as last-minus-first per bucket —
 * had only synthetic fixtures behind it. The twins are the same observations
 * accumulated rather than reset, one explicit-bucket and one exponential, so
 * both merge paths get real race data.
 */

import { ValueType, type Attributes } from "@opentelemetry/api";
import {
  AggregationTemporality,
  DataPointType,
  InstrumentType,
  type DataPoint,
  type ExponentialHistogram,
  type Histogram,
  type MetricData,
  type ResourceMetrics,
  type ScopeMetrics,
} from "@opentelemetry/sdk-metrics";
import type { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-grpc";
import type { Resource } from "@opentelemetry/resources";
import type { HrTime } from "@opentelemetry/api";

import { SCOPE_NAME, SCOPE_VERSION } from "./constants.js";
import { raceTimeToUnixNanos } from "../util.js";
import { log } from "../util.js";

/** Race seconds between flushes → one datapoint per dirty series per 5s. */
export const FLUSH_EVERY_S = 5;

// --- instrument roster --------------------------------------------------------

type BankKind = "gauge" | "counter" | "up_down_counter" | "histogram" | "exp_histogram";

interface InstrumentSpec {
  kind: BankKind;
  unit: string;
  description: string;
  boundaries?: number[];    // histograms only
  /** Histograms only; delta when unset. */
  temporality?: "delta" | "cumulative";
  /** Record every observation of this instrument into that one as well. */
  twin?: string;
}

/** Steps from `from` to `to` inclusive. */
function range(from: number, to: number, step: number): number[] {
  const out: number[] = [];
  for (let v = from; v <= to + 1e-9; v += step) out.push(Number(v.toFixed(3)));
  return out;
}

// Universal bucket layouts: dry race laps across all circuits fit 63–105s,
// sectors 15–45s, pit lanes 15–35s, top speeds 280–360 km/h. Tails catch
// SC/VSC/wet laps and outliers.
const LAP_TIME_BOUNDS = [...range(60, 110, 2.5), 120, 135, 150, 180, 240];
const SECTOR_TIME_BOUNDS = [...range(15, 45, 1.5), 60, 90];
const PIT_DURATION_BOUNDS = [...range(15, 35, 1), 45, 60, 90];
const TOP_SPEED_BOUNDS = [150, 200, 250, ...range(280, 360, 5)];

const ROSTER: Record<string, InstrumentSpec> = {
  // telemetry gauges
  "f1.car.speed":       { kind: "gauge", unit: "km/h", description: "Car speed" },
  "f1.car.rpm":         { kind: "gauge", unit: "{rpm}", description: "Engine RPM" },
  "f1.car.throttle":    { kind: "gauge", unit: "%", description: "Throttle position" },
  "f1.car.brake":       { kind: "gauge", unit: "%", description: "Brake pressure" },
  "f1.car.gear":        { kind: "gauge", unit: "{gear}", description: "Selected gear" },
  "f1.car.drs":         { kind: "gauge", unit: "{bool}", description: "DRS open (1) or closed (0)" },
  "f1.car.position_x":  { kind: "gauge", unit: "m", description: "Track-local X position" },
  "f1.car.position_y":  { kind: "gauge", unit: "m", description: "Track-local Y position" },
  "f1.car.position_z":  { kind: "gauge", unit: "m", description: "Track-local Z position (elevation)" },
  // race-state gauges
  "f1.driver.standings_position": { kind: "gauge", unit: "{position}", description: "Current race position" },
  "f1.driver.gap_to_leader":      { kind: "gauge", unit: "s", description: "Gap to the race leader" },
  "f1.driver.interval":           { kind: "gauge", unit: "s", description: "Gap to the car ahead" },
  "f1.driver.trap_speed":         { kind: "gauge", unit: "km/h", description: "Speed-trap reading (split by trap attribute)" },
  // weather gauges (session-scoped, no driver attributes)
  "f1.session.air_temp":   { kind: "gauge", unit: "Cel", description: "Air temperature" },
  "f1.session.track_temp": { kind: "gauge", unit: "Cel", description: "Track temperature" },
  "f1.session.humidity":   { kind: "gauge", unit: "%", description: "Relative humidity" },
  "f1.session.rainfall":   { kind: "gauge", unit: "{bool}", description: "Rain falling (1) or not (0)" },
  "f1.session.wind_speed": { kind: "gauge", unit: "m/s", description: "Wind speed" },
  // counters
  "f1.driver.laps_completed": { kind: "counter", unit: "{lap}", description: "Laps completed" },
  "f1.driver.pit_stops":      { kind: "counter", unit: "{stop}", description: "Pit stops completed" },
  "f1.driver.blue_flags":     { kind: "counter", unit: "{flag}", description: "Blue flags shown to this driver" },
  "f1.driver.penalties":      { kind: "counter", unit: "{penalty}", description: "Penalties received" },
  "f1.driver.investigations": { kind: "counter", unit: "{incident}", description: "Incidents opened against this driver" },
  // up-down counters
  "f1.driver.championship_points": { kind: "up_down_counter", unit: "{point}", description: "Championship points" },
  "f1.session.cars_on_track":      { kind: "up_down_counter", unit: "{car}", description: "Number of cars still circulating" },
  // histograms
  "f1.driver.lap_time":     { kind: "histogram", unit: "s", description: "Lap time", boundaries: LAP_TIME_BOUNDS, twin: "f1.driver.lap_time_cumulative" },
  // The cumulative pair. Same observations, accumulated from lights-out rather
  // than zeroed each window, which is what a service on OTLP defaults sends.
  // Two of them because the explicit-bucket and exponential merges are separate
  // code paths; more instruments would multiply export weight without reaching
  // any further code.
  "f1.driver.lap_time_cumulative": { kind: "histogram", unit: "s", description: "Lap time (cumulative)", boundaries: LAP_TIME_BOUNDS, temporality: "cumulative" },
  "f1.driver.sector_time":  { kind: "histogram", unit: "s", description: "Sector time", boundaries: SECTOR_TIME_BOUNDS },
  "f1.driver.pit_duration": { kind: "histogram", unit: "s", description: "Pit lane duration", boundaries: PIT_DURATION_BOUNDS },
  "f1.driver.top_speed":    { kind: "histogram", unit: "km/h", description: "Top speed per lap", boundaries: TOP_SPEED_BOUNDS },
  // Exponential: intervals span 0.05s (DRS range) to minutes — a ratio scale
  // where fixed buckets can't give sub-tenth resolution at the tight end
  // without wasting hundreds of buckets at the loose end.
  "f1.driver.interval_distribution": { kind: "exp_histogram", unit: "s", description: "Distribution of gaps to the car ahead", twin: "f1.driver.interval_distribution_cumulative" },
  "f1.driver.interval_distribution_cumulative": { kind: "exp_histogram", unit: "s", description: "Distribution of gaps to the car ahead (cumulative)", temporality: "cumulative" },
};

// --- per-series accumulators ---------------------------------------------------

interface GaugeSeries {
  attrs: Attributes;
  value: number;
  dirty: boolean;
}
interface SumSeries {
  attrs: Attributes;
  total: number;
  dirty: boolean;
}
/** Histograms ship DELTA: each datapoint covers only the observations since
 *  that series' previous flush, so a heatmap column reads "lap times set in
 *  this window" — the race's actual texture. Cumulative histograms would put
 *  every observation since lights-out in every column, which is why the
 *  heatmap looked identically wide the whole race. */
interface HistSeries {
  attrs: Attributes;
  counts: number[];        // boundaries.length + 1
  sum: number;
  count: number;
  min: number;
  max: number;
  dirty: boolean;
  windowStartT: number;    // race_t this delta window opened
}

/** Base-2 exponential histogram accumulator (positive + zero buckets only —
 *  gaps are non-negative). Fixed scale + fixed bucket window, because the
 *  viewer's delta merge sums bucket vectors positionally: every window has
 *  to share one scale/offset for that sum to mean anything. Values outside
 *  the window clamp to the edge buckets; sum/count/min/max stay exact. */
interface ExpHistSeries {
  attrs: Attributes;
  zeroCount: number;
  counts: number[];               // EXP_BUCKET_COUNT entries starting at EXP_OFFSET
  sum: number;
  count: number;
  min: number;
  max: number;
  dirty: boolean;
  windowStartT: number;
}

/** Scale 3 = 8 buckets per octave (~9% wide — fine enough to separate a
 *  0.5s DRS gap from a 1.0s one). The window spans bucket indices
 *  -35..60, i.e. ~0.047s to ~193s, which covers every real interval
 *  OpenF1 reports numerically (bigger gaps come through as "+1 LAP" and
 *  are dropped upstream). */
const EXP_SCALE = 3;
const EXP_OFFSET = -35;
const EXP_BUCKET_COUNT = 96;

/** Stable key for an attribute set. */
function attrKey(attrs: Attributes): string {
  const keys = Object.keys(attrs).sort();
  return keys.map((k) => `${k}=${String(attrs[k])}`).join("|");
}

// --- the bank -------------------------------------------------------------------

export class MetricBank {
  private readonly gaugeSeries = new Map<string, Map<string, GaugeSeries>>();
  private readonly sumSeries = new Map<string, Map<string, SumSeries>>();
  private readonly histSeries = new Map<string, Map<string, HistSeries>>();
  private readonly expHistSeries = new Map<string, Map<string, ExpHistSeries>>();

  /** Datapoints accumulated since the last export, per instrument. */
  private readonly pendingGauge = new Map<string, DataPoint<number>[]>();
  private readonly pendingSum = new Map<string, DataPoint<number>[]>();
  private readonly pendingHist = new Map<string, DataPoint<Histogram>[]>();
  private readonly pendingExpHist = new Map<string, DataPoint<ExponentialHistogram>[]>();

  private startTime: HrTime = [0, 0];
  /** Where the currently-open delta window began. A series created mid-race
   *  starts observing here, not at lights-out -- dating its first datapoint
   *  back to the start would claim a window minutes wide for a few seconds of
   *  observations, and anything dividing by that window reads far too low. */
  private windowOpenT = 0;
  private exportsInFlight = 0;

  constructor(
    private readonly resource: Resource,
    private readonly exporter: OTLPMetricExporter | null,
    private readonly sessionStart: Date,
  ) {}

  /** Cumulative series (sums) start counting here; it also opens the first
   *  delta window for histograms. Call once at lights-out (or startAt). */
  setStartTime(raceT: number): void {
    this.startTime = hrTime(this.sessionStart, raceT);
    this.windowOpenT = raceT;
  }

  // --- record APIs (called by the interpreters) ---

  setGauge(instrument: string, value: number, attrs: Attributes): void {
    if (ROSTER[instrument]?.kind !== "gauge") return;
    const series = mapFor(this.gaugeSeries, instrument);
    const key = attrKey(attrs);
    const s = series.get(key);
    if (s) {
      s.value = value;
      s.dirty = true;
    } else {
      series.set(key, { attrs, value, dirty: true });
    }
  }

  addSum(instrument: string, delta: number, attrs: Attributes): void {
    const kind = ROSTER[instrument]?.kind;
    if (kind !== "counter" && kind !== "up_down_counter") return;
    if (kind === "counter" && delta < 0) return;   // monotonic
    const series = mapFor(this.sumSeries, instrument);
    const key = attrKey(attrs);
    const s = series.get(key);
    if (s) {
      s.total += delta;
      s.dirty = true;
    } else {
      series.set(key, { attrs, total: delta, dirty: true });
    }
  }

  recordHistogram(instrument: string, value: number, attrs: Attributes): void {
    const spec = ROSTER[instrument];
    // The twin sees exactly what its source sees, so the two differ only in
    // how they are aggregated. Recorded here rather than in the handlers so
    // no caller has to know a twin exists.
    if (spec?.twin) this.recordHistogram(spec.twin, value, attrs);
    if (spec?.kind === "exp_histogram") return this.recordExpHistogram(instrument, value, attrs);
    if (spec?.kind !== "histogram") return;
    const boundaries = spec.boundaries!;
    const series = mapFor(this.histSeries, instrument);
    const key = attrKey(attrs);
    let s = series.get(key);
    if (!s) {
      s = {
        attrs,
        counts: new Array<number>(boundaries.length + 1).fill(0),
        sum: 0, count: 0,
        min: Infinity, max: -Infinity,
        dirty: false,
        windowStartT: this.windowOpenT,
      };
      series.set(key, s);
    }
    s.counts[bucketIndex(boundaries, value)]!++;
    s.sum += value;
    s.count += 1;
    if (value < s.min) s.min = value;
    if (value > s.max) s.max = value;
    s.dirty = true;
  }

  private recordExpHistogram(instrument: string, value: number, attrs: Attributes): void {
    if (value < 0) return;   // negative buckets unsupported (and gaps can't be)
    const series = mapFor(this.expHistSeries, instrument);
    const key = attrKey(attrs);
    let s = series.get(key);
    if (!s) {
      s = {
        attrs,
        zeroCount: 0,
        counts: new Array<number>(EXP_BUCKET_COUNT).fill(0),
        sum: 0, count: 0,
        min: Infinity, max: -Infinity,
        dirty: false,
        windowStartT: this.windowOpenT,
      };
      series.set(key, s);
    }
    if (value === 0) {
      s.zeroCount++;
    } else {
      // Clamp into the fixed window: the totals below stay exact either way,
      // and a clamped edge bucket beats a scale that drifts per series.
      const slot = expBucketIndex(value, EXP_SCALE) - EXP_OFFSET;
      s.counts[Math.min(EXP_BUCKET_COUNT - 1, Math.max(0, slot))]!++;
    }
    s.sum += value;
    s.count += 1;
    if (value < s.min) s.min = value;
    if (value > s.max) s.max = value;
    s.dirty = true;
  }

  // --- flush + export ---

  /** Append one datapoint (stamped at sessionStart + raceT) for every series
   *  touched since the last flush. */
  flush(raceT: number): void {
    const endTime = hrTime(this.sessionStart, raceT);

    for (const [instrument, series] of this.gaugeSeries) {
      for (const s of series.values()) {
        if (!s.dirty) continue;
        s.dirty = false;
        pendFor(this.pendingGauge, instrument).push({
          startTime: endTime,      // gauges: point-in-time
          endTime,
          attributes: s.attrs,
          value: s.value,
        });
      }
    }
    for (const [instrument, series] of this.sumSeries) {
      for (const s of series.values()) {
        if (!s.dirty) continue;
        s.dirty = false;
        pendFor(this.pendingSum, instrument).push({
          startTime: this.startTime,
          endTime,
          attributes: s.attrs,
          value: s.total,
        });
      }
    }
    // Delta histograms emit the window's observations and then zero, so the
    // next datapoint covers only what follows. Cumulative ones keep counting
    // and date every datapoint to the stream's start, which is what makes the
    // running total meaningful.
    for (const [instrument, series] of this.histSeries) {
      const spec = ROSTER[instrument]!;
      const cumulative = spec.temporality === "cumulative";
      for (const s of series.values()) {
        if (!s.dirty) continue;
        pendFor(this.pendingHist, instrument).push({
          startTime: cumulative ? this.startTime : hrTime(this.sessionStart, s.windowStartT),
          endTime,
          attributes: s.attrs,
          value: {
            buckets: { boundaries: spec.boundaries!, counts: [...s.counts] },
            sum: s.sum,
            count: s.count,
            min: s.min,
            max: s.max,
          },
        });
        s.dirty = false;
        if (cumulative) continue;
        s.counts.fill(0);
        s.sum = 0;
        s.count = 0;
        s.min = Infinity;
        s.max = -Infinity;
        s.windowStartT = raceT;
      }
    }
    for (const [instrument, series] of this.expHistSeries) {
      const cumulative = ROSTER[instrument]!.temporality === "cumulative";
      for (const s of series.values()) {
        if (!s.dirty) continue;
        pendFor(this.pendingExpHist, instrument).push({
          startTime: cumulative ? this.startTime : hrTime(this.sessionStart, s.windowStartT),
          endTime,
          attributes: s.attrs,
          value: denseExpBuckets(s),
        });
        s.dirty = false;
        if (cumulative) continue;
        s.counts.fill(0);
        s.zeroCount = 0;
        s.sum = 0;
        s.count = 0;
        s.min = Infinity;
        s.max = -Infinity;
        s.windowStartT = raceT;
      }
    }
    // Every delta window that was open has now been emitted and closed, so the
    // next one starts here -- including for series that do not exist yet.
    this.windowOpenT = raceT;
  }

  /** True if any flushed datapoints are waiting to ship. */
  private hasPending(): boolean {
    for (const dps of this.pendingGauge.values()) if (dps.length > 0) return true;
    for (const dps of this.pendingSum.values()) if (dps.length > 0) return true;
    for (const dps of this.pendingHist.values()) if (dps.length > 0) return true;
    for (const dps of this.pendingExpHist.values()) if (dps.length > 0) return true;
    return false;
  }

  /** Ship pending datapoints to the OTLP exporter (no-op in dry-run).
   *  Exports are serialized (while one is in flight, datapoints keep
   *  accumulating and merge into the next batch) AND size-budgeted so a
   *  merged batch never exceeds gRPC's 4MB message cap — the remainder
   *  stays pending for the next call. */
  export(): void {
    if (this.exporter && this.exportsInFlight > 0) return;
    const metrics: MetricData[] = [];
    // Budget in "points": histogram points carry ~26 bucket counts, so they
    // weigh more. 12k gauge-equivalents ≈ ~1.5MB on the wire — safe margin.
    let budget = 12_000;
    const HIST_WEIGHT = 20;

    for (const [instrument, dataPoints] of this.pendingGauge) {
      if (dataPoints.length === 0 || budget <= 0) continue;
      const take = dataPoints.splice(0, budget);
      budget -= take.length;
      metrics.push({
        descriptor: descriptorFor(instrument, InstrumentType.GAUGE),
        aggregationTemporality: AggregationTemporality.CUMULATIVE,
        dataPointType: DataPointType.GAUGE,
        dataPoints: take,
      });
    }
    for (const [instrument, dataPoints] of this.pendingSum) {
      if (dataPoints.length === 0 || budget <= 0) continue;
      const take = dataPoints.splice(0, budget);
      budget -= take.length;
      const monotonic = ROSTER[instrument]!.kind === "counter";
      metrics.push({
        descriptor: descriptorFor(
          instrument,
          monotonic ? InstrumentType.COUNTER : InstrumentType.UP_DOWN_COUNTER,
        ),
        aggregationTemporality: AggregationTemporality.CUMULATIVE,
        dataPointType: DataPointType.SUM,
        isMonotonic: monotonic,
        dataPoints: take,
      });
    }
    for (const [instrument, dataPoints] of this.pendingHist) {
      if (dataPoints.length === 0 || budget <= 0) continue;
      const take = dataPoints.splice(0, Math.max(1, Math.floor(budget / HIST_WEIGHT)));
      budget -= take.length * HIST_WEIGHT;
      metrics.push({
        descriptor: descriptorFor(instrument, InstrumentType.HISTOGRAM),
        aggregationTemporality: temporalityOf(instrument),
        dataPointType: DataPointType.HISTOGRAM,
        dataPoints: take,
      });
    }
    const EXP_WEIGHT = 24;   // EXP_BUCKET_COUNT counts per point
    for (const [instrument, dataPoints] of this.pendingExpHist) {
      if (dataPoints.length === 0 || budget <= 0) continue;
      const take = dataPoints.splice(0, Math.max(1, Math.floor(budget / EXP_WEIGHT)));
      budget -= take.length * EXP_WEIGHT;
      metrics.push({
        descriptor: descriptorFor(instrument, InstrumentType.HISTOGRAM),
        aggregationTemporality: temporalityOf(instrument),
        dataPointType: DataPointType.EXPONENTIAL_HISTOGRAM,
        dataPoints: take,
      });
    }

    if (metrics.length === 0 || !this.exporter) return;

    const scopeMetrics: ScopeMetrics = {
      scope: { name: SCOPE_NAME, version: SCOPE_VERSION },
      metrics,
    };
    const rm: ResourceMetrics = { resource: this.resource, scopeMetrics: [scopeMetrics] };
    this.exportsInFlight++;
    this.exporter.export(rm, (result) => {
      this.exportsInFlight--;
      if (result.code !== 0) {
        log.warn(`Metric export failed: ${result.error?.message ?? "unknown"}`);
      }
    });
  }

  /** Ship any remaining batches and wait for in-flight exports to settle
   *  (called before exporter shutdown). */
  async drain(): Promise<void> {
    if (!this.exporter) return;
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline) {
      if (this.exportsInFlight === 0) {
        if (!this.hasPending()) return;
        this.export();
      }
      await new Promise((r) => setTimeout(r, 20));
    }
    log.warn("Metric drain timed out with batches still pending.");
  }
}

// --- helpers ----------------------------------------------------------------------

/** A histogram's temporality: delta unless the roster says otherwise. */
function temporalityOf(instrument: string): AggregationTemporality {
  return ROSTER[instrument]?.temporality === "cumulative"
    ? AggregationTemporality.CUMULATIVE
    : AggregationTemporality.DELTA;
}

function mapFor<T>(outer: Map<string, Map<string, T>>, instrument: string): Map<string, T> {
  let m = outer.get(instrument);
  if (!m) {
    m = new Map();
    outer.set(instrument, m);
  }
  return m;
}

function pendFor<T>(outer: Map<string, T[]>, instrument: string): T[] {
  let a = outer.get(instrument);
  if (!a) {
    a = [];
    outer.set(instrument, a);
  }
  return a;
}

function descriptorFor(instrument: string, type: InstrumentType) {
  const spec = ROSTER[instrument]!;
  return {
    name: instrument,
    description: spec.description,
    unit: spec.unit,
    type,
    valueType: ValueType.DOUBLE,
    advice: {},
  };
}

/** Index of the bucket `value` falls in: counts[i] covers
 *  (boundaries[i-1], boundaries[i]]; the last bucket is overflow. */
function bucketIndex(boundaries: number[], value: number): number {
  for (let i = 0; i < boundaries.length; i++) {
    if (value <= boundaries[i]!) return i;
  }
  return boundaries.length;
}

/** OTel exp-histogram bucket index at `scale`: bucket i covers
 *  (base^i, base^(i+1)] with base = 2^(2^-scale). */
function expBucketIndex(value: number, scale: number): number {
  return Math.ceil(Math.log2(value) * 2 ** scale) - 1;
}

/** Snapshot the fixed window in the offset+counts shape OTLP wants. Trailing
 *  empty buckets are trimmed (a shorter vector is still positionally aligned
 *  with a longer one at the same offset, so delta merges stay correct). */
function denseExpBuckets(s: ExpHistSeries): ExponentialHistogram {
  let end = s.counts.length;
  while (end > 0 && s.counts[end - 1] === 0) end--;
  return {
    scale: EXP_SCALE,
    zeroCount: s.zeroCount,
    positive: { offset: EXP_OFFSET, bucketCounts: s.counts.slice(0, end) },
    negative: { offset: 0, bucketCounts: [] },
    sum: s.sum,
    count: s.count,
    min: s.min,
    max: s.max,
  };
}

function hrTime(sessionStart: Date, raceT: number): HrTime {
  const ns = raceTimeToUnixNanos(sessionStart, raceT);
  return [Number(ns / 1_000_000_000n), Number(ns % 1_000_000_000n)];
}
