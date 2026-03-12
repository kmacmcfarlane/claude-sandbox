#!/usr/bin/env node
'use strict';

// Activity watchdog — passes through all NDJSON lines unchanged and exits
// with code 124 if no input is received within the timeout period. This
// prevents stuck iterations from blocking the ralph loop indefinitely.
//
// Usage (in the ralph pipeline):
//   ... | exit-on-result.js | activity-watchdog.js --timeout 15 | console-output.js

const fs = require('fs');
const readline = require('readline');

let timeoutMinutes = 15;
let markerFile = null;
const args = process.argv.slice(2);
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--timeout' && i + 1 < args.length) {
    timeoutMinutes = parseFloat(args[++i]);
  } else if (args[i] === '--marker-file' && i + 1 < args.length) {
    markerFile = args[++i];
  }
}

const timeoutMs = timeoutMinutes * 60 * 1000;

function killPipeline() {
  process.stderr.write(`[activity-watchdog] No activity for ${timeoutMinutes}m. Killing pipeline.\n`);
  if (markerFile) {
    try { fs.writeFileSync(markerFile, 'watchdog\n'); } catch {}
  }
  process.exit(124);
}

let timer = setTimeout(killPipeline, timeoutMs);

const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  clearTimeout(timer);
  process.stdout.write(line + '\n');
  timer = setTimeout(killPipeline, timeoutMs);
});

rl.on('close', () => {
  clearTimeout(timer);
  process.exit(0);
});
