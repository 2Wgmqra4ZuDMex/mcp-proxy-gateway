import assert from 'node:assert/strict'
import test from 'node:test'
import {
  groupUpstreamsByAccount,
  nextAccountName,
  summarizeAccountGroup,
} from '../src/utils/accountGroups.ts'

const up = (id, name, state = 'available', enabled = true, proxyId) => ({
  id,
  state,
  config: { name, enabled, connParams: proxyId ? { proxyId } : {} },
})

test('groupUpstreamsByAccount groups numbered accounts and keeps natural order', () => {
  const groups = groupUpstreamsByAccount([
    up('1', 'tavily10'),
    up('2', 'exa01'),
    up('3', 'tavily02'),
    up('4', 'deepwiki'),
    up('5', 'tavily01'),
  ])
  assert.deepEqual(
    groups.map((g) => [g.label, g.items.map((x) => x.config.name)]),
    [
      ['tavily', ['tavily01', 'tavily02', 'tavily10']],
      ['exa', ['exa01']],
      ['deepwiki', ['deepwiki']],
    ],
  )
})

test('nextAccountName keeps separator and padding', () => {
  const all = ['tavily01', 'tavily06', 'context7-01', 'context7-07', 'deepwiki']
  assert.equal(nextAccountName('tavily', ['tavily01', 'tavily06'], all), 'tavily07')
  assert.equal(nextAccountName('context7', ['context7-01', 'context7-07'], all), 'context7-08')
  assert.equal(nextAccountName('deepwiki', ['deepwiki'], all), 'deepwiki02')
})

test('nextAccountName skips names already taken anywhere', () => {
  assert.equal(nextAccountName('exa', ['exa01', 'exa02'], ['exa01', 'exa02', 'EXA03']), 'exa04')
})

test('summarizeAccountGroup counts states and egress', () => {
  const s = summarizeAccountGroup(
    [
      up('1', 'tavily01', 'available', true, 'p1'),
      up('2', 'tavily02', 'unavailable', true, 'p1'),
      up('3', 'tavily03', 'available', true),
      up('4', 'tavily04', 'unavailable', false),
    ],
    (id) => (id === 'p1' ? '家里-1' : '未知'),
  )
  assert.equal(s.available, 2)
  assert.equal(s.unavailable, 1)
  assert.equal(s.disabled, 1)
  assert.equal(s.allDisabled, false)
  assert.deepEqual(s.egress, ['家里-1 ×2', '直连 ×2'])
})
