/**
 * Pre-populate the Parquet cache for every completed 2026 race.
 *
 * Queries OpenF1 for all 2026 Race sessions whose start time is in the past,
 * then runs loadEvents (with telemetry) for each one so the cache is warm.
 * Already-cached sessions are skipped automatically.
 *
 * Usage:
 *   npx tsx scripts/cache-season.ts [--no-cache] [--year 2026]
 *
 * --no-cache  forces a fresh fetch even if a session is already cached.
 * --year      defaults to 2026.
 */

import { loadDrivers, loadEvents, loadSessionInfo } from "../src/extract.js";
import { getSessions } from "../src/openf1.js";
import { hasCachedSession } from "../src/cache.js";
import { log } from "../src/util.js";
import type { SessionInfo, DriverInfo } from "../src/models.js";

const args = process.argv.slice(2);
const noCache = args.includes("--no-cache");
const yearIdx = args.indexOf("--year");
const year = yearIdx !== -1 ? Number(args[yearIdx + 1]) : 2026;

async function main(): Promise<void> {
  log.info(`Fetching completed ${year} Race sessions from OpenF1...`);
  const sessions = await getSessions({ year, session_name: "Race" });
  const now = new Date();
  const past = sessions
    .filter((s) => new Date(s.date_start) <= now)
    .sort((a, b) => a.date_start.localeCompare(b.date_start));

  if (past.length === 0) {
    log.info("No completed races found.");
    return;
  }
  log.info(`Found ${past.length} completed race(s): ${past.map((s) => s.country_name).join(", ")}`);

  let cached = 0;
  let fetched = 0;
  let unavailable = 0;
  let failed = 0;

  for (const s of past) {
    const label = `${s.year} ${s.country_name} (session_key=${s.session_key})`;

    if (!noCache && await hasCachedSession(s.session_key)) {
      log.info(`[skip] ${label} — already cached`);
      cached++;
      continue;
    }

    log.info(`[fetch] ${label}`);
    try {
      const session: SessionInfo = {
        year: s.year,
        round_name: s.country_name,
        session_type: s.session_name,
        start_time: new Date(s.date_start),
        duration_s: (new Date(s.date_end).getTime() - new Date(s.date_start).getTime()) / 1000,
        session_key: s.session_key,
      };
      const { drivers, numberToCode } = await loadDrivers(s.session_key);
      const { events } = await loadEvents(session, drivers, numberToCode, { noCache });
      if (events.length === 0) {
        log.info(`[skip] ${label} — no data published (cancelled or not yet available)`);
        unavailable++;
        continue;
      }
      log.info(`[done] ${label}`);
      fetched++;
    } catch (e) {
      log.warn(`[fail] ${label}: ${(e as Error).message}`);
      failed++;
    }
  }

  log.info(`\nDone. ${fetched} fetched, ${cached} already cached, ${unavailable} unavailable, ${failed} failed.`);
}

main().catch((e) => { console.error(e); process.exit(1); });
