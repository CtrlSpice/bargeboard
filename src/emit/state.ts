/**
 * Per-driver and per-session emitter state.
 *
 * Pure data. Handlers in `handlers.ts` take state in and return new state +
 * effects. The interpreter in `effects.ts` translates effects against the
 * OTel SDK and the SpanRegistry (which holds the actual `Span` objects).
 *
 * We use string IDs for spans (e.g. "race", "lap", "sector", "pit") rather
 * than carrying live Span references in the immutable state — handlers stay
 * pure, while the interpreter does the bookkeeping.
 */

export interface DriverState {
  code: string;
  retired: boolean;
  // The most-specific currently-open span ID, in priority order. Used by
  // span-event / log handlers to find their parent. Updated by the interpreter
  // whenever a StartSpan/EndSpan effect fires.
  raceSpanId: string | null;
  lapSpanId: string | null;
  sectorSpanId: string | null;
  pitSpanId: string | null;
  // The lap/sector counts and timing anchors live here. The interpreter never
  // mutates them; handlers do.
  lapNumber: number;
  sectorNumber: 0 | 1 | 2 | 3;
  lapStartT: number | null;       // race_t when current lap opened
  sectorStartT: number | null;
  pitEntryT: number | null;
  lapTopSpeed: number;            // max kph seen during current lap
  // Dedupes: only count investigations at UNDER_INVESTIGATION.
}

export interface SessionState {
  rootSpanId: string | null;
  carsOnTrack: number;
}

export function initialDriverState(code: string): DriverState {
  return {
    code,
    retired: false,
    raceSpanId: null,
    lapSpanId: null,
    sectorSpanId: null,
    pitSpanId: null,
    lapNumber: 0,
    sectorNumber: 0,
    lapStartT: null,
    sectorStartT: null,
    pitEntryT: null,
    lapTopSpeed: 0,
  };
}

export function initialSessionState(): SessionState {
  return { rootSpanId: null, carsOnTrack: 0 };
}
