/**
 * Turn OpenF1 raw rows into the bargeboard `Event` stream.
 *
 * Pure functions — no I/O lives here apart from the orchestrator `loadEvents`
 * which calls the openf1 client. Each per-endpoint transformer is independent
 * and testable in isolation.
 */

import {
  type DriverInfo,
  type Event,
  type Flag,
  type FlagColor,
  type FlagStatus,
  type LapStart,
  type PitEntry,
  type PitExit,
  type RaceControl,
  type SectorBoundary,
  type SessionInfo,
  type Telemetry,
  type TyreChange,
  SESSION_WIDE,
} from "./models.js";
import {
  getCarData,
  getDrivers,
  getLaps,
  getLocation,
  getPit,
  getRaceControl,
  getSessions,
  getStints,
  type OF1CarData,
  type OF1Lap,
  type OF1Location,
  type OF1Pit,
  type OF1RaceControl,
  type OF1Stint,
} from "./openf1.js";
import { log } from "./util.js";

// --- session + driver lookup ------------------------------------------------

/**
 * Find the OpenF1 race session for a given year + round slug
 * (e.g. "canada"). Match against country_name case-insensitively.
 */
export async function loadSessionInfo(year: number, round: string): Promise<SessionInfo> {
  const sessions = await getSessions({ year, session_name: "Race" });
  const want = round.toLowerCase();
  const match = sessions.find(
    (s) =>
      s.country_name.toLowerCase() === want ||
      s.location.toLowerCase() === want ||
      s.circuit_short_name.toLowerCase() === want,
  );
  if (!match) {
    const available = sessions.map((s) => s.country_name).join(", ");
    throw new Error(`No ${year} race found for "${round}". Available: ${available}`);
  }
  const start = new Date(match.date_start);
  const end = new Date(match.date_end);
  return {
    year: match.year,
    round_name: match.country_name,
    session_type: match.session_name,
    start_time: start,
    duration_s: (end.getTime() - start.getTime()) / 1000,
    session_key: match.session_key,
  };
}

export async function loadDrivers(session_key: number): Promise<{
  drivers: DriverInfo[];
  numberToCode: Map<number, string>;
}> {
  const raw = await getDrivers(session_key);
  const drivers: DriverInfo[] = raw.map((d) => ({
    code: d.name_acronym,
    full_name: d.full_name,
    team: d.team_name,
    car_number: d.driver_number,
  }));
  const numberToCode = new Map(raw.map((d) => [d.driver_number, d.name_acronym]));
  return { drivers, numberToCode };
}

// --- per-endpoint transformers ---------------------------------------------

function secondsBetween(a: string, b: Date): number {
  return (new Date(a).getTime() - b.getTime()) / 1000;
}

/** Map OpenF1 DRS integer to bool. 0/1/8 closed, 10/12/14 open. */
function drsOpen(v: number | null): boolean {
  return v != null && v >= 10;
}

export function lapsToEvents(
  laps: OF1Lap[],
  sessionStart: Date,
  numberToCode: Map<number, string>,
): Event[] {
  const out: Event[] = [];
  for (const lap of laps) {
    const code = numberToCode.get(lap.driver_number);
    if (!code || !lap.date_start) continue;
    const start_t = secondsBetween(lap.date_start, sessionStart);

    out.push({
      kind: "lap_start",
      race_t: start_t,
      driver_code: code,
      lap_number: lap.lap_number,
    } satisfies LapStart);

    // Sector boundaries fire at the END of each sector. Reconstruct from the
    // sector durations if present.
    const s1 = lap.duration_sector_1;
    const s2 = lap.duration_sector_2;
    const s3 = lap.duration_sector_3;
    if (s1 != null) {
      out.push({
        kind: "sector_boundary",
        race_t: start_t + s1,
        driver_code: code,
        sector: 1,
      } satisfies SectorBoundary);
    }
    if (s1 != null && s2 != null) {
      out.push({
        kind: "sector_boundary",
        race_t: start_t + s1 + s2,
        driver_code: code,
        sector: 2,
      } satisfies SectorBoundary);
    }
    if (s1 != null && s2 != null && s3 != null) {
      out.push({
        kind: "sector_boundary",
        race_t: start_t + s1 + s2 + s3,
        driver_code: code,
        sector: 3,
      } satisfies SectorBoundary);
    }
  }
  return out;
}

export function pitsToEvents(
  pits: OF1Pit[],
  sessionStart: Date,
  numberToCode: Map<number, string>,
): Event[] {
  // OpenF1's /pit endpoint records pit entry events with a duration. We model
  // each as a PitEntry at `date` and a PitExit at `date + pit_duration`.
  // When `pit_duration` is missing we still emit Entry, omit Exit.
  const out: Event[] = [];
  for (const p of pits) {
    const code = numberToCode.get(p.driver_number);
    if (!code) continue;
    const entry_t = secondsBetween(p.date, sessionStart);
    out.push({ kind: "pit_entry", race_t: entry_t, driver_code: code } satisfies PitEntry);
    if (p.pit_duration != null) {
      out.push({
        kind: "pit_exit",
        race_t: entry_t + p.pit_duration,
        driver_code: code,
      } satisfies PitExit);
    }
  }
  return out;
}

export function stintsToEvents(
  stints: OF1Stint[],
  laps: OF1Lap[],
  sessionStart: Date,
  numberToCode: Map<number, string>,
): Event[] {
  // /stints gives lap_start. Match it to that driver's lap's `date_start` for
  // the race_t. First stint of the race fires at race_t = 0.
  const lapByDriverNumber = new Map<string, OF1Lap>();
  for (const l of laps) {
    if (l.date_start) lapByDriverNumber.set(`${l.driver_number}:${l.lap_number}`, l);
  }
  const out: Event[] = [];
  for (const s of stints) {
    const code = numberToCode.get(s.driver_number);
    if (!code) continue;
    const lap = lapByDriverNumber.get(`${s.driver_number}:${s.lap_start}`);
    const race_t = lap?.date_start ? secondsBetween(lap.date_start, sessionStart) : 0;
    out.push({
      kind: "tyre_change",
      race_t,
      driver_code: code,
      compound: s.compound,
      age_laps: s.tyre_age_at_start,
    } satisfies TyreChange);
  }
  return out;
}

// --- race control: messages, flags ------------------------------------------

const FLAG_MAP: Record<string, FlagColor> = {
  YELLOW: "yellow",
  "DOUBLE YELLOW": "double_yellow",
  GREEN: "green",
  RED: "red",
  BLUE: "blue",
  WHITE: "white",
  CHEQUERED: "chequered",
  BLACK: "black",
  "BLACK AND WHITE": "black_and_white",
  "BLACK AND ORANGE": "black_and_orange",
};

export function raceControlToEvents(
  rc: OF1RaceControl[],
  sessionStart: Date,
  numberToCode: Map<number, string>,
): Event[] {
  const out: Event[] = [];
  for (const m of rc) {
    const race_t = secondsBetween(m.date, sessionStart);
    if (race_t < 0) continue;             // pre-session messages ignored
    const driver_code = m.driver_number != null
      ? (numberToCode.get(m.driver_number) ?? SESSION_WIDE)
      : SESSION_WIDE;

    // Flags: typed event.
    if (m.flag) {
      const flag = m.flag.toUpperCase();
      const color = FLAG_MAP[flag];
      if (color) {
        // OpenF1 doesn't separate deployed/withdrawn; messages like "CLEAR"
        // indicate withdrawal but flag=null on those — treat all here as
        // DEPLOYED. Withdrawals show up as catch-all race-control messages.
        const status: FlagStatus = "deployed";
        const flagEv: Flag = {
          kind: "flag",
          race_t,
          driver_code,
          color,
          status,
        };
        if (m.scope) flagEv.scope = m.scope.toLowerCase();
        out.push(flagEv);
        continue;
      }
    }

    // SC / VSC come through as category=="SafetyCar" with message text.
    if (m.category === "SafetyCar") {
      const msg = m.message.toUpperCase();
      const color: FlagColor = msg.includes("VIRTUAL") ? "vsc" : "sc";
      const status: FlagStatus = msg.includes("ENDING") || msg.includes("IN THIS LAP")
        ? "ending"
        : msg.includes("WITHDRAWN") || msg.includes("DEPLOYED") === false && msg.includes("CLEAR")
          ? "withdrawn"
          : "deployed";
      out.push({ kind: "flag", race_t, driver_code, color, status });
      continue;
    }

    // Anything else: catch-all RaceControl message.
    out.push({
      kind: "race_control",
      race_t,
      driver_code,
      message: m.message,
    } satisfies RaceControl);
  }
  return out;
}

// --- telemetry (car_data + location) ----------------------------------------

/**
 * Merge car_data and location into Telemetry events. We snap each location
 * sample to its nearest car_data sample (by timestamp) — they're recorded at
 * different rates by separate ECUs.
 *
 * If location data is missing entirely, we still emit telemetry with zeroed
 * position fields.
 */
export function telemetryToEvents(
  carData: OF1CarData[],
  location: OF1Location[],
  sessionStart: Date,
  driverCode: string,
): Telemetry[] {
  // Pre-extract sorted location timestamps for a binary search.
  const locTimes = location.map((l) => new Date(l.date).getTime());
  const out: Telemetry[] = [];

  for (const c of carData) {
    const tMs = new Date(c.date).getTime();
    const loc = locTimes.length > 0 ? location[nearestIndex(locTimes, tMs)] : undefined;
    out.push({
      kind: "telemetry",
      race_t: (tMs - sessionStart.getTime()) / 1000,
      driver_code: driverCode,
      speed_kph: c.speed ?? 0,
      rpm: c.rpm ?? 0,
      throttle: c.throttle ?? 0,
      brake: c.brake ?? 0,
      gear: c.n_gear ?? 0,
      drs: drsOpen(c.drs),
      x_m: loc?.x ?? 0,
      y_m: loc?.y ?? 0,
      z_m: loc?.z ?? 0,
    });
  }
  return out;
}

/** Index in `sorted` whose value is closest to `target`. */
function nearestIndex(sorted: number[], target: number): number {
  let lo = 0;
  let hi = sorted.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (sorted[mid]! < target) lo = mid + 1;
    else hi = mid;
  }
  if (lo > 0 && Math.abs(sorted[lo - 1]! - target) < Math.abs(sorted[lo]! - target)) {
    return lo - 1;
  }
  return lo;
}

// --- top-level orchestrator -------------------------------------------------

export interface LoadedEvents {
  session: SessionInfo;
  drivers: DriverInfo[];
  events: Event[];
}

/**
 * Fetch everything for a session and turn it into a sorted event stream.
 * Per-driver telemetry fetches run concurrently (bounded by a small pool).
 */
export async function loadEvents(
  session: SessionInfo,
  drivers: DriverInfo[],
  numberToCode: Map<number, string>,
  opts: { driverFilter?: Set<string>; skipTelemetry?: boolean } = {},
): Promise<Event[]> {
  const sessionStart = session.start_time;
  const sessionEnd = new Date(sessionStart.getTime() + session.duration_s * 1000);

  log.info(`Fetching lap/pit/stint/race_control for session ${session.session_key}...`);
  const [laps, pits, stints, rc] = await Promise.all([
    getLaps(session.session_key),
    getPit(session.session_key),
    getStints(session.session_key),
    getRaceControl(session.session_key),
  ]);

  const events: Event[] = [
    ...lapsToEvents(laps, sessionStart, numberToCode),
    ...pitsToEvents(pits, sessionStart, numberToCode),
    ...stintsToEvents(stints, laps, sessionStart, numberToCode),
    ...raceControlToEvents(rc, sessionStart, numberToCode),
  ];
  log.info(`Lap/pit/stint/rc events: ${events.length}`);

  if (opts.skipTelemetry) {
    events.sort((a, b) => a.race_t - b.race_t);
    return events;
  }

  // Per-driver concurrent telemetry. Throttle to keep OpenF1 happy.
  const targetDrivers = drivers.filter(
    (d) => !opts.driverFilter || opts.driverFilter.has(d.code),
  );
  log.info(`Fetching telemetry for ${targetDrivers.length} drivers...`);

  const POOL = 2;  // OpenF1 rate-limits hard; keep concurrency low
  let cursor = 0;
  const telemEvents: Telemetry[] = [];
  async function worker(): Promise<void> {
    while (cursor < targetDrivers.length) {
      const i = cursor++;
      const d = targetDrivers[i]!;
      try {
        const [carData, location] = await Promise.all([
          getCarData(session.session_key, d.car_number, sessionStart, sessionEnd),
          getLocation(session.session_key, d.car_number, sessionStart, sessionEnd),
        ]);
        const t = telemetryToEvents(carData, location, sessionStart, d.code);
        for (const ev of t) telemEvents.push(ev);  // avoid spread blowing the call stack on large arrays
        log.info(`  ${d.code}: ${t.length} telemetry samples`);
      } catch (e) {
        log.warn(`  ${d.code}: telemetry fetch failed: ${(e as Error).message}`);
      }
    }
  }
  await Promise.all(Array.from({ length: POOL }, () => worker()));

  for (const ev of telemEvents) events.push(ev);
  events.sort((a, b) => a.race_t - b.race_t);
  return events;
}
