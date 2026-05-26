/**
 * Typed HTTP client for the OpenF1 REST API.
 *
 * One function per endpoint. Endpoints that cap at 1000 rows (car_data,
 * location) get paginated with `paginateByDate`, which walks a date window
 * forward until the API returns fewer than the cap (= we hit the end).
 *
 * No auth, no rate-limit visible. We keep concurrency in `extract.ts`.
 */

import { log } from "./util.js";

const BASE = "https://api.openf1.org/v1";
const PAGE_CAP = 1000;

// --- raw row shapes (a subset of fields, mirroring what we actually use) ----

export interface OF1Session {
  session_key: number;
  session_type: string;        // "Race", "Qualifying", ...
  session_name: string;
  date_start: string;          // ISO
  date_end: string;            // ISO
  meeting_key: number;
  circuit_short_name: string;
  country_name: string;
  location: string;
  year: number;
}

export interface OF1Driver {
  session_key: number;
  driver_number: number;
  name_acronym: string;        // "NOR"
  full_name: string;           // "Lando NORRIS"
  team_name: string;           // "McLaren"
  broadcast_name: string;
  first_name?: string;
  last_name?: string;
}

export interface OF1Lap {
  session_key: number;
  driver_number: number;
  lap_number: number;
  date_start: string | null;
  duration_sector_1: number | null;
  duration_sector_2: number | null;
  duration_sector_3: number | null;
  i1_speed: number | null;
  i2_speed: number | null;
  st_speed: number | null;
  is_pit_out_lap: boolean;
  lap_duration: number | null;
}

export interface OF1CarData {
  session_key: number;
  driver_number: number;
  date: string;
  speed: number | null;
  rpm: number | null;
  throttle: number | null;
  brake: number | null;
  n_gear: number | null;
  drs: number | null;          // 0/1/8 closed, 10/12/14 open
}

export interface OF1Location {
  session_key: number;
  driver_number: number;
  date: string;
  x: number | null;
  y: number | null;
  z: number | null;
}

export interface OF1Pit {
  session_key: number;
  driver_number: number;
  date: string;
  lap_number: number;
  pit_duration: number | null;
  lane_duration?: number | null;
  stop_duration?: number | null;
}

export interface OF1Stint {
  session_key: number;
  driver_number: number;
  stint_number: number;
  lap_start: number;
  lap_end: number;
  compound: string;            // "SOFT" | "MEDIUM" | "HARD" | "INTERMEDIATE" | "WET"
  tyre_age_at_start: number;
}

export interface OF1RaceControl {
  session_key: number;
  date: string;
  driver_number: number | null;
  lap_number: number | null;
  category: string;            // "Flag", "Drs", "SafetyCar", "Other"
  flag: string | null;         // "YELLOW", "DOUBLE YELLOW", "GREEN", "RED", "BLUE", "WHITE", "CHEQUERED", "CLEAR", ...
  scope: string | null;        // "Track", "Sector", "Driver"
  sector: number | null;
  message: string;
}

// --- fetch helper -----------------------------------------------------------

async function fetchJson<T>(path: string): Promise<T[]> {
  const url = `${BASE}/${path}`;
  // OpenF1 rate-limits aggressively. Retry policy:
  //  - 12 attempts (worst case wait ~10 min total)
  //  - exponential backoff capped at 60s, with full jitter to avoid
  //    everybody retrying in sync after a burst
  //  - honour the Retry-After header when OpenF1 sends one
  //  - 404 means empty date window — treat as empty array, not an error
  const maxAttempts = 12;
  const baseDelay = 1000;
  const maxDelay = 60_000;
  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    try {
      const res = await fetch(url);
      if (res.ok) return (await res.json()) as T[];
      if (res.status === 404) return [];
      if (res.status === 429 || (res.status >= 500 && res.status < 600)) {
        const retryAfter = parseRetryAfter(res.headers.get("Retry-After"));
        const wait = retryAfter ?? jitteredBackoff(attempt, baseDelay, maxDelay);
        if (attempt >= 3) {
          log.info(`OpenF1 ${path} -> ${res.status}, retrying in ${wait}ms (attempt ${attempt}/${maxAttempts})`);
        }
        await new Promise((r) => setTimeout(r, wait));
        continue;
      }
      throw new Error(`OpenF1 ${path} -> ${res.status} ${res.statusText}`);
    } catch (e) {
      if (attempt === maxAttempts) throw e;
      const wait = jitteredBackoff(attempt, baseDelay, maxDelay);
      log.debug(`OpenF1 ${path} threw: ${(e as Error).message}; retrying in ${wait}ms`);
      await new Promise((r) => setTimeout(r, wait));
    }
  }
  throw new Error(`OpenF1 ${path} -> exhausted retries`);
}

/** Exponential backoff with full jitter: random in [0, min(max, base * 2^(n-1))]. */
function jitteredBackoff(attempt: number, base: number, max: number): number {
  const ceiling = Math.min(max, base * 2 ** (attempt - 1));
  return Math.floor(Math.random() * ceiling);
}

/** Parse HTTP Retry-After (seconds-int form only — OpenF1 doesn't use HTTP-date). */
function parseRetryAfter(header: string | null): number | null {
  if (!header) return null;
  const seconds = Number(header);
  return Number.isFinite(seconds) ? seconds * 1000 : null;
}

/** URL-encode a key=value pair, leaving the API's `>` / `<` operators intact. */
function q(params: Record<string, string | number | undefined>): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined) continue;
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
  }
  return parts.join("&");
}

// --- endpoints --------------------------------------------------------------

export async function getSessions(params: {
  year: number;
  country_name?: string;
  session_name?: string;
}): Promise<OF1Session[]> {
  return fetchJson<OF1Session>(`sessions?${q(params)}`);
}

export async function getDrivers(session_key: number): Promise<OF1Driver[]> {
  return fetchJson<OF1Driver>(`drivers?${q({ session_key })}`);
}

export async function getLaps(session_key: number): Promise<OF1Lap[]> {
  return fetchJson<OF1Lap>(`laps?${q({ session_key })}`);
}

export async function getPit(session_key: number): Promise<OF1Pit[]> {
  return fetchJson<OF1Pit>(`pit?${q({ session_key })}`);
}

export async function getStints(session_key: number): Promise<OF1Stint[]> {
  return fetchJson<OF1Stint>(`stints?${q({ session_key })}`);
}

export async function getRaceControl(session_key: number): Promise<OF1RaceControl[]> {
  return fetchJson<OF1RaceControl>(`race_control?${q({ session_key })}`);
}

/**
 * Paginate `car_data` for one driver across the session.
 *
 * OpenF1 caps each response at 1000 rows. We page by date: ask for everything
 * from the current cursor onward; if we got 1000 rows, advance the cursor to
 * just past the last `date` and ask again. Otherwise we hit the end.
 *
 * The "+1ms" cursor bump avoids re-fetching the boundary row on the next page.
 */
export async function getCarData(
  session_key: number,
  driver_number: number,
  startDate: Date,
  endDate: Date,
): Promise<OF1CarData[]> {
  return paginateByDate<OF1CarData>("car_data", session_key, driver_number, startDate, endDate);
}

export async function getLocation(
  session_key: number,
  driver_number: number,
  startDate: Date,
  endDate: Date,
): Promise<OF1Location[]> {
  return paginateByDate<OF1Location>("location", session_key, driver_number, startDate, endDate);
}

async function paginateByDate<T extends { date: string }>(
  endpoint: string,
  session_key: number,
  driver_number: number,
  startDate: Date,
  endDate: Date,
): Promise<T[]> {
  const all: T[] = [];
  let cursor = startDate.toISOString();
  const endIso = endDate.toISOString();

  for (let page = 0; page < 200; page++) {     // hard stop, prevents runaway loops
    const url = `${endpoint}?session_key=${session_key}&driver_number=${driver_number}&date>${cursor}&date<${endIso}`;
    const rows = await fetchJson<T>(url);
    if (rows.length === 0) break;
    all.push(...rows);
    if (rows.length < PAGE_CAP) break;
    // advance cursor past the last date we got, +1 ms to avoid boundary repeat
    const last = rows[rows.length - 1]!.date;
    const nextMs = new Date(last).getTime() + 1;
    cursor = new Date(nextMs).toISOString();
  }
  log.debug(`${endpoint}: driver ${driver_number} -> ${all.length} rows`);
  return all;
}
