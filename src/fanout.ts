/**
 * Route a mixed event stream into per-driver queues.
 *
 * Session-wide events (driver_code === "*") are duplicated onto every driver's
 * queue with the sentinel rewritten to that driver's code. Each driver then
 * gets the event on their own currently-active span.
 *
 * Result queues are sorted by race_t.
 */

import type { Event } from "./models.js";
import { SESSION_WIDE } from "./models.js";

export function fanOut(events: Event[], driverCodes: string[]): Map<string, Event[]> {
  const out = new Map<string, Event[]>();
  for (const code of driverCodes) out.set(code, []);

  for (const ev of events) {
    if (ev.driver_code === SESSION_WIDE) {
      for (const code of driverCodes) {
        out.get(code)!.push({ ...ev, driver_code: code });
      }
    } else {
      const q = out.get(ev.driver_code);
      if (q) q.push(ev);
      // events for filtered-out drivers are dropped silently
    }
  }

  for (const q of out.values()) q.sort((a, b) => a.race_t - b.race_t);
  return out;
}
