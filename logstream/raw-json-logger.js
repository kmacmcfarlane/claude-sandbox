#!/usr/bin/env node
'use strict';

// Transparent NDJSON passthrough that writes every line verbatim to a file.
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

const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  // Pass through unchanged
  process.stdout.write(line + '\n');
  // Write raw line to file
  fs.writeSync(fd, line + '\n');
});

rl.on('close', () => {
  fs.closeSync(fd);
});
