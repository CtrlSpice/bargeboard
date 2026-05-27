/**
 * Parquet-backed cache for OpenF1 session data.
 *
 * Cache layout: ~/.cache/bargeboard/<session_key>/
 *   telemetry.parquet  — all Telemetry events (high-volume, column-friendly)
 *   events.json        — all non-telemetry events (small, schema-heterogeneous)
 *   meta.json          — raceStartT, grid, version tag
 *
 * Cache is keyed by session_key. Race data is immutable once the event is
 * done, so we never expire entries — use --no-cache to force a fresh fetch.
 */

import { mkdir, readFile, writeFile, access } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import parquet from "parquetjs-lite";
import type { Event, Telemetry } from "./models.js";
import { log } from "./util.js";

const CACHE_VERSION = 1;

// --- schema -----------------------------------------------------------------

const TELEM_SCHEMA = new parquet.ParquetSchema({
  race_t:     { type: "DOUBLE" },
  driver_code: { type: "UTF8" },
  speed_kph:  { type: "DOUBLE" },
  rpm:        { type: "DOUBLE" },
  throttle:   { type: "DOUBLE" },
  brake:      { type: "DOUBLE" },
  gear:       { type: "INT32" },
  drs:        { type: "BOOLEAN" },
  x_m:        { type: "DOUBLE" },
  y_m:        { type: "DOUBLE" },
  z_m:        { type: "DOUBLE" },
});

// --- paths ------------------------------------------------------------------

function sessionCacheDir(sessionKey: number): string {
  return join(homedir(), ".cache", "bargeboard", String(sessionKey));
}

async function ensureCacheDir(sessionKey: number): Promise<string> {
  const dir = sessionCacheDir(sessionKey);
  await mkdir(dir, { recursive: true });
  return dir;
}

// --- meta -------------------------------------------------------------------

interface CacheMeta {
  version: number;
  session_key: number;
  raceStartT: number;
  grid: [string, number][];
  cachedAt: string;
}

// --- public API -------------------------------------------------------------

export interface CachedSession {
  events: Event[];
  raceStartT: number;
  grid: Map<string, number>;
}

export async function hasCachedSession(sessionKey: number): Promise<boolean> {
  const dir = sessionCacheDir(sessionKey);
  try {
    await access(join(dir, "meta.json"));
    await access(join(dir, "telemetry.parquet"));
    await access(join(dir, "events.json"));
    return true;
  } catch {
    return false;
  }
}

export async function readCachedSession(sessionKey: number): Promise<CachedSession> {
  const dir = sessionCacheDir(sessionKey);

  const metaRaw = await readFile(join(dir, "meta.json"), "utf-8");
  const meta: CacheMeta = JSON.parse(metaRaw);

  if (meta.version !== CACHE_VERSION) {
    throw new Error(`Cache version mismatch (got ${meta.version}, want ${CACHE_VERSION})`);
  }

  // Read non-telemetry events from JSON.
  const eventsRaw = await readFile(join(dir, "events.json"), "utf-8");
  const events: Event[] = JSON.parse(eventsRaw);

  // Read telemetry from Parquet.
  const reader = await parquet.ParquetReader.openFile(join(dir, "telemetry.parquet"));
  const cursor = reader.getCursor();
  let row: Record<string, unknown> | null;
  while ((row = await cursor.next()) !== null) {
    events.push({
      kind:       "telemetry",
      race_t:     row["race_t"] as number,
      driver_code: row["driver_code"] as string,
      speed_kph:  row["speed_kph"] as number,
      rpm:        row["rpm"] as number,
      throttle:   row["throttle"] as number,
      brake:      row["brake"] as number,
      gear:       row["gear"] as number,
      drs:        row["drs"] as boolean,
      x_m:        row["x_m"] as number,
      y_m:        row["y_m"] as number,
      z_m:        row["z_m"] as number,
    });
  }
  await reader.close();

  events.sort((a, b) => a.race_t - b.race_t);

  const grid = new Map<string, number>(meta.grid);
  return { events, raceStartT: meta.raceStartT, grid };
}

export async function writeCachedSession(
  sessionKey: number,
  data: CachedSession,
): Promise<void> {
  const dir = await ensureCacheDir(sessionKey);

  const telemetry: Telemetry[] = [];
  const rest: Event[] = [];
  for (const ev of data.events) {
    if (ev.kind === "telemetry") telemetry.push(ev);
    else rest.push(ev);
  }

  // Write non-telemetry events as JSON.
  await writeFile(join(dir, "events.json"), JSON.stringify(rest), "utf-8");

  // Write telemetry as Parquet (column-oriented; efficient for the ~300k rows).
  const writer = await parquet.ParquetWriter.openFile(TELEM_SCHEMA, join(dir, "telemetry.parquet"));
  for (const t of telemetry) {
    await writer.appendRow({
      race_t:     t.race_t,
      driver_code: t.driver_code,
      speed_kph:  t.speed_kph,
      rpm:        t.rpm,
      throttle:   t.throttle,
      brake:      t.brake,
      gear:       t.gear,
      drs:        t.drs,
      x_m:        t.x_m,
      y_m:        t.y_m,
      z_m:        t.z_m,
    });
  }
  await writer.close();

  const meta: CacheMeta = {
    version:    CACHE_VERSION,
    session_key: sessionKey,
    raceStartT: data.raceStartT,
    grid:       [...data.grid.entries()],
    cachedAt:   new Date().toISOString(),
  };
  await writeFile(join(dir, "meta.json"), JSON.stringify(meta, null, 2), "utf-8");

  log.info(`Cached session ${sessionKey} → ${dir} (${telemetry.length} telemetry rows, ${rest.length} other events)`);
}
