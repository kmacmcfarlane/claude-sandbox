#!/usr/bin/env node
'use strict';

// Filters Claude Code stream-json (NDJSON) into human-readable terminal output.
//
// Claude Code --output-format stream-json emits whole-message events:
//   system    — session init
//   assistant — .message.content[] with text and tool_use blocks
//   user      — .message.content[] with tool_result blocks
//   result    — final summary with cost and duration

const CYAN = '\x1b[36m';
const DIM = '\x1b[2m';
const GREEN = '\x1b[32m';
const RESET = '\x1b[0m';

function formatTokens(n) {
  if (n == null) return null;
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
  return String(n);
}

function renderEvent(event, toolNames) {
  const lines = [];

  switch (event.type) {
    case 'assistant': {
      const content = (event.message && event.message.content) || [];
      for (const block of content) {
        if (block.type === 'text' && block.text) {
          lines.push(block.text);
        } else if (block.type === 'tool_use') {
          const input = block.input || {};
          let summary;
          if (block.name === 'Task') {
            const agentType = input.subagent_type ? '(' + input.subagent_type + ') ' : '';
            summary = agentType + (input.description || '');
          } else {
            const target = input.file_path || input.command || input.pattern
              || input.query || input.prompt || input.url || '';
            summary = target || (input.old_string ? '(edit)' : JSON.stringify(input).slice(0, 120));
          }
          lines.push('  ' + CYAN + '\u2192 ' + block.name + RESET + ' ' + summary);
          if (block.id) toolNames[block.id] = block.name;
        }
      }
      break;
    }

    case 'user': {
      const content = (event.message && event.message.content) || [];
      const meta = event.tool_use_result || {};
      for (const block of content) {
        if (block.type !== 'tool_result') continue;
        const name = toolNames[block.tool_use_id] || 'tool';
        let summary = '';
        if (meta.file) {
          summary = meta.file.totalLines + ' lines';
        } else if (meta.durationSeconds != null) {
          summary = meta.durationSeconds.toFixed(1) + 's';
        } else if (typeof block.content === 'string') {
          summary = block.content;
        }
        if (name === 'Task' && summary.includes('\n')) {
          const indented = summary.split('\n').map(l => '    ' + l).join('\n');
          lines.push('  ' + GREEN + '\u2190 ' + name + RESET + ' ');
          lines.push(DIM + indented + RESET);
        } else {
          lines.push('  ' + GREEN + '\u2190 ' + name + RESET + ' ' + summary);
        }
      }
      break;
    }

    case 'result': {
      const parts = [];
      if (event.num_turns != null) parts.push(event.num_turns + ' turns');
      if (event.duration_ms != null) parts.push((event.duration_ms / 1000).toFixed(1) + 's');
      const u = event.usage || {};
      const tokParts = [];
      const inTok = formatTokens((u.input_tokens || 0) + (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0));
      if (inTok) tokParts.push('in:' + inTok);
      const outTok = formatTokens(u.output_tokens);
      if (outTok) tokParts.push('out:' + outTok);
      if (tokParts.length) parts.push(tokParts.join(' '));
      if (event.total_cost_usd != null) parts.push('$' + event.total_cost_usd.toFixed(4));
      if (parts.length) {
        lines.push(DIM + '  [' + parts.join(' | ') + ']' + RESET);
      }
      break;
    }

    // system — skip silently
  }

  return lines;
}

// ---------------------------------------------------------------------------
// CLI entry point
// ---------------------------------------------------------------------------
if (require.main === module) {
  const readline = require('readline');
  const toolNames = {};
  const rl = readline.createInterface({ input: process.stdin, terminal: false });

  const MAX_LINES = 5;
  const MAX_LINE_LEN = 2048;

  rl.on('line', (line) => {
    let e;
    try { e = JSON.parse(line); } catch { return; }

    const lines = renderEvent(e, toolNames);
    const capped = lines.length > MAX_LINES;
    const visible = capped ? lines.slice(0, MAX_LINES) : lines;
    for (const l of visible) {
      process.stdout.write((l.length > MAX_LINE_LEN ? l.slice(0, MAX_LINE_LEN) + '…' : l) + '\n');
    }
    if (capped) {
      process.stdout.write(DIM + '  … ' + (lines.length - MAX_LINES) + ' more lines' + RESET + '\n');
    }
  });
}

module.exports = { formatTokens, renderEvent };
