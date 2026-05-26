/**
 * Bargeboard instrumentation-scope identity.
 *
 * One scope, one name, one version, used across all signal types (traces,
 * metrics, logs). Pinned here so the providers and the emit handlers agree.
 */
export const SCOPE_NAME = "bargeboard";
export const BARGEBOARD_VERSION = "0.2.0";
export const SCOPE_VERSION = BARGEBOARD_VERSION;
