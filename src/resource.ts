/**
 * Build OpenTelemetry Resources.
 *
 * - Session resource: the race itself. service.name = "race". Carries the
 *   session root span AND all metrics (driver split by datapoint attribute).
 * - Driver resource: the team/car. service.name = team, instance.id = code.
 *   Traces and logs only — no metrics.
 *
 * Session-scoped facts (year, round, type) live on the race-resource and
 * the race span. Driver-scoped facts live on the driver resource.
 */

import { resourceFromAttributes, type Resource } from "@opentelemetry/resources";
import { ATTR_SERVICE_NAME, ATTR_SERVICE_VERSION } from "@opentelemetry/semantic-conventions";
import type { DriverInfo, SessionInfo } from "./models.js";
import { BARGEBOARD_VERSION } from "./emit/constants.js";

function instanceId(s: SessionInfo): string {
  return `${s.year}-${s.round_name.toLowerCase().replace(/\s+/g, "-")}-${s.session_type.toLowerCase()}`;
}

export function makeSessionResource(session: SessionInfo): Resource {
  return resourceFromAttributes({
    [ATTR_SERVICE_NAME]: "race",
    "service.namespace": "f1",
    "service.instance.id": instanceId(session),
    [ATTR_SERVICE_VERSION]: BARGEBOARD_VERSION,
    "f1.session.year": session.year,
    "f1.session.round": session.round_name,
    "f1.session.type": session.session_type,
  });
}

export function makeDriverResource(driver: DriverInfo): Resource {
  return resourceFromAttributes({
    [ATTR_SERVICE_NAME]: driver.team,
    "service.namespace": "f1",
    "service.instance.id": driver.code,
    [ATTR_SERVICE_VERSION]: BARGEBOARD_VERSION,
    "f1.driver.code": driver.code,
    "f1.driver.full_name": driver.full_name,
    "f1.car.number": driver.car_number,
  });
}
