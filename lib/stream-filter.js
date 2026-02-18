#!/usr/bin/env node
'use strict';

// Filters Claude Code stream-json (NDJSON) into human-readable terminal output.
//
// Claude Code --output-format stream-json emits whole-message events:
//   system    — session init
//   assistant — .message.content[] with text and tool_use blocks
//   user      — .message.content[] with tool_result blocks
//   result    — final summary with cost and duration

const readline = require('readline');

const CYAN = '\x1b[36m';
const DIM = '\x1b[2m';
const GREEN = '\x1b[32m';
const RESET = '\x1b[0m';

// Track tool names by id so we can label results
const toolNames = {};

const rl = readline.createInterface({ input: process.stdin, terminal: false });

function formatTokens(n) {
  if (n == null) return null;
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

rl.on('line', (line) => {
  let e;
  try { e = JSON.parse(line); } catch { return; }

  switch (e.type) {
    case 'assistant': {
      const content = (e.message && e.message.content) || [];
      for (const block of content) {
        if (block.type === 'text' && block.text) {
          process.stdout.write(block.text + '\n');
        } else if (block.type === 'tool_use') {
          const input = block.input || {};
          const target = input.file_path || input.command || input.pattern
            || input.query || input.prompt || input.url || '';
          const summary = target || (input.old_string ? '(edit)' : JSON.stringify(input).slice(0, 120));
          process.stdout.write('  ' + CYAN + '\u2192 ' + block.name + RESET + ' ' + summary + '\n');
          if (block.id) toolNames[block.id] = block.name;
        }
      }
      break;
    }

    case 'user': {
      const content = (e.message && e.message.content) || [];
      const meta = e.tool_use_result || {};
      for (const block of content) {
        if (block.type !== 'tool_result') continue;
        const name = toolNames[block.tool_use_id] || 'tool';
        // Build a short summary of the result
        let summary = '';
        if (meta.file) {
          summary = meta.file.totalLines + ' lines';
        } else if (meta.durationSeconds != null) {
          summary = meta.durationSeconds.toFixed(1) + 's';
        } else if (typeof block.content === 'string') {
          const first = block.content.split('\n')[0];
          summary = first.length > 100 ? first.slice(0, 100) + '\u2026' : first;
        }
        process.stdout.write('  ' + GREEN + '\u2190 ' + name + RESET + ' ' + summary + '\n');
      }
      break;
    }

    case 'result': {
      const parts = [];
      if (e.num_turns != null) parts.push(e.num_turns + ' turns');
      if (e.duration_ms != null) parts.push((e.duration_ms / 1000).toFixed(1) + 's');
      const u = e.usage || {};
      const tokParts = [];
      const inTok = formatTokens((u.input_tokens || 0) + (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0));
      if (inTok) tokParts.push('in:' + inTok);
      const outTok = formatTokens(u.output_tokens);
      if (outTok) tokParts.push('out:' + outTok);
      if (tokParts.length) parts.push(tokParts.join(' '));
      if (e.total_cost_usd != null) parts.push('$' + e.total_cost_usd.toFixed(4));
      if (parts.length) {
        process.stdout.write(DIM + '  [' + parts.join(' | ') + ']' + RESET + '\n');
      }
      break;
    }

    // system — skip silently
  }
});
