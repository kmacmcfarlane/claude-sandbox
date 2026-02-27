#!/usr/bin/env node
'use strict';

// Transparent NDJSON passthrough that captures per-iteration metrics from
// Claude Code's stream-json output and writes them to a run log file.
//
// Sits in the pipeline between claude and stream-filter.js:
//   claude --output-format stream-json | run-logger.js | stream-filter.js
//
// Usage:
//   node run-logger.js --log-file <path> --iteration <N>

const fs = require('fs');
const readline = require('readline');

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------
let logFile = null;
let iteration = null;

for (let i = 2; i < process.argv.length; i++) {
  if (process.argv[i] === '--log-file' && process.argv[i + 1]) {
    logFile = process.argv[++i];
  } else if (process.argv[i] === '--iteration' && process.argv[i + 1]) {
    iteration = parseInt(process.argv[++i], 10);
  }
}

if (!logFile || iteration == null || isNaN(iteration)) {
  process.stderr.write('Usage: run-logger.js --log-file <path> --iteration <N>\n');
  process.exit(2);
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
const startedAt = new Date().toISOString();
let storyId = null;
let storyName = null;
const taskDispatches = new Map(); // keyed by tool_use id
let resultEvent = null;

const STORY_RE = /<!-- story: ([A-Za-z]+-\d+)\s*(?:—\s*(.+?))?\s*-->/;

function formatDuration(ms) {
  if (ms == null) return null;
  const totalSeconds = Math.round(ms / 1000);
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const parts = [];
  if (h > 0) parts.push(h + 'h');
  if (m > 0) parts.push(m + 'm');
  if (s > 0 || parts.length === 0) parts.push(s + 's');
  return parts.join('');
}

// ---------------------------------------------------------------------------
// NDJSON line processing (passthrough + metric capture)
// ---------------------------------------------------------------------------
const rl = readline.createInterface({ input: process.stdin, terminal: false });

rl.on('line', (line) => {
  // Always pass through unchanged
  process.stdout.write(line + '\n');

  let e;
  try { e = JSON.parse(line); } catch { return; }

  switch (e.type) {
    case 'system':
      break;

    case 'assistant': {
      const content = (e.message && e.message.content) || [];
      for (const block of content) {
        if (block.type === 'text' && block.text && !storyId) {
          const m = STORY_RE.exec(block.text);
          if (m) {
            storyId = m[1];
            storyName = m[2] || null;
          }
        }
        if (block.type === 'tool_use' && block.name === 'Task') {
          const input = block.input || {};
          taskDispatches.set(block.id, {
            toolUseId: block.id,
            description: input.description || '',
            subagentType: input.subagent_type || '',
            durationMs: null,
            tokensIn: null,
            tokensOut: null,
          });
        }
      }
      break;
    }

    case 'user': {
      const content = (e.message && e.message.content) || [];
      const meta = e.tool_use_result || {};
      for (const block of content) {
        if (block.type !== 'tool_result') continue;
        const dispatch = taskDispatches.get(block.tool_use_id);
        if (!dispatch) continue;
        if (meta.totalDurationMs != null) {
          dispatch.durationMs = meta.totalDurationMs;
        }
        if (meta.usage) {
          const u = meta.usage;
          dispatch.tokensIn = (u.input_tokens || 0)
            + (u.cache_creation_input_tokens || 0)
            + (u.cache_read_input_tokens || 0);
          dispatch.tokensOut = u.output_tokens || 0;
        }
      }
      break;
    }

    case 'result': {
      resultEvent = e;
      break;
    }
  }
});

// ---------------------------------------------------------------------------
// Post-stream: write log
// ---------------------------------------------------------------------------
rl.on('close', () => {
  writeLog();
});

function writeLog() {
  const usage = (resultEvent && resultEvent.usage) || {};
  const tokensIn = (usage.input_tokens || 0)
    + (usage.cache_creation_input_tokens || 0)
    + (usage.cache_read_input_tokens || 0);
  const tokensOut = usage.output_tokens || 0;

  const entry = {
    storyId: storyId || null,
    storyName: storyName || null,
    sessionId: (resultEvent && resultEvent.session_id) || null,
    startedAt,
    endedAt: new Date().toISOString(),
    iteration,
    duration: formatDuration((resultEvent && resultEvent.duration_ms) || null),
    turns: (resultEvent && resultEvent.num_turns) || null,
    tokensIn,
    tokensOut,
    costUsd: (resultEvent && resultEvent.total_cost_usd) || null,
    subagents: Array.from(taskDispatches.values()).map(d => ({
      description: d.description,
      type: d.subagentType,
      duration: formatDuration(d.durationMs),
      tokensIn: d.tokensIn,
      tokensOut: d.tokensOut,
    })),
  };

  try {
    let runs = [];
    try {
      runs = JSON.parse(fs.readFileSync(logFile, 'utf8'));
      if (!Array.isArray(runs)) runs = [];
    } catch { /* start fresh */ }
    // Append to the current (first) run's iterations array
    if (runs.length === 0) runs.unshift({ startedAt: startedAt, iterations: [] });
    runs[0].iterations.push(entry);
    fs.writeFileSync(logFile, JSON.stringify(runs, null, 2) + '\n');
  } catch (err) {
    process.stderr.write('run-logger: failed to write log: ' + err.message + '\n');
  }
}
