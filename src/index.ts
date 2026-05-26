#!/usr/bin/env node
/**
 * bargeboard entry point. Boots the commander program.
 */
import { buildProgram } from "./cli.js";

const program = buildProgram();
program.parseAsync(process.argv).catch((err: Error) => {
  process.stderr.write(`[fatal] ${err.message}\n`);
  process.exit(1);
});
