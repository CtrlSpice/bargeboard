/**
 * Effect discriminated union and the OTel-side interpreter.
 *
 * Handlers return a list of these. The interpreter is the only place that
 * touches the mutable OTel SDK (open spans, gauges, counters, loggers). This
 * keeps handlers pure and trivially testable.
 *
 * Span lifecycle effects identify spans by a logical role: "race", "lap",
 * "sector", "pit" — the interpreter maintains a registry per driver mapping
 * role -> live Span. Span-event and log effects pin to one of these roles
 * (or "active" = sector then lap then race, whichever is open).
 */

import {
  trace,
  context,
  SpanStatusCode,
  type Attributes,
  type Span,
  type Tracer,
  type Counter,
  type UpDownCounter,
  type Histogram,
  type Gauge,
  type Meter,
} from "@opentelemetry/api";
import type { Logger, LogRecord } from "@opentelemetry/api-logs";
import { SeverityNumber } from "@opentelemetry/api-logs";

import type { DriverBundle, SessionBundle } from "../providers.js";
import { SCOPE_NAME, SCOPE_VERSION } from "./constants.js";
import { raceTimeToUnixNanos } from "../util.js";
import type { SessionInfo } from "../models.js";

export type SpanRole = "race" | "lap" | "sector" | "pit";
export type AnchorRole = SpanRole | "active";

// --- Effect type ------------------------------------------------------------

export type Effect =
  | StartSpan
  | EndSpan
  | AddSpanEvent
  | SetGauge
  | AddCounter
  | AddUpDownCounter
  | RecordHistogram
  | EmitLog
  | SetSpanStatus
  // session-scoped:
  | StartSessionSpan
  | EndSessionSpan
  | AddSessionUpDownCounter;

export interface StartSpan {
  kind: "start_span";
  role: SpanRole;
  parentRole: SpanRole | "session_root";   // what to nest under
  name: string;
  raceT: number;
  attributes?: Attributes;
}
export interface EndSpan {
  kind: "end_span";
  role: SpanRole;
  raceT: number;
  status?: { code: SpanStatusCode; message?: string };
  endEvent?: { name: string; attributes?: Attributes };
}
export interface AddSpanEvent {
  kind: "add_span_event";
  anchor: AnchorRole;
  raceT: number;
  name: string;
  attributes?: Attributes;
}
export interface SetSpanStatus {
  kind: "set_span_status";
  role: SpanRole;
  code: SpanStatusCode;
  message?: string;
}

export interface SetGauge {
  kind: "set_gauge";
  instrument: string;
  value: number;
  attributes?: Attributes;
}
export interface AddCounter {
  kind: "add_counter";
  instrument: string;
  delta: number;
  attributes?: Attributes;
}
export interface AddUpDownCounter {
  kind: "add_up_down_counter";
  instrument: string;
  delta: number;
  attributes?: Attributes;
}
export interface RecordHistogram {
  kind: "record_histogram";
  instrument: string;
  value: number;
  attributes?: Attributes;
}
export interface EmitLog {
  kind: "emit_log";
  anchor: AnchorRole;
  raceT: number;
  severity: SeverityNumber;
  body: string;
  attributes?: Attributes;
}

export interface StartSessionSpan {
  kind: "start_session_span";
  name: string;
  raceT: number;
}
export interface EndSessionSpan {
  kind: "end_session_span";
  raceT: number;
}
export interface AddSessionUpDownCounter {
  kind: "add_session_up_down_counter";
  instrument: string;
  delta: number;
}

// --- per-driver interpreter -------------------------------------------------

/**
 * Owns the live OTel handles for one driver: tracer, meter instruments,
 * logger, and the current Span registry. Apply effects with `apply(effect)`.
 */
export class DriverInterpreter {
  private readonly tracer: Tracer;
  private readonly logger: Logger;
  private readonly meter: Meter;

  private readonly gauges = new Map<string, Gauge>();
  private readonly counters = new Map<string, Counter>();
  private readonly udcs = new Map<string, UpDownCounter>();
  private readonly histograms = new Map<string, Histogram>();
  private readonly spans = new Map<SpanRole, Span>();

  constructor(
    bundle: DriverBundle,
    private readonly sessionStart: Date,
    private readonly sessionInterp: SessionInterpreter,
  ) {
    this.tracer = bundle.tracerProvider.getTracer(SCOPE_NAME, SCOPE_VERSION);
    this.logger = bundle.loggerProvider.getLogger(SCOPE_NAME, SCOPE_VERSION);
    this.meter = bundle.meterProvider.getMeter(SCOPE_NAME, SCOPE_VERSION);
    this.declareInstruments();
  }

  /** Pre-create every per-driver instrument so set/add/record never lazily allocates. */
  private declareInstruments(): void {
    const m = this.meter;
    const gauge = (name: string, unit: string, description: string): void => {
      this.gauges.set(name, m.createGauge(name, { unit, description }));
    };
    gauge("f1.car.speed", "km/h", "Car speed");
    gauge("f1.car.rpm", "{rpm}", "Engine RPM");
    gauge("f1.car.throttle", "%", "Throttle position");
    gauge("f1.car.brake", "%", "Brake pressure");
    gauge("f1.car.gear", "{gear}", "Selected gear");
    gauge("f1.car.drs", "{bool}", "DRS open (1) or closed (0)");
    gauge("f1.car.position_x", "m", "Track-local X position");
    gauge("f1.car.position_y", "m", "Track-local Y position");
    gauge("f1.car.position_z", "m", "Track-local Z position (elevation)");
    gauge("f1.driver.standings_position", "{position}", "Current race position");

    const counter = (name: string, unit: string, description: string): void => {
      this.counters.set(name, m.createCounter(name, { unit, description }));
    };
    counter("f1.driver.laps_completed", "{lap}", "Laps completed");
    counter("f1.driver.pit_stops", "{stop}", "Pit stops completed");
    counter("f1.driver.blue_flags", "{flag}", "Blue flags shown to this driver");
    counter("f1.driver.penalties", "{penalty}", "Penalties received");
    counter("f1.driver.investigations", "{incident}", "Incidents opened against this driver");
    counter("f1.driver.defensive_moves", "{move}", "Defensive moves under braking");

    this.udcs.set(
      "f1.driver.championship_points",
      m.createUpDownCounter("f1.driver.championship_points", {
        unit: "{point}",
        description: "Championship points",
      }),
    );

    const hist = (name: string, unit: string, description: string): void => {
      this.histograms.set(name, m.createHistogram(name, { unit, description }));
    };
    hist("f1.driver.lap_time", "s", "Lap time");
    hist("f1.driver.sector_time", "s", "Sector time");
    hist("f1.driver.pit_duration", "s", "Pit lane duration");
    hist("f1.driver.top_speed", "km/h", "Top speed per lap");
    hist("f1.driver.gap_to_leader", "s", "Gap to race leader");
  }

  apply(effect: Effect): void {
    switch (effect.kind) {
      case "start_span": return this.startSpan(effect);
      case "end_span": return this.endSpan(effect);
      case "set_span_status": return this.setSpanStatus(effect);
      case "add_span_event": return this.addSpanEvent(effect);
      case "set_gauge": return this.setGauge(effect);
      case "add_counter": return this.addCounter(effect);
      case "add_up_down_counter": return this.addUpDownCounter(effect);
      case "record_histogram": return this.recordHistogram(effect);
      case "emit_log": return this.emitLog(effect);
      case "start_session_span":
      case "end_session_span":
      case "add_session_up_down_counter":
        return this.sessionInterp.apply(effect);
    }
  }

  // --- span lifecycle ---

  private startSpan(e: StartSpan): void {
    const parent =
      e.parentRole === "session_root"
        ? this.sessionInterp.rootSpan()
        : this.spans.get(e.parentRole);
    if (e.parentRole !== "session_root" && !parent) return;  // can't open child without parent

    const startTime = raceTimeToUnixNanos(this.sessionStart, e.raceT);
    const ctx = parent ? trace.setSpan(context.active(), parent) : context.active();
    const span = this.tracer.startSpan(
      e.name,
      { startTime: nanosTimeInput(startTime), attributes: e.attributes },
      ctx,
    );
    this.spans.set(e.role, span);
  }

  private endSpan(e: EndSpan): void {
    const span = this.spans.get(e.role);
    if (!span) return;
    const endTime = raceTimeToUnixNanos(this.sessionStart, e.raceT);
    if (e.endEvent) {
      span.addEvent(e.endEvent.name, e.endEvent.attributes, nanosTimeInput(endTime));
    }
    if (e.status) span.setStatus({ code: e.status.code, message: e.status.message });
    span.end(nanosTimeInput(endTime));
    this.spans.delete(e.role);
  }

  private setSpanStatus(e: SetSpanStatus): void {
    const span = this.spans.get(e.role);
    if (!span) return;
    span.setStatus({ code: e.code, message: e.message });
  }

  private activeSpan(anchor: AnchorRole): Span | undefined {
    if (anchor === "active") {
      return this.spans.get("sector") ?? this.spans.get("lap") ?? this.spans.get("race");
    }
    return this.spans.get(anchor);
  }

  private addSpanEvent(e: AddSpanEvent): void {
    const span = this.activeSpan(e.anchor);
    if (!span) return;
    const t = raceTimeToUnixNanos(this.sessionStart, e.raceT);
    span.addEvent(e.name, e.attributes, nanosTimeInput(t));
  }

  // --- metrics ---

  private setGauge(e: SetGauge): void {
    this.gauges.get(e.instrument)?.record(e.value, e.attributes);
  }
  private addCounter(e: AddCounter): void {
    this.counters.get(e.instrument)?.add(e.delta, e.attributes);
  }
  private addUpDownCounter(e: AddUpDownCounter): void {
    this.udcs.get(e.instrument)?.add(e.delta, e.attributes);
  }
  private recordHistogram(e: RecordHistogram): void {
    this.histograms.get(e.instrument)?.record(e.value, e.attributes);
  }

  // --- logs ---

  private emitLog(e: EmitLog): void {
    const span = this.activeSpan(e.anchor);
    const spanCtx = span ? trace.setSpan(context.active(), span) : context.active();
    const ts = raceTimeToUnixNanos(this.sessionStart, e.raceT);
    const record: LogRecord = {
      severityNumber: e.severity,
      severityText: SeverityNumber[e.severity],
      body: e.body,
      attributes: e.attributes,
      timestamp: nanosTimeInput(ts),
      observedTimestamp: nanosTimeInput(ts),
      context: spanCtx,
    };
    this.logger.emit(record);
  }
}

// --- per-session interpreter ------------------------------------------------

export class SessionInterpreter {
  private readonly tracer: Tracer;
  private readonly meter: Meter;
  private root: Span | null = null;
  private readonly udcs = new Map<string, UpDownCounter>();

  constructor(
    bundle: SessionBundle,
    private readonly sessionStart: Date,
    sessionInfo: SessionInfo,
  ) {
    void sessionInfo;
    this.tracer = bundle.tracerProvider.getTracer(SCOPE_NAME, SCOPE_VERSION);
    this.meter = bundle.meterProvider.getMeter(SCOPE_NAME, SCOPE_VERSION);
    this.udcs.set(
      "f1.session.cars_on_track",
      this.meter.createUpDownCounter("f1.session.cars_on_track", {
        unit: "{car}",
        description: "Number of cars still circulating",
      }),
    );
  }

  rootSpan(): Span | undefined {
    return this.root ?? undefined;
  }

  apply(effect: Effect): void {
    if (effect.kind === "start_session_span") {
      const startTime = raceTimeToUnixNanos(this.sessionStart, effect.raceT);
      this.root = this.tracer.startSpan(effect.name, { startTime: nanosTimeInput(startTime) });
    } else if (effect.kind === "end_session_span") {
      if (!this.root) return;
      const endTime = raceTimeToUnixNanos(this.sessionStart, effect.raceT);
      this.root.setStatus({ code: SpanStatusCode.OK });
      this.root.end(nanosTimeInput(endTime));
      this.root = null;
    } else if (effect.kind === "add_session_up_down_counter") {
      this.udcs.get(effect.instrument)?.add(effect.delta);
    }
  }
}

// --- helpers ----------------------------------------------------------------

/**
 * OTel JS expects `TimeInput` for span start/end. With bigint nanos we convert
 * to the `[seconds, nanos]` HrTime tuple.
 */
function nanosTimeInput(ns: bigint): [number, number] {
  const sec = Number(ns / 1_000_000_000n);
  const rem = Number(ns % 1_000_000_000n);
  return [sec, rem];
}
