/**
 * Route a mixed event stream into a session-phase queue plus per-driver queues.
 *
 * Splits at `raceStartT` (lights-out):
 *  - Events with race_t < raceStartT land on the formation queue — one entry
 *    per event (no fan-out), processed by the session interpreter as span
 *    events on the formation span.
 *  - Events with race_t >= raceStartT route per driver as before: session-wide
 *    events ("*") duplicate onto every driver's queue with the sentinel
 *    replaced; driver-specific events go to that driver.
 *
 * All result queues sorted by race_t.
 */

import type { Event } from "./models.js";
import { SESSION_WIDE } from "./models.js";

export interface FannedOut {
  /** Pre-race events to drive the formation phase (single list, no fan-out). */
  formation: Event[];
  /** Race-phase events split per driver, with session-wide events duplicated. */
  perDriver: Map<string, Event[]>;
}

export function fanOut(
  events: Event[],
  driverCodes: string[],
  raceStartT: number,
): FannedOut {
  const formation: Event[] = [];
  const perDriver = new Map<string, Event[]>();
  for (const code of driverCodes) perDriver.set(code, []);

  for (const ev of events) {
    if (ev.race_t < raceStartT) {
      // Pre-race. Only event kinds the formation handler actually consumes
      // get queued — pre-race telemetry / lap_starts (if any leak through)
      // are not part of the formation story we want to tell. One copy each;
      // no per-driver duplication.
      if (isFormationEvent(ev)) formation.push(ev);
      continue;
    }
    if (ev.driver_code === SESSION_WIDE) {
      for (const code of driverCodes) {
        perDriver.get(code)!.push({ ...ev, driver_code: code });
      }
    } else {
      const q = perDriver.get(ev.driver_code);
      if (q) q.push(ev);
      // events for filtered-out drivers are dropped silently
    }
  }

  formation.sort((a, b) => a.race_t - b.race_t);
  for (const q of perDriver.values()) q.sort((a, b) => a.race_t - b.race_t);
  return { formation, perDriver };
}

/** Event kinds that make sense pre-race and have a handler in handleFormation.
 *  Mirrors the cases in `handleFormation` — keep in sync. */
function isFormationEvent(ev: Event): boolean {
  switch (ev.kind) {
    case "race_control":
    case "flag":
    case "penalty":
    case "investigation":
      return true;
    default:
      return false;
  }
}
