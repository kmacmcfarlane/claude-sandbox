#!/usr/bin/env node
'use strict';

// NDJSON passthrough that wraps each line in a timestamped envelope and writes
// it to a file.  Stdout passthrough remains unwrapped so downstream consumers
// (run-logger, console-output) see the original Claude JSON.
//
// Envelope format (one per line in the output file):
//   {"ts":"2026-03-04T12:00:00.123Z","event":{…original claude json…}}
//
// If a line isn't valid JSON the raw string is stored as the event value.
//
// Sits in the pipeline alongside other filters:
//   claude | raw-json-logger.js --out <path> | ...
//
// Usage:
//   node raw-json-logger.js --out <path>

const fs = require('fs');
const readline = require('readline');

let outFile = null;

for (let i = 2; i < process.argv.length; i++) {
  if (process.argv[i] === '--out' && process.argv[i + 1]) {
    outFile = process.argv[++i];
  }
}

if (!outFile) {
  process.stderr.write('Usage: raw-json-logger.js --out <path>\n');
  process.exit(2);
}

const fd = fs.openSync(outFile, 'w');

// Exit cleanly on EPIPE (downstream pipe closed by exit-on-result.js)
process.stdout.on('error', (err) => {
  if (err.code === 'EPIPE') {
    fs.closeSync(fd);
    process.exit(0);
  }
  throw err;
});

const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  // Pass through unchanged so downstream sees original Claude JSON
  process.stdout.write(line + '\n');

  // Wrap in timestamped envelope for the raw log file
  let event;
  try {
    event = JSON.parse(line);
  } catch {
    event = line;
  }
  const envelope = JSON.stringify({ ts: new Date().toISOString(), event });
  fs.writeSync(fd, envelope + '\n');
});

rl.on('close', () => {
  fs.closeSync(fd);
});
