import assert from 'node:assert/strict'
import test from 'node:test'
import {
  filterToolGroups,
  groupToolsForAccess,
  serviceNameOf,
} from '../src/utils/toolAccessGroups.ts'

test('serviceNameOf strips trailing account numbers', () => {
  assert.equal(serviceNameOf('tavily01'), 'tavily')
  assert.equal(serviceNameOf('context7-02'), 'context7')
  assert.equal(serviceNameOf('deepwiki'), 'deepwiki')
  assert.equal(serviceNameOf('123'), '123')
})

test('groupToolsForAccess groups multi-account tools under one service', () => {
  const groups = groupToolsForAccess([
    {
      tool: { name: 'tavily_search', description: 'search' },
      sources: [{ upstreamName: 'tavily01' }, { upstreamName: 'tavily02' }],
    },
    {
      tool: { name: 'tavily_crawl', description: 'crawl' },
      sources: [{ upstreamName: 'tavily03' }],
    },
    {
      tool: { name: 'web_search_exa', description: 'exa search' },
      sources: [{ upstreamName: 'exa01' }],
    },
  ])
  assert.deepEqual(
    groups.map((g) => [g.label, g.tools.map((t) => t.name)]),
    [
      ['exa', ['web_search_exa']],
      ['tavily', ['tavily_crawl', 'tavily_search']],
    ],
  )
  assert.equal(groups[1].tools[1].sourceCount, 2)
})

test('filterToolGroups matches tool names, descriptions and group labels', () => {
  const groups = groupToolsForAccess([
    {
      tool: { name: 'tavily_search', description: 'web search' },
      sources: [{ upstreamName: 'tavily01' }],
    },
    {
      tool: { name: 'tavily_crawl', description: 'crawl pages' },
      sources: [{ upstreamName: 'tavily01' }],
    },
    {
      tool: { name: 'query-docs', description: 'docs' },
      sources: [{ upstreamName: 'context7-01' }],
    },
  ])
  assert.deepEqual(
    filterToolGroups(groups, 'crawl').map((g) => g.tools.map((t) => t.name)),
    [['tavily_crawl']],
  )
  assert.equal(filterToolGroups(groups, 'context7')[0].tools.length, 1)
  assert.equal(filterToolGroups(groups, '  ').length, 2)
})
