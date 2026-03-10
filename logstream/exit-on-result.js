#!/usr/bin/env node
'use strict';

// Pipeline terminator — passes through all NDJSON lines unchanged and exits
// the process when a "result" event is seen. This tears down the pipeline
// via SIGPIPE, which is necessary because Claude Code's process may not exit
// on its own after emitting the result event (internal keep-alive timers).
//
// Placed after run-logger so metrics are flushed before the pipe breaks.
// console-output sits downstream and may miss the final stats line, which is
// acceptable since run-logger already captures all metrics to runlog.json.
//
// Usage (in the ralph pipeline):
//   ... | run-logger.js | exit-on-result.js | console-output.js

const readline = require('readline');

const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  process.stdout.write(line + '\n');

  let e;
  try { e = JSON.parse(line); } catch { return; }

  if (e.type === 'result') {
    process.stderr.write('[exit-on-result] Conversation ended, exiting pipeline.\n');
    process.exit(0);
  }
});
