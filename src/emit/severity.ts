/**
 * Log-severity tables for typed events. See the brief for the full mapping.
 */

import { SeverityNumber } from "@opentelemetry/api-logs";
import type {
  FlagColor,
  InvestigationStatus,
  PenaltyType,
} from "../models.js";

export const FLAG_SEVERITY: Record<FlagColor, SeverityNumber> = {
  green: SeverityNumber.INFO,
  chequered: SeverityNumber.INFO,
  white: SeverityNumber.INFO,
  yellow: SeverityNumber.WARN,
  double_yellow: SeverityNumber.WARN,
  sc: SeverityNumber.WARN,
  vsc: SeverityNumber.WARN,
  black_and_white: SeverityNumber.WARN,
  black_and_orange: SeverityNumber.WARN,
  black: SeverityNumber.ERROR,
  red: SeverityNumber.ERROR,
  blue: SeverityNumber.DEBUG,
};

export const PENALTY_SEVERITY: Record<PenaltyType, SeverityNumber> = {
  reprimand: SeverityNumber.INFO,
  "5_second": SeverityNumber.WARN,
  "10_second": SeverityNumber.WARN,
  drive_through: SeverityNumber.WARN,
  stop_go_10: SeverityNumber.WARN,
  grid: SeverityNumber.WARN,
  disqualification: SeverityNumber.ERROR,
};

export const INVESTIGATION_SEVERITY: Record<InvestigationStatus, SeverityNumber> = {
  noted: SeverityNumber.INFO,
  under_investigation: SeverityNumber.INFO,
  no_action: SeverityNumber.DEBUG,
  penalty: SeverityNumber.WARN,
};
