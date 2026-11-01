import assert from 'node:assert/strict'
import test from 'node:test'
import { buildProbeArgs, pickProbeTool } from '../src/utils/mcpCheck.ts'

test('pickProbeTool prefers known cheap tools', () => {
  assert.equal(pickProbeTool(['tavily_crawl', 'tavily_search', 'tavily_research']), 'tavily_search')
  assert.equal(pickProbeTool(['web_search_advanced_exa', 'web_search_exa']), 'web_search_exa')
  assert.equal(pickProbeTool(['query-docs', 'resolve-library-id']), 'resolve-library-id')
})

test('pickProbeTool avoids costly tools for unknown services', () => {
  assert.equal(pickProbeTool(['deep_research', 'list_items', 'batch_run']), 'list_items')
  assert.equal(pickProbeTool(['agent_run']), 'agent_run')
  assert.equal(pickProbeTool([]), '')
})

test('buildProbeArgs fills required fields and minimises result counts', () => {
  const args = buildProbeArgs({
    properties: {
      query: { type: 'string' },
      url: { type: 'string' },
      repoName: { type: 'string' },
      max_results: { type: 'integer' },
      topic: { type: 'string', enum: ['general', 'news'] },
      depth: { type: 'number', minimum: 3 },
      optional: { type: 'string' },
    },
    required: ['query', 'url', 'repoName', 'topic', 'depth'],
  })
  assert.deepEqual(args, {
    query: 'test',
    url: 'https://example.com',
    repoName: 'facebook/react',
    topic: 'general',
    depth: 3,
    max_results: 1,
  })
})

test('buildProbeArgs tolerates missing schema', () => {
  assert.deepEqual(buildProbeArgs(undefined), {})
})
