'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { formatTokens, renderEvent } = require('../logstream/stream-filter');

describe('formatTokens', () => {
  it('returns null for null/undefined', () => {
    assert.equal(formatTokens(null), null);
    assert.equal(formatTokens(undefined), null);
  });

  it('formats small numbers as-is', () => {
    assert.equal(formatTokens(0), '0');
    assert.equal(formatTokens(999), '999');
  });

  it('formats thousands with k suffix', () => {
    assert.equal(formatTokens(1000), '1.0k');
    assert.equal(formatTokens(1500), '1.5k');
    assert.equal(formatTokens(12345), '12.3k');
  });
});

describe('renderEvent', () => {
  it('renders assistant text blocks', () => {
    const toolNames = {};
    const lines = renderEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Hello world' }] },
    }, toolNames);
    assert.deepEqual(lines, ['Hello world']);
  });

  it('renders tool_use with file_path summary', () => {
    const toolNames = {};
    const lines = renderEvent({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use', name: 'Read', id: 'tu_1',
          input: { file_path: '/foo/bar.js' },
        }],
      },
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('Read'));
    assert.ok(lines[0].includes('/foo/bar.js'));
    assert.equal(toolNames['tu_1'], 'Read');
  });

  it('renders Task tool_use with subagent type', () => {
    const toolNames = {};
    const lines = renderEvent({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use', name: 'Task', id: 'tu_2',
          input: { subagent_type: 'Explore', description: 'find files' },
        }],
      },
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('(Explore)'));
    assert.ok(lines[0].includes('find files'));
  });

  it('renders tool_use with edit fallback', () => {
    const toolNames = {};
    const lines = renderEvent({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use', name: 'Edit', id: 'tu_3',
          input: { old_string: 'foo', new_string: 'bar' },
        }],
      },
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('(edit)'));
  });

  it('renders user tool_result with file metadata', () => {
    const toolNames = { tu_1: 'Read' };
    const lines = renderEvent({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1' }] },
      tool_use_result: { file: { totalLines: 42 } },
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('Read'));
    assert.ok(lines[0].includes('42 lines'));
  });

  it('renders user tool_result with duration', () => {
    const toolNames = { tu_1: 'Bash' };
    const lines = renderEvent({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1' }] },
      tool_use_result: { durationSeconds: 3.5 },
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('3.5s'));
  });

  it('renders user tool_result with string content fallback', () => {
    const toolNames = { tu_1: 'Grep' };
    const lines = renderEvent({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1', content: '5 matches' }] },
      tool_use_result: {},
    }, toolNames);
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('5 matches'));
  });

  it('renders Task result with multiline output dimmed', () => {
    const toolNames = { tu_1: 'Task' };
    const lines = renderEvent({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1', content: 'line1\nline2' }] },
      tool_use_result: {},
    }, toolNames);
    assert.equal(lines.length, 2);
    assert.ok(lines[1].includes('line1'));
    assert.ok(lines[1].includes('line2'));
  });

  it('renders result event with stats', () => {
    const lines = renderEvent({
      type: 'result',
      num_turns: 10,
      duration_ms: 30000,
      total_cost_usd: 0.1234,
      usage: { input_tokens: 5000, cache_read_input_tokens: 1000, cache_creation_input_tokens: 500, output_tokens: 2000 },
    }, {});
    assert.equal(lines.length, 1);
    assert.ok(lines[0].includes('10 turns'));
    assert.ok(lines[0].includes('30.0s'));
    assert.ok(lines[0].includes('$0.1234'));
    assert.ok(lines[0].includes('in:6.5k'));
    assert.ok(lines[0].includes('out:2.0k'));
  });

  it('returns empty array for system events', () => {
    const lines = renderEvent({ type: 'system' }, {});
    assert.deepEqual(lines, []);
  });

  it('returns empty array for unknown events', () => {
    const lines = renderEvent({ type: 'unknown' }, {});
    assert.deepEqual(lines, []);
  });
});
