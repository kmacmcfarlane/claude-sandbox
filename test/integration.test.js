'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const ROOT = path.resolve(__dirname, '..');

describe('run-logger.js CLI', () => {
  it('passes through NDJSON and writes log file', () => {
    const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'run-logger-test-'));
    const logFile = path.join(tmpDir, 'run.json');

    const input = [
      JSON.stringify({ type: 'system' }),
      JSON.stringify({
        type: 'assistant',
        message: { content: [{ type: 'text', text: '<!-- story: TEST-1 — Integration test -->' }] },
      }),
      JSON.stringify({
        type: 'result',
        session_id: 'sess_int',
        num_turns: 2,
        duration_ms: 5000,
        total_cost_usd: 0.01,
        usage: { input_tokens: 100, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 50 },
      }),
    ].join('\n') + '\n';

    const stdout = execFileSync(
      process.execPath,
      [path.join(ROOT, 'lib/run-logger.js'), '--log-file', logFile, '--iteration', '1'],
      { input, encoding: 'utf8' },
    );

    // Verify passthrough: stdout should contain all input lines
    assert.equal(stdout, input);

    // Verify log file was written
    const runs = JSON.parse(fs.readFileSync(logFile, 'utf8'));
    assert.ok(Array.isArray(runs));
    assert.equal(runs[0].iterations.length, 1);
    const entry = runs[0].iterations[0];
    assert.equal(entry.storyId, 'TEST-1');
    assert.equal(entry.storyName, 'Integration test');
    assert.equal(entry.sessionId, 'sess_int');
    assert.equal(entry.iteration, 1);
    assert.equal(entry.duration, '5s');

    // Cleanup
    fs.rmSync(tmpDir, { recursive: true });
  });

  it('exits with code 2 on missing arguments', () => {
    assert.throws(
      () => execFileSync(process.execPath, [path.join(ROOT, 'lib/run-logger.js')], { encoding: 'utf8' }),
      (err) => err.status === 2,
    );
  });
});

describe('stream-filter.js CLI', () => {
  it('renders NDJSON events to human-readable output', () => {
    const input = [
      JSON.stringify({ type: 'system' }),
      JSON.stringify({
        type: 'assistant',
        message: { content: [{ type: 'text', text: 'Hello from Claude' }] },
      }),
      JSON.stringify({
        type: 'assistant',
        message: {
          content: [{
            type: 'tool_use', name: 'Read', id: 'tu_1',
            input: { file_path: '/tmp/test.js' },
          }],
        },
      }),
      JSON.stringify({
        type: 'user',
        message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1' }] },
        tool_use_result: { file: { totalLines: 100 } },
      }),
      JSON.stringify({
        type: 'result',
        num_turns: 1,
        duration_ms: 2000,
        total_cost_usd: 0.005,
        usage: { input_tokens: 500, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 200 },
      }),
    ].join('\n') + '\n';

    const stdout = execFileSync(
      process.execPath,
      [path.join(ROOT, 'lib/stream-filter.js')],
      { input, encoding: 'utf8' },
    );

    assert.ok(stdout.includes('Hello from Claude'));
    assert.ok(stdout.includes('Read'));
    assert.ok(stdout.includes('/tmp/test.js'));
    assert.ok(stdout.includes('100 lines'));
    assert.ok(stdout.includes('1 turns'));
    assert.ok(stdout.includes('$0.0050'));
  });

  it('handles invalid JSON lines gracefully', () => {
    const input = 'not json\n{"type":"system"}\n';
    const stdout = execFileSync(
      process.execPath,
      [path.join(ROOT, 'lib/stream-filter.js')],
      { input, encoding: 'utf8' },
    );
    // Should not crash — system events produce no output
    assert.equal(stdout, '');
  });
});
