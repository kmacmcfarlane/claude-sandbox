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
const path = require('path');
const os = require('os');
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
let sessionId = null;
let cwd = null;
const startedAt = new Date().toISOString();
let storyId = null;
let storyName = null;
const taskDispatches = new Map(); // keyed by tool_use id
let resultEvent = null;

const STORY_RE = /<!-- story: ([A-Za-z]+-\d+)\s*(?:—\s*(.+?))?\s*-->/;

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
    case 'system': {
      if (e.subtype === 'init') {
        sessionId = e.session_id || null;
        cwd = e.cwd || null;
      }
      break;
    }

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
            prompt: input.prompt || '',
            subagentType: input.subagent_type || '',
            durationSeconds: null,
            tokensIn: null,
            tokensOut: null,
            turns: null,
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
        if (dispatch && meta.durationSeconds != null) {
          dispatch.durationSeconds = meta.durationSeconds;
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
// Post-stream: enrich subagent metrics from stored JSONL, then write log
// ---------------------------------------------------------------------------
rl.on('close', () => {
  // Small delay to let Claude Code finish writing subagent JSONL files
  setTimeout(() => {
    enrichSubagents();
    writeLog();
  }, 500);
});

function enrichSubagents() {
  if (!sessionId || !cwd || taskDispatches.size === 0) return;

  const encodedCwd = cwd.replace(/\//g, '-');
  const subagentsDir = path.join(
    os.homedir(), '.claude', 'projects', encodedCwd, sessionId, 'subagents'
  );

  let files;
  try {
    files = fs.readdirSync(subagentsDir).filter(f => f.startsWith('agent-') && f.endsWith('.jsonl'));
  } catch (err) {
    process.stderr.write('run-logger: subagent enrichment skipped: ' + subagentsDir + ' — ' + err.code + '\n');
    return;
  }

  const subagentData = [];

  for (const file of files) {
    const filePath = path.join(subagentsDir, file);
    let lines;
    try {
      lines = fs.readFileSync(filePath, 'utf8').trim().split('\n');
    } catch { continue; }

    let firstUserText = '';
    let tokensIn = 0;
    let tokensOut = 0;
    let turns = 0;

    for (const raw of lines) {
      let ev;
      try { ev = JSON.parse(raw); } catch { continue; }

      if (ev.type === 'user' && !firstUserText) {
        const content = (ev.message && ev.message.content) || '';
        if (typeof content === 'string') {
          firstUserText = content;
        } else {
          for (const block of content) {
            if (block.type === 'text' && block.text) {
              firstUserText = block.text;
              break;
            }
          }
        }
      }

      if (ev.type === 'assistant' && ev.message && ev.message.usage) {
        const u = ev.message.usage;
        tokensIn += (u.input_tokens || 0)
          + (u.cache_creation_input_tokens || 0)
          + (u.cache_read_input_tokens || 0);
        tokensOut += (u.output_tokens || 0);
        turns++;
      }
    }

    subagentData.push({ firstUserText, tokensIn, tokensOut, turns });
  }

  // Match subagent data to dispatches by prompt content overlap
  const unmatchedData = [...subagentData];
  for (const dispatch of taskDispatches.values()) {
    // The subagent's first user message is the prompt sent by the Task tool.
    // Match by checking if the stored prompt appears in the subagent's first
    // user text, or vice versa (the stored JSONL may wrap the prompt).
    const idx = unmatchedData.findIndex(d => {
      if (!d.firstUserText) return false;
      if (dispatch.prompt && d.firstUserText.includes(dispatch.prompt)) return true;
      if (dispatch.prompt && dispatch.prompt.includes(d.firstUserText)) return true;
      // Fall back to description match
      if (dispatch.description && d.firstUserText.includes(dispatch.description)) return true;
      return false;
    });
    if (idx !== -1) {
      const matched = unmatchedData.splice(idx, 1)[0];
      dispatch.tokensIn = matched.tokensIn;
      dispatch.tokensOut = matched.tokensOut;
      dispatch.turns = matched.turns;
    }
  }

  // Fall back to order-based matching for remaining unmatched pairs
  const unmatchedDispatches = [...taskDispatches.values()].filter(d => d.tokensIn == null);
  for (let i = 0; i < Math.min(unmatchedDispatches.length, unmatchedData.length); i++) {
    unmatchedDispatches[i].tokensIn = unmatchedData[i].tokensIn;
    unmatchedDispatches[i].tokensOut = unmatchedData[i].tokensOut;
    unmatchedDispatches[i].turns = unmatchedData[i].turns;
  }
}

function writeLog() {
  const usage = (resultEvent && resultEvent.usage) || {};
  const tokensIn = (usage.input_tokens || 0)
    + (usage.cache_creation_input_tokens || 0)
    + (usage.cache_read_input_tokens || 0);
  const tokensOut = usage.output_tokens || 0;

  const entry = {
    storyId: storyId || null,
    storyName: storyName || null,
    sessionId: sessionId || null,
    startedAt,
    endedAt: new Date().toISOString(),
    iteration,
    durationMs: (resultEvent && resultEvent.duration_ms) || null,
    turns: (resultEvent && resultEvent.num_turns) || null,
    tokensIn,
    tokensOut,
    costUsd: (resultEvent && resultEvent.total_cost_usd) || null,
    subagents: Array.from(taskDispatches.values()).map(d => ({
      description: d.description,
      type: d.subagentType,
      durationSeconds: d.durationSeconds,
      tokensIn: d.tokensIn,
      tokensOut: d.tokensOut,
      turns: d.turns,
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
