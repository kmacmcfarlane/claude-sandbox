#!/usr/bin/env node
'use strict';

// Transparent NDJSON passthrough that captures per-iteration metrics from
// Claude Code's stream-json output and writes them to a run log file.
//
// Sits in the pipeline between claude and console-output.js:
//   claude --output-format stream-json | run-logger.js | console-output.js
//
// Usage:
//   node run-logger.js --log-file <path> --iteration <N>

const STORY_RE = /<!-- story: ([A-Za-z]+-\d+)\s*(?:—\s*(.+?))?\s*-->/;

const QUOTA_PATTERNS = [
  { pattern: /usage.limit/i, status: 'quota_exhausted' },
  { pattern: /out of .* usage/i, status: 'quota_exhausted' },
  { pattern: /rate.limit|rate_limit/i, status: 'rate_limit' },
  { pattern: /overloaded/i, status: 'rate_limit' },
];

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

function createState() {
  return {
    storyId: null,
    storyName: null,
    quotaStatus: null,
    taskDispatches: new Map(),
    resultEvent: null,
  };
}

function processEvent(event, state) {
  switch (event.type) {
    case 'system':
      break;

    case 'assistant': {
      const content = (event.message && event.message.content) || [];
      for (const block of content) {
        if (block.type === 'text' && block.text) {
          const m = STORY_RE.exec(block.text);
          if (m) {
            state.storyId = m[1];
            state.storyName = m[2] || null;
          }
          if (!state.quotaStatus) {
            for (const { pattern, status } of QUOTA_PATTERNS) {
              if (pattern.test(block.text)) {
                state.quotaStatus = status;
                break;
              }
            }
          }
        }
        if (block.type === 'tool_use' && block.name === 'Task') {
          const input = block.input || {};
          state.taskDispatches.set(block.id, {
            toolUseId: block.id,
            description: input.description || '',
            subagentType: input.subagent_type || '',
            model: input.model || null,
            durationMs: null,
            tokensIn: null,
            tokensOut: null,
          });
        }
      }
      break;
    }

    case 'user': {
      const content = (event.message && event.message.content) || [];
      const meta = event.tool_use_result || {};
      for (const block of content) {
        if (block.type !== 'tool_result') continue;
        const dispatch = state.taskDispatches.get(block.tool_use_id);
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
      state.resultEvent = event;
      // Check the result text for quota patterns (e.g. "You're out of extra usage")
      if (!state.quotaStatus && event.result) {
        for (const { pattern, status } of QUOTA_PATTERNS) {
          if (pattern.test(event.result)) {
            state.quotaStatus = status;
            break;
          }
        }
      }
      break;
    }
  }
}

function buildLogEntry(state, iteration, startedAt) {
  const usage = (state.resultEvent && state.resultEvent.usage) || {};
  const tokensIn = (usage.input_tokens || 0)
    + (usage.cache_creation_input_tokens || 0)
    + (usage.cache_read_input_tokens || 0);
  const tokensOut = usage.output_tokens || 0;

  return {
    storyId: state.storyId || null,
    storyName: state.storyName || null,
    sessionId: (state.resultEvent && state.resultEvent.session_id) || null,
    startedAt,
    endedAt: new Date().toISOString(),
    iteration,
    duration: formatDuration((state.resultEvent && state.resultEvent.duration_ms) || null),
    turns: (state.resultEvent && state.resultEvent.num_turns) || null,
    tokensIn,
    tokensOut,
    costUsd: (state.resultEvent && state.resultEvent.total_cost_usd) || null,
    subagents: Array.from(state.taskDispatches.values()).map(d => ({
      description: d.description,
      type: d.subagentType,
      model: d.model,
      duration: formatDuration(d.durationMs),
      tokensIn: d.tokensIn,
      tokensOut: d.tokensOut,
    })),
  };
}

// ---------------------------------------------------------------------------
// CLI entry point
// ---------------------------------------------------------------------------
if (require.main === module) {
  const fs = require('fs');
  const readline = require('readline');

  let logFile = null;
  let iteration = null;
  let quotaStatusFile = null;

  for (let i = 2; i < process.argv.length; i++) {
    if (process.argv[i] === '--log-file' && process.argv[i + 1]) {
      logFile = process.argv[++i];
    } else if (process.argv[i] === '--iteration' && process.argv[i + 1]) {
      iteration = parseInt(process.argv[++i], 10);
    } else if (process.argv[i] === '--quota-status-file' && process.argv[i + 1]) {
      quotaStatusFile = process.argv[++i];
    }
  }

  if (!logFile || iteration == null || isNaN(iteration)) {
    process.stderr.write('Usage: run-logger.js --log-file <path> --iteration <N>\n');
    process.exit(2);
  }

  const startedAt = new Date().toISOString();
  const state = createState();
  let logWritten = false;

  function flushLog() {
    if (logWritten) return;
    logWritten = true;

    const entry = buildLogEntry(state, iteration, startedAt);

    try {
      let runs = [];
      try {
        runs = JSON.parse(fs.readFileSync(logFile, 'utf8'));
        if (!Array.isArray(runs)) runs = [];
      } catch { /* start fresh */ }
      if (runs.length === 0) runs.unshift({ startedAt: startedAt, iterations: [] });
      runs[0].iterations.push(entry);
      fs.writeFileSync(logFile, JSON.stringify(runs, null, 2) + '\n');
    } catch (err) {
      process.stderr.write('run-logger: failed to write log: ' + err.message + '\n');
    }

    if (quotaStatusFile) {
      try {
        fs.writeFileSync(quotaStatusFile, (state.quotaStatus || 'ok') + '\n');
      } catch (err) {
        process.stderr.write('run-logger: failed to write quota status: ' + err.message + '\n');
      }
    }
  }

  // Catch SIGINT/SIGTERM so we flush the log before the pipeline is torn down
  process.on('SIGINT', () => {
    flushLog();
    process.exit(130);
  });
  process.on('SIGTERM', () => {
    flushLog();
    process.exit(143);
  });

  // Exit cleanly on EPIPE (downstream pipe closed by exit-on-result.js)
  process.stdout.on('error', (err) => {
    if (err.code === 'EPIPE') {
      flushLog();
      process.exit(0);
    }
    throw err;
  });

  const rl = readline.createInterface({ input: process.stdin, terminal: false });

  rl.on('line', (line) => {
    process.stdout.write(line + '\n');

    let e;
    try { e = JSON.parse(line); } catch { return; }

    processEvent(e, state);

    // Flush immediately on result event so metrics are persisted before
    // downstream exit-on-result.js tears down the pipeline via SIGPIPE.
    if (e.type === 'result') {
      flushLog();
    }
  });

  rl.on('close', () => {
    flushLog();
  });
}

module.exports = { formatDuration, STORY_RE, QUOTA_PATTERNS, createState, processEvent, buildLogEntry };
