/**
 * Core data model: sessions, drivers, replay events.
 *
 * Events are the unit of replay. Each carries a `race_t` (seconds from session
 * start) and a `driver_code`. The session-wide sentinel "*" is fanned out to
 * every driver before the engine sees the queue.
 *
 * Events are a discriminated union over `kind`. Add a case to `Event`, add a
 * `kind` literal, write a handler in `emit/handlers.ts`.
 */

export type DriverState =
  | "pre_race"
  | "racing"
  | "pit"
  | "vsc"
  | "sc"
  | "dnf"
  | "finished";

export type FlagColor =
  | "yellow"
  | "double_yellow"
  | "green"
  | "red"
  | "blue"
  | "white"
  | "chequered"
  | "black"
  | "black_and_white"
  | "black_and_orange"
  | "sc"
  | "vsc";

export type FlagStatus = "deployed" | "withdrawn" | "ending";

export type PenaltyType =
  | "5_second"
  | "10_second"
  | "drive_through"
  | "stop_go_10"
  | "grid"
  | "reprimand"
  | "disqualification";

export type InvestigationStatus =
  | "noted"
  | "under_investigation"
  | "no_action"
  | "penalty";

export interface SessionInfo {
  year: number;
  round_name: string;       // "Canada"
  session_type: string;     // "Race"
  start_time: Date;         // absolute wall-clock of session start
  duration_s: number;       // session-end minus session-start, in seconds
  session_key: number;      // OpenF1 session key
}

export interface DriverInfo {
  code: string;             // "ANT"
  full_name: string;        // "Kimi Antonelli"
  team: string;             // "Mercedes"
  car_number: number;       // 12
}

// race_t is seconds from session start. Sub-second precision is preserved by
// using a JS number (float64). Times are large enough we don't lose precision.

export interface LapStart {
  kind: "lap_start";
  race_t: number;
  driver_code: string;
  lap_number: number;
}

export interface SectorBoundary {
  kind: "sector_boundary";
  race_t: number;
  driver_code: string;
  sector: 1 | 2 | 3;        // fires at END of this sector
}

export interface PitEntry {
  kind: "pit_entry";
  race_t: number;
  driver_code: string;
}

export interface PitExit {
  kind: "pit_exit";
  race_t: number;
  driver_code: string;
}

export interface TyreChange {
  kind: "tyre_change";
  race_t: number;
  driver_code: string;
  compound: string;        // "SOFT" | "MEDIUM" | "HARD" | "INTERMEDIATE" | "WET"
  age_laps: number;
}

export interface Telemetry {
  kind: "telemetry";
  race_t: number;
  driver_code: string;
  speed_kph: number;
  rpm: number;
  throttle: number;        // 0-100
  brake: number;           // 0-100
  gear: number;            // 0-8
  drs: boolean;
  x_m: number;
  y_m: number;
  z_m: number;
}

export interface RaceControl {
  kind: "race_control";
  race_t: number;
  driver_code: string;     // "*" for session-wide
  message: string;
}

export interface Flag {
  kind: "flag";
  race_t: number;
  driver_code: string;     // "*" for session-wide
  color: FlagColor;
  status: FlagStatus;
  scope?: string;          // "track" | "sector_1" | ...
}

export interface Penalty {
  kind: "penalty";
  race_t: number;
  driver_code: string;
  type: PenaltyType;
  reason: string;
  seconds?: number;
}

export interface Investigation {
  kind: "investigation";
  race_t: number;
  driver_code: string;
  status: InvestigationStatus;
  reason: string;
}

export interface DefensiveMove {
  kind: "defensive_move";
  race_t: number;
  driver_code: string;
  lateral_meters: number;
  chaser_code: string;
  chaser_gap_m: number;
}

export interface Retirement {
  kind: "retirement";
  race_t: number;
  driver_code: string;
  reason: string;
}

export type Event =
  | LapStart
  | SectorBoundary
  | PitEntry
  | PitExit
  | TyreChange
  | Telemetry
  | RaceControl
  | Flag
  | Penalty
  | Investigation
  | DefensiveMove
  | Retirement;

export const SESSION_WIDE = "*";
