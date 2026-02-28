'use strict';

const { describe, it } = require('node:test');
const assert = require('node:assert/strict');
const { formatDuration, STORY_RE, QUOTA_PATTERNS, createState, processEvent, buildLogEntry } = require('../logstream/run-logger');

describe('formatDuration', () => {
  it('returns null for null/undefined', () => {
    assert.equal(formatDuration(null), null);
    assert.equal(formatDuration(undefined), null);
  });

  it('formats zero milliseconds', () => {
    assert.equal(formatDuration(0), '0s');
  });

  it('formats seconds only', () => {
    assert.equal(formatDuration(45000), '45s');
  });

  it('formats minutes and seconds', () => {
    assert.equal(formatDuration(125000), '2m5s');
  });

  it('formats hours, minutes, seconds', () => {
    assert.equal(formatDuration(3661000), '1h1m1s');
  });

  it('omits zero components in the middle', () => {
    assert.equal(formatDuration(3600000), '1h');
    assert.equal(formatDuration(60000), '1m');
  });

  it('rounds to nearest second', () => {
    assert.equal(formatDuration(1499), '1s');
    assert.equal(formatDuration(1500), '2s');
  });
});

describe('STORY_RE', () => {
  it('matches story ID with name', () => {
    const m = STORY_RE.exec('<!-- story: PROJ-123 — My story name -->');
    assert.equal(m[1], 'PROJ-123');
    assert.equal(m[2], 'My story name');
  });

  it('matches story ID without name', () => {
    const m = STORY_RE.exec('<!-- story: ABC-1 -->');
    assert.equal(m[1], 'ABC-1');
    assert.equal(m[2], undefined);
  });

  it('does not match invalid format', () => {
    assert.equal(STORY_RE.exec('<!-- story: 123 -->'), null);
    assert.equal(STORY_RE.exec('not a story marker'), null);
  });
});

describe('processEvent', () => {
  it('extracts story ID from assistant text', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: '<!-- story: FEAT-42 — Build widget -->' }] },
    }, state);
    assert.equal(state.storyId, 'FEAT-42');
    assert.equal(state.storyName, 'Build widget');
  });

  it('only captures first story marker', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: '<!-- story: FEAT-1 — First -->' }] },
    }, state);
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: '<!-- story: FEAT-2 — Second -->' }] },
    }, state);
    assert.equal(state.storyId, 'FEAT-1');
  });

  it('tracks Task tool_use dispatches', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use', name: 'Task', id: 'tu_1',
          input: { description: 'research', subagent_type: 'Explore', model: 'haiku' },
        }],
      },
    }, state);
    assert.equal(state.taskDispatches.size, 1);
    const d = state.taskDispatches.get('tu_1');
    assert.equal(d.description, 'research');
    assert.equal(d.subagentType, 'Explore');
    assert.equal(d.model, 'haiku');
  });

  it('defaults model to null when not specified', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: {
        content: [{
          type: 'tool_use', name: 'Task', id: 'tu_1',
          input: { description: 'research', subagent_type: 'Explore' },
        }],
      },
    }, state);
    assert.equal(state.taskDispatches.get('tu_1').model, null);
  });

  it('ignores non-Task tool_use blocks', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'tool_use', name: 'Read', id: 'tu_2', input: {} }] },
    }, state);
    assert.equal(state.taskDispatches.size, 0);
  });

  it('updates dispatch metrics from user tool_result', () => {
    const state = createState();
    state.taskDispatches.set('tu_1', {
      toolUseId: 'tu_1', description: '', subagentType: '',
      durationMs: null, tokensIn: null, tokensOut: null,
    });
    processEvent({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'tu_1' }] },
      tool_use_result: {
        totalDurationMs: 5000,
        usage: { input_tokens: 100, cache_creation_input_tokens: 20, cache_read_input_tokens: 30, output_tokens: 50 },
      },
    }, state);
    const d = state.taskDispatches.get('tu_1');
    assert.equal(d.durationMs, 5000);
    assert.equal(d.tokensIn, 150);
    assert.equal(d.tokensOut, 50);
  });

  it('captures result event', () => {
    const state = createState();
    const result = { type: 'result', session_id: 'sess_1', num_turns: 5 };
    processEvent(result, state);
    assert.deepEqual(state.resultEvent, result);
  });

  it('handles system events without error', () => {
    const state = createState();
    processEvent({ type: 'system' }, state);
    assert.equal(state.storyId, null);
  });
});

describe('buildLogEntry', () => {
  it('builds a complete log entry', () => {
    const state = createState();
    state.storyId = 'PROJ-1';
    state.storyName = 'Test';
    state.resultEvent = {
      session_id: 'sess_1',
      duration_ms: 60000,
      num_turns: 3,
      total_cost_usd: 0.05,
      usage: { input_tokens: 1000, cache_creation_input_tokens: 0, cache_read_input_tokens: 200, output_tokens: 500 },
    };
    state.taskDispatches.set('tu_1', {
      toolUseId: 'tu_1', description: 'research', subagentType: 'Explore',
      model: 'sonnet', durationMs: 10000, tokensIn: 300, tokensOut: 100,
    });

    const entry = buildLogEntry(state, 1, '2025-01-01T00:00:00.000Z');

    assert.equal(entry.storyId, 'PROJ-1');
    assert.equal(entry.storyName, 'Test');
    assert.equal(entry.sessionId, 'sess_1');
    assert.equal(entry.startedAt, '2025-01-01T00:00:00.000Z');
    assert.equal(entry.iteration, 1);
    assert.equal(entry.duration, '1m');
    assert.equal(entry.turns, 3);
    assert.equal(entry.tokensIn, 1200);
    assert.equal(entry.tokensOut, 500);
    assert.equal(entry.costUsd, 0.05);
    assert.equal(entry.subagents.length, 1);
    assert.equal(entry.subagents[0].description, 'research');
    assert.equal(entry.subagents[0].model, 'sonnet');
    assert.equal(entry.subagents[0].duration, '10s');
  });

  it('handles empty state gracefully', () => {
    const state = createState();
    const entry = buildLogEntry(state, 0, '2025-01-01T00:00:00.000Z');
    assert.equal(entry.storyId, null);
    assert.equal(entry.sessionId, null);
    assert.equal(entry.duration, null);
    assert.equal(entry.tokensIn, 0);
    assert.equal(entry.tokensOut, 0);
    assert.deepEqual(entry.subagents, []);
  });
});

describe('quota detection', () => {
  it('detects "usage limit reached" as quota_exhausted', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Claude usage limit reached. Your limit will reset at 2pm.' }] },
    }, state);
    assert.equal(state.quotaStatus, 'quota_exhausted');
  });

  it('detects "Usage Limit" case-insensitively', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: "You've hit a Usage Limit for this period." }] },
    }, state);
    assert.equal(state.quotaStatus, 'quota_exhausted');
  });

  it('detects "rate limit" as rate_limit', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'API Error: Rate limit reached' }] },
    }, state);
    assert.equal(state.quotaStatus, 'rate_limit');
  });

  it('detects "rate_limit" (underscore) as rate_limit', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Error type: rate_limit_error' }] },
    }, state);
    assert.equal(state.quotaStatus, 'rate_limit');
  });

  it('detects "overloaded" as rate_limit', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'The API is temporarily overloaded' }] },
    }, state);
    assert.equal(state.quotaStatus, 'rate_limit');
  });

  it('does not trigger on unrelated text', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Here is how to implement pagination with a page limit of 50.' }] },
    }, state);
    assert.equal(state.quotaStatus, null);
  });

  it('only captures first quota signal', () => {
    const state = createState();
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'Claude usage limit reached.' }] },
    }, state);
    processEvent({
      type: 'assistant',
      message: { content: [{ type: 'text', text: 'API Error: Rate limit reached' }] },
    }, state);
    assert.equal(state.quotaStatus, 'quota_exhausted');
  });

  it('quotaStatus defaults to null', () => {
    const state = createState();
    assert.equal(state.quotaStatus, null);
  });

  it('QUOTA_PATTERNS is exported and has expected entries', () => {
    assert.ok(Array.isArray(QUOTA_PATTERNS));
    assert.ok(QUOTA_PATTERNS.length >= 3);
    assert.equal(QUOTA_PATTERNS[0].status, 'quota_exhausted');
    assert.equal(QUOTA_PATTERNS[1].status, 'rate_limit');
  });
});
