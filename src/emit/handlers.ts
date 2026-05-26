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
  DefensiveMove,
  DriverInfo,
  Event,
  Flag,
  Investigation,
  LapStart,
  Penalty,
  PitEntry,
  PitExit,
  RaceControl,
  Retirement,
  SectorBoundary,
  Telemetry,
  TyreChange,
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
    case "defensive_move": return onDefensiveMove(state, event);
    case "retirement": return onRetirement(state, event);
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
      attributes: { "f1.lap.number": state.lapNumber },
    });
    if (state.lapTopSpeed > 0) {
      effects.push({
        kind: "record_histogram",
        instrument: "f1.driver.top_speed",
        value: state.lapTopSpeed,
        attributes: { "f1.lap.number": state.lapNumber },
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
      attributes: { "f1.lap.number": state.lapNumber, "f1.sector.number": ev.sector },
    });
    effects.push({ kind: "end_span", role: "sector", raceT: ev.race_t });
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
      attributes: { "f1.lap.number": state.lapNumber },
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
  const lapTopSpeed = ev.speed_kph > state.lapTopSpeed ? ev.speed_kph : state.lapTopSpeed;
  return { state: { ...state, lapTopSpeed }, effects };
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
      { kind: "add_counter", instrument: "f1.driver.penalties", delta: 1, attributes: { "f1.penalty.type": ev.type } },
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

function onDefensiveMove(state: DriverState, ev: DefensiveMove): HandlerResult {
  const attrs = driverAttrs(state, {
    "f1.defensive_move.lateral_m": ev.lateral_meters,
    "f1.defensive_move.chaser_code": ev.chaser_code,
    "f1.defensive_move.chaser_gap_m": ev.chaser_gap_m,
  });
  return {
    state,
    effects: [
      { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: "defensive_move", attributes: attrs },
      { kind: "add_counter", instrument: "f1.driver.defensive_moves", delta: 1 },
      {
        kind: "emit_log",
        anchor: "active",
        raceT: ev.race_t,
        severity: SeverityNumber.WARN,
        body: `Defensive move under braking: ${ev.lateral_meters.toFixed(1)}m lateral, chaser ${ev.chaser_code} ${ev.chaser_gap_m.toFixed(1)}m back`,
        attributes: attrs,
      },
    ],
  };
}

function onRetirement(state: DriverState, ev: Retirement): HandlerResult {
  if (state.retired) return { state, effects: [] };
  const attrs = driverAttrs(state, { "f1.retirement.reason": ev.reason });
  return {
    state: { ...state, retired: true },
    effects: [
      { kind: "add_span_event", anchor: "active", raceT: ev.race_t, name: "retirement", attributes: attrs },
      {
        kind: "emit_log",
        anchor: "active",
        raceT: ev.race_t,
        severity: SeverityNumber.ERROR,
        body: `Retired: ${ev.reason}`,
        attributes: attrs,
      },
      { kind: "add_session_up_down_counter", instrument: "f1.session.cars_on_track", delta: -1 },
    ],
  };
}

// --- race lifecycle (called by the engine, not from event dispatch) --------

export function openSessionRoot(name: string, raceT: number): Effect[] {
  return [{ kind: "start_session_span", name, raceT }];
}

export function closeSessionRoot(raceT: number): Effect[] {
  return [{ kind: "end_session_span", raceT }];
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

export function closeDriverRace(state: DriverState, raceT: number): Effect[] {
  const effects: Effect[] = [];
  if (state.pitSpanId) effects.push({ kind: "end_span", role: "pit", raceT });
  if (state.sectorSpanId) effects.push({ kind: "end_span", role: "sector", raceT });
  if (state.lapSpanId) effects.push({ kind: "end_span", role: "lap", raceT });
  if (state.raceSpanId) {
    const status = state.retired
      ? { code: SpanStatusCode.ERROR, message: "DNF" }
      : { code: SpanStatusCode.OK };
    effects.push({ kind: "end_span", role: "race", raceT, status });
  }
  return effects;
}
