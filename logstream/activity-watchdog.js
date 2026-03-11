#!/usr/bin/env node
'use strict';

// Activity watchdog — passes through all NDJSON lines unchanged and exits
// with code 124 if no input is received within the timeout period. This
// prevents stuck iterations from blocking the ralph loop indefinitely.
//
// Usage (in the ralph pipeline):
//   ... | exit-on-result.js | activity-watchdog.js --timeout 15 | console-output.js

const readline = require('readline');

let timeoutMinutes = 15;
const args = process.argv.slice(2);
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--timeout' && i + 1 < args.length) {
    timeoutMinutes = parseFloat(args[i + 1]);
    break;
  }
}

const timeoutMs = timeoutMinutes * 60 * 1000;

let timer = setTimeout(() => {
  process.stderr.write(`[activity-watchdog] No activity for ${timeoutMinutes}m. Killing pipeline.\n`);
  process.exit(124);
}, timeoutMs);

const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  clearTimeout(timer);
  process.stdout.write(line + '\n');
  timer = setTimeout(() => {
    process.stderr.write(`[activity-watchdog] No activity for ${timeoutMinutes}m. Killing pipeline.\n`);
    process.exit(124);
  }, timeoutMs);
});

rl.on('close', () => {
  clearTimeout(timer);
});
