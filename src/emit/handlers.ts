/**
 * Pure (state, event) -> { state, effects } handlers for each event kind.
 *
 * Mirrors the Python state machine. Closes prior children before opening
 * successors, records histograms at close-time, emits typed span events +
 * logs where the brief calls for them.
 */

import { SpanStatusCode, type Attributes } from "@opentelemetry/api";
import { SeverityNumber } from "@opentelemetry/api-logs";

import type {
  DriverInfo,
  Event,
  Flag,
  GapUpdate,
  Investigation,
  LapStart,
  Penalty,
  PitEntry,
  PitExit,
  PositionChange,
  RaceControl,
  RaceFinish,
  Retirement,
  SectorBoundary,
  Telemetry,
  TyreChange,
  Weather,
} from "../models.js";
import type { DriverState } from "./state.js";
import type { Effect } from "./effects.js";
import {
  FLAG_SEVERITY,
  INVESTIGATION_SEVERITY,
  PENALTY_SEVERITY,
} from "./severity.js";

export interface HandlerResult {
  state: DriverState;
  effects: Effect[];
}

export function handle(
  state: DriverState,
  event: Event,
  driver: DriverInfo,
): HandlerResult {
  switch (event.kind) {
    case "lap_start": return onLapStart(state, event, driver);
    case "sector_boundary": return onSectorBoundary(state, event);
    case "pit_entry": return onPitEntry(state, event, driver);
    case "pit_exit": return onPitExit(state, event);
    case "telemetry": return onTelemetry(state, event);
    case "tyre_change": return onTyreChange(state, event);
    case "race_control": return onRaceControl(state, event);
    case "flag": return onFlag(state, event);
    case "penalty": return onPenalty(state, event);
    case "investigation": return onInvestigation(state, event);
    case "position_change": return onPositionChange(state, event);
    case "gap_update": return onGapUpdate(state, event);
    case "retirement": return onRetirement(state, event);
    case "race_finish": return onRaceFinish(state, event);
    case "weather": return { state, effects: [] };  // session-scoped; see handleSessionEvent
  }
}

// --- helpers ---------------------------------------------------------------

function driverAttrs(state: DriverState, extra: Attributes = {}): Attributes {
  return { "f1.driver.code": state.code, ...extra };
}

// --- lap / sector / pit ----------------------------------------------------

function onLapStart(state: DriverState, ev: LapStart, driver: DriverInfo): HandlerResult {
  const effects: Effect[] = [];

  // Close any open pit + sector spans first.
  if (state.pitSpanId) {
    effects.push({ kind: "end_span", role: "pit", raceT: ev.race_t });
  }
  if (state.sectorSpanId) {
    effects.push({ kind: "end_span", role: "sector", raceT: ev.race_t });
  }
  // If a previous lap is open, record its histograms then close it.
  if (state.lapSpanId && state.lapStartT != null) {
    const lapSeconds = ev.race_t - state.lapStartT;
    effects.push({
      kind: "record_histogram",
      instrument: "f1.driver.lap_time",
      value: lapSeconds,
      // No attributes — histograms aggregate across the race. Per-lap
      // breakdown lives on the matching span (each lap is one span).
    });
    if (state.lapTopSpeed > 0) {
      effects.push({
        kind: "record_histogram",
        instrument: "f1.driver.top_speed",
        value: state.lapTopSpeed,
        // No attributes — histograms aggregate across the race. Per-lap
      // breakdown lives on the matching span (each lap is one span).
      });
    }
    effects.push({ kind: "end_span", role: "lap", raceT: ev.race_t });
  }

  // Open the new lap.
  effects.push({
    kind: "start_span",
    role: "lap",
    parentRole: "race",
    name: `${driver.code}_L${ev.lap_number}`,
    raceT: ev.race_t,
    attributes: { "f1.lap.number": ev.lap_number },
  });
  effects.push({ kind: "add_counter", instrument: "f1.driver.laps_completed", delta: 1 });
  // Open S1.
  effects.push({
    kind: "start_span",
    role: "sector",
    parentRole: "lap",
    name: `${driver.code}_L${ev.lap_number}_S1`,
    raceT: ev.race_t,
    attributes: { "f1.lap.number": ev.lap_number, "f1.sector.number": 1 },
  });

  return {
    state: {
      ...state,
      pitSpanId: null,
      pitEntryT: null,
      lapSpanId: `L${ev.lap_number}`,
      lapNumber: ev.lap_number,
      lapStartT: ev.race_t,
      lapTopSpeed: 0,
      sectorSpanId: `L${ev.lap_number}_S1`,
      sectorNumber: 1,
      sectorStartT: ev.race_t,
    },
    effects,
  };
}

function onSectorBoundary(state: DriverState, ev: SectorBoundary): HandlerResult {
  // Boundary fires at END of sector N. Record sector_time, close N, open N+1
  // (unless N === 3, end of lap).
  const effects: Effect[] = [];
  if (state.sectorSpanId && state.sectorStartT != null) {
    const secs = ev.race_t - state.sectorStartT;
    effects.push({
      kind: "record_histogram",
      instrument: "f1.driver.sector_time",
      value: secs,
      // No attributes — see f1.driver.lap_time comment. Per-sector drill-down
      // lives on the sector span.
    });
    effects.push({ kind: "end_span", role: "sector", raceT: ev.race_t });
  }
  // Speed trap tied to this boundary (i1/i2/st) → gauge split by trap attr.
  if (ev.trap_speed_kph != null) {
    const trap = ev.sector === 1 ? "i1" : ev.sector === 2 ? "i2" : "st";
    effects.push({
      kind: "set_gauge",
      instrument: "f1.driver.trap_speed",
      value: ev.trap_speed_kph,
      attributes: { "f1.trap": trap },
    });
  }
  let nextSector: 0 | 1 | 2 | 3 = 0;
  let nextSectorSpanId: string | null = null;
  let nextSectorStart: number | null = null;
  if (ev.sector < 3 && state.lapSpanId) {
    const n = (ev.sector + 1) as 1 | 2 | 3;
    nextSector = n;
    nextSectorSpanId = `L${state.lapNumber}_S${n}`;
    nextSectorStart = ev.race_t;
    effects.push({
      kind: "start_span",
      role: "sector",
      parentRole: "lap",
      name: `${state.code}_L${state.lapNumber}_S${n}`,
      raceT: ev.race_t,
      attributes: { "f1.lap.number": state.lapNumber, "f1.sector.number": n },
    });
  }
  return {
    state: {
      ...state,
      sectorSpanId: nextSectorSpanId,
      sectorNumber: nextSector,
      sectorStartT: nextSectorStart,
    },
    effects,
  };
}

function onPitEntry(state: DriverState, ev: PitEntry, driver: DriverInfo): HandlerResult {
  if (!state.lapSpanId) return { state, effects: [] };
  const effects: Effect[] = [{
    kind: "start_span",
    role: "pit",
    parentRole: "lap",
    name: `${driver.code}_L${state.lapNumber}_pit`,
    raceT: ev.race_t,
    attributes: { "f1.lap.number": state.lapNumber },
  }];
  return {
    state: { ...state, pitSpanId: `L${state.lapNumber}_pit`, pitEntryT: ev.race_t },
    effects,
  };
}

function onPitExit(state: DriverState, ev: PitExit): HandlerResult {
  const effects: Effect[] = [];
  if (state.pitEntryT != null) {
    const secs = ev.race_t - state.pitEntryT;
    effects.push({
      kind: "record_histogram",
      instrument: "f1.driver.pit_duration",
      value: secs,
      // No attributes — histograms aggregate across the race. Per-lap
      // breakdown lives on the matching span (each lap is one span).
    });
  }
  effects.push({ kind: "add_counter", instrument: "f1.driver.pit_stops", delta: 1 });
  if (state.pitSpanId) effects.push({ kind: "end_span", role: "pit", raceT: ev.race_t });
  return {
    state: { ...state, pitSpanId: null, pitEntryT: null },
    effects,
  };
}

// --- telemetry -------------------------------------------------------------

function onTelemetry(state: DriverState, ev: Telemetry): HandlerResult {
  // Don't emit gauge effects here — at OpenF1's ~3.7 Hz × 22 drivers ×
  // 9 gauges per sample, that's 4.5M+ gauge.set calls for a single race.
  // Instead, just track lap top speed and stash the latest sample; the
  // engine flushes one batch of gauge effects per tick (see
  // `flushTelemetryGauges`). Gauges are LastValue, so collapsing
  // intervening samples within a tick window is semantically identical.
  const lapTopSpeed = ev.speed_kph > state.lapTopSpeed ? ev.speed_kph : state.lapTopSpeed;
  return {
    state: { ...state, lapTopSpeed, latestTelemetry: ev },
    effects: [],
  };
}

/** Emit gauge effects from the most recent Telemetry observed in this tick,
 *  if any. Clears `latestTelemetry` so the next tick only flushes when a
 *  fresh sample arrived. */
export function flushTelemetryGauges(state: DriverState): {
  state: DriverState;
  effects: Effect[];
} {
  const ev = state.latestTelemetry;
  if (!ev) return { state, effects: [] };
  const effects: Effect[] = [
    { kind: "set_gauge", instrument: "f1.car.speed", value: ev.speed_kph },
    { kind: "set_gauge", instrument: "f1.car.rpm", value: ev.rpm },
    { kind: "set_gauge", instrument: "f1.car.throttle", value: ev.throttle },
    { kind: "set_gauge", instrument: "f1.car.brake", value: ev.brake },
    { kind: "set_gauge", instrument: "f1.car.gear", value: ev.gear },
    { kind: "set_gauge", instrument: "f1.car.drs", value: ev.drs ? 1 : 0 },
    { kind: "set_gauge", instrument: "f1.car.position_x", value: ev.x_m },
    { kind: "set_gauge", instrument: "f1.car.position_y", value: ev.y_m },
    { kind: "set_gauge", instrument: "f1.car.position_z", value: ev.z_m },
  ];
  return { state: { ...state, latestTelemetry: null }, effects };
}

// --- typed race-control events ---------------------------------------------

function onTyreChange(state: DriverState, ev: TyreChange): HandlerResult {
  const attrs = driverAttrs(state, {
    "f1.tyre.compound": ev.compound,
    "f1.tyre.age_laps": ev.age_laps,
  });
  return {
    state,
    effects: [
      { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: "tyre_change", attributes: attrs },
      {
        kind: "emit_log",
        anchor: "active",
        raceT: ev.race_t,
        severity: SeverityNumber.DEBUG,
        body: `${ev.compound} tyres fitted (age ${ev.age_laps})`,
        attributes: attrs,
      },
    ],
  };
}

function onRaceControl(state: DriverState, ev: RaceControl): HandlerResult {
  const attrs = driverAttrs(state, { "f1.race_control.message": ev.message });
  return {
    state,
    effects: [
      { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: "race_control", attributes: attrs },
      { kind: "emit_log", anchor: "active", raceT: ev.race_t, severity: SeverityNumber.INFO, body: ev.message, attributes: attrs },
    ],
  };
}

function onFlag(state: DriverState, ev: Flag): HandlerResult {
  const attrs: Attributes = driverAttrs(state, {
    "f1.flag.color": ev.color,
    "f1.flag.status": ev.status,
  });
  if (ev.scope) attrs["f1.flag.scope"] = ev.scope;
  const effects: Effect[] = [
    { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: `flag.${ev.color}`, attributes: attrs },
  ];
  if (ev.color === "blue") {
    effects.push({ kind: "add_counter", instrument: "f1.driver.blue_flags", delta: 1 });
  }
  const severity = FLAG_SEVERITY[ev.color];
  const scopeStr = ev.scope ? ` (${ev.scope})` : "";
  effects.push({
    kind: "emit_log",
    anchor: "active",
    raceT: ev.race_t,
    severity,
    body: `${ev.color} flag ${ev.status}${scopeStr}`,
    attributes: attrs,
  });
  return { state, effects };
}

function onPenalty(state: DriverState, ev: Penalty): HandlerResult {
  const attrs: Attributes = driverAttrs(state, {
    "f1.penalty.type": ev.type,
    "f1.penalty.reason": ev.reason,
  });
  if (ev.seconds != null) attrs["f1.penalty.seconds"] = ev.seconds;
  return {
    state,
    effects: [
      { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: `penalty.${ev.type}`, attributes: attrs },
      // Counter keyed by driver datapoint attribute only — keep cardinality at
      // one series per driver. Penalty type lives on the span event for drill-down.
      { kind: "add_counter", instrument: "f1.driver.penalties", delta: 1 },
      {
        kind: "emit_log",
        anchor: "active",
        raceT: ev.race_t,
        severity: PENALTY_SEVERITY[ev.type],
        body: `${ev.type} penalty: ${ev.reason}`,
        attributes: attrs,
      },
    ],
  };
}

function onInvestigation(state: DriverState, ev: Investigation): HandlerResult {
  const attrs = driverAttrs(state, {
    "f1.investigation.status": ev.status,
    "f1.investigation.reason": ev.reason,
  });
  const effects: Effect[] = [
    { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: `investigation.${ev.status}`, attributes: attrs },
  ];
  // Dedupe: only count at under_investigation, not at noted or resolution.
  if (ev.status === "under_investigation") {
    effects.push({ kind: "add_counter", instrument: "f1.driver.investigations", delta: 1 });
  }
  effects.push({
    kind: "emit_log",
    anchor: "active",
    raceT: ev.race_t,
    severity: INVESTIGATION_SEVERITY[ev.status],
    body: `investigation ${ev.status}: ${ev.reason}`,
    attributes: attrs,
  });
  return { state, effects };
}

function onPositionChange(state: DriverState, ev: PositionChange): HandlerResult {
  return {
    state,
    effects: [
      { kind: "set_gauge", instrument: "f1.driver.standings_position", value: ev.position },
    ],
  };
}

function onGapUpdate(state: DriverState, ev: GapUpdate): HandlerResult {
  const effects: Effect[] = [];
  if (ev.gap_to_leader_s != null) {
    effects.push({ kind: "set_gauge", instrument: "f1.driver.gap_to_leader", value: ev.gap_to_leader_s });
  }
  if (ev.interval_s != null) {
    effects.push({ kind: "set_gauge", instrument: "f1.driver.interval", value: ev.interval_s });
    // Also feed the exponential histogram — the interval's ratio scale
    // (0.05s DRS battles to minute-long gaps) is what exp buckets are for.
    effects.push({
      kind: "record_histogram",
      instrument: "f1.driver.interval_distribution",
      value: ev.interval_s,
    });
  }
  return { state, effects };
}

function onRetirement(state: DriverState, ev: Retirement): HandlerResult {
  if (state.retired || state.raceClosed) return { state, effects: [] };
  const attrs = driverAttrs(state, { "f1.retirement.reason": ev.reason });
  return {
    state: { ...state, retired: true, raceClosed: true,
             pitSpanId: null, sectorSpanId: null, lapSpanId: null, raceSpanId: null,
             pitEntryT: null, sectorStartT: null, lapStartT: null },
    effects: [
      ...closeOpenChildren(state, ev.race_t, `DNF: ${ev.reason}`),
      {
        kind: "emit_log",
        anchor: "race",
        raceT: ev.race_t,
        severity: SeverityNumber.ERROR,
        body: `Retired: ${ev.reason}`,
        attributes: attrs,
      },
      { kind: "add_session_up_down_counter", instrument: "f1.session.cars_on_track", delta: -1 },
      // Close the race span at the retirement moment, not at engine exit.
      {
        kind: "end_span",
        role: "race",
        raceT: ev.race_t,
        status: { code: SpanStatusCode.ERROR, message: `DNF: ${ev.reason}` },
        endEvent: { name: "retirement", attributes: attrs },
      },
    ],
  };
}

function onRaceFinish(state: DriverState, ev: RaceFinish): HandlerResult {
  // Already closed (e.g. driver retired earlier) → no-op.
  if (state.raceClosed) return { state, effects: [] };
  const effects: Effect[] = [
    ...closeOpenChildren(state, ev.race_t),
    { kind: "end_span", role: "race", raceT: ev.race_t,
      status: { code: SpanStatusCode.OK } },
  ];
  // Credit championship points at the flag. Up-down counter: a future DSQ
  // could subtract them again.
  if (ev.points != null && ev.points > 0) {
    effects.push({
      kind: "add_up_down_counter",
      instrument: "f1.driver.championship_points",
      delta: ev.points,
    });
  }
  return {
    state: { ...state, raceClosed: true,
             pitSpanId: null, sectorSpanId: null, lapSpanId: null, raceSpanId: null,
             pitEntryT: null, sectorStartT: null, lapStartT: null },
    effects,
  };
}

// --- session-scoped events (drained by the engine, applied to the session
//     interpreter directly — never fanned out per driver) -------------------

/** Weather and any future session-scoped events: metric effects with NO
 *  driver attributes, applied straight to the SessionInterpreter. */
export function handleSessionEvent(event: Event): Effect[] {
  if (event.kind !== "weather") return [];
  const w: Weather = event;
  return [
    { kind: "set_gauge", instrument: "f1.session.air_temp", value: w.air_temp_c },
    { kind: "set_gauge", instrument: "f1.session.track_temp", value: w.track_temp_c },
    { kind: "set_gauge", instrument: "f1.session.humidity", value: w.humidity_pct },
    { kind: "set_gauge", instrument: "f1.session.rainfall", value: w.rainfall },
    { kind: "set_gauge", instrument: "f1.session.wind_speed", value: w.wind_speed_ms },
  ];
}

/** Emit end_span effects for any still-open child spans (pit, sector, lap) at
 *  the given race_t — used by both Retirement and RaceFinish so the race
 *  span closes cleanly with no orphaned children. When the driver is
 *  retiring, the still-open child spans get ERROR status too: those are
 *  literally the spans the car failed in, so clicking through the trace
 *  shows ERROR all the way from race → lap → sector. */
function closeOpenChildren(
  state: DriverState,
  raceT: number,
  reason?: string,
): Effect[] {
  const status = reason
    ? { code: SpanStatusCode.ERROR, message: reason }
    : undefined;
  const out: Effect[] = [];
  if (state.pitSpanId) out.push({ kind: "end_span", role: "pit", raceT, status });
  if (state.sectorSpanId) out.push({ kind: "end_span", role: "sector", raceT, status });
  if (state.lapSpanId) out.push({ kind: "end_span", role: "lap", raceT, status });
  return out;
}

// --- race lifecycle (called by the engine, not from event dispatch) --------

export function openSessionRoot(name: string, raceT: number): Effect[] {
  return [{ kind: "start_session_span", name, raceT }];
}

export function closeSessionRoot(raceT: number): Effect[] {
  return [{ kind: "end_session_span", raceT }];
}

/** Translate a pre-race event into a span event on the session root.
 *  The root opens at lights-out, so these carry timestamps preceding the
 *  span start — pre-race context without a formation span eating the
 *  front of the timeline. Driver-specific events (stewards notes, etc.)
 *  keep their driver code as an attribute. Returns [] for event kinds
 *  that should never appear pre-race (laps, telemetry, etc.) — those
 *  would indicate a data bug. */
export function handleFormation(event: Event): Effect[] {
  const at = event.race_t;
  switch (event.kind) {
    case "race_control":
      return [{
        kind: "add_root_span_event",
        raceT: at,
        name: "race_control",
        attributes: formationAttrs(event.driver_code, {
          "f1.race_control.message": event.message,
        }),
      }];
    case "flag":
      return [{
        kind: "add_root_span_event",
        raceT: at,
        name: `flag.${event.color}`,
        attributes: formationAttrs(event.driver_code, {
          "f1.flag.color": event.color,
          "f1.flag.status": event.status,
          ...(event.scope ? { "f1.flag.scope": event.scope } : {}),
        }),
      }];
    case "penalty":
      return [{
        kind: "add_root_span_event",
        raceT: at,
        name: `penalty.${event.type}`,
        attributes: formationAttrs(event.driver_code, {
          "f1.penalty.type": event.type,
          "f1.penalty.reason": event.reason,
          ...(event.seconds != null ? { "f1.penalty.seconds": event.seconds } : {}),
        }),
      }];
    case "investigation":
      return [{
        kind: "add_root_span_event",
        raceT: at,
        name: `investigation.${event.status}`,
        attributes: formationAttrs(event.driver_code, {
          "f1.investigation.status": event.status,
          "f1.investigation.reason": event.reason,
        }),
      }];
    default:
      return [];
  }
}

function formationAttrs(driverCode: string, extra: Attributes): Attributes {
  // "*" means session-wide; omit it as an attribute. Otherwise keep the code
  // so per-driver pre-race events stay attributable.
  if (driverCode === "*") return extra;
  return { ...extra, "f1.driver.code": driverCode };
}

export function openDriverRace(driver: DriverInfo, raceT: number): {
  state: Partial<DriverState>;
  effects: Effect[];
} {
  return {
    state: { raceSpanId: "race" },
    effects: [
      {
        kind: "start_span",
        role: "race",
        parentRole: "session_root",
        name: `${driver.code}_race`,
        raceT,
      },
      { kind: "add_session_up_down_counter", instrument: "f1.session.cars_on_track", delta: 1 },
    ],
  };
}

/** Engine-exit safety net: closes spans for any driver whose race wasn't
 *  already wrapped up via Retirement or RaceFinish. Most drivers should hit
 *  one of those during dispatch; this only fires if extraction missed a
 *  finish for some reason. */
export function closeDriverRace(state: DriverState, raceT: number): Effect[] {
  if (state.raceClosed) return [];
  const effects: Effect[] = closeOpenChildren(state, raceT);
  if (state.raceSpanId) {
    const status = state.retired
      ? { code: SpanStatusCode.ERROR, message: "DNF" }
      : { code: SpanStatusCode.OK };
    effects.push({ kind: "end_span", role: "race", raceT, status });
  }
  return effects;
}
