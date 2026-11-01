/**
 * 上游「账号组」：同一服务的多个账号（tavily01…tavily06）按名称去掉末尾编号归为一组。
 * 只做界面聚合，不改变数据结构；分组规则与工具权限、出站代理页一致。
 *
 * 注意：本文件供 node:test 直接加载，只使用相对路径导入。
 */

import { serviceNameOf } from './toolAccessGroups.ts'

export interface AccountLike {
  id: string
  config: { name: string; enabled: boolean; connParams?: Record<string, unknown> | null }
  state: string
}

export interface AccountGroup<T extends AccountLike> {
  key: string
  label: string
  items: T[]
}

/** 按服务名分组；组按首次出现顺序排列，组内按名称自然排序。 */
export function groupUpstreamsByAccount<T extends AccountLike>(
  list: readonly T[],
): AccountGroup<T>[] {
  const groups = new Map<string, AccountGroup<T>>()
  for (const up of list) {
    const label = serviceNameOf(up.config.name)
    const key = `acct:${label.toLowerCase()}`
    let group = groups.get(key)
    if (group === undefined) {
      group = { key, label, items: [] }
      groups.set(key, group)
    }
    group.items.push(up)
  }
  const out = Array.from(groups.values())
  for (const group of out) {
    group.items.sort((a, b) =>
      a.config.name.localeCompare(b.config.name, undefined, { numeric: true }),
    )
  }
  return out
}

/**
 * 为账号组生成下一个账号名：沿用已有编号的分隔符与位数（tavily06 → tavily07，
 * context7-07 → context7-08）；组内没有编号时从 02 开始（deepwiki → deepwiki02）。
 * 结果保证不与 existingNames 冲突（不区分大小写）。
 */
export function nextAccountName(
  groupLabel: string,
  groupNames: readonly string[],
  existingNames: readonly string[],
): string {
  const taken = new Set(existingNames.map((n) => n.trim().toLowerCase()))
  let separator = ''
  let width = 2
  let max = 1
  let numbered = false
  for (const name of groupNames) {
    const match = /^(.*?)([\s_-]*)(\d+)$/.exec(name.trim())
    if (match === null || serviceNameOf(name).toLowerCase() !== groupLabel.toLowerCase()) continue
    const value = Number(match[3])
    if (!numbered || value >= max) {
      separator = match[2] ?? ''
      width = match[3]!.length
    }
    numbered = true
    max = Math.max(max, value)
  }
  for (let n = max + 1; n < max + 10_000; n += 1) {
    const candidate = `${groupLabel}${separator}${String(n).padStart(width, '0')}`
    if (!taken.has(candidate.toLowerCase())) return candidate
  }
  return `${groupLabel}-${Date.now()}`
}

export interface AccountGroupSummary {
  total: number
  available: number
  unavailable: number
  disabled: number
  allDisabled: boolean
  /** 出口汇总，例如 ["家里-1 ×3", "直连 ×2"]。 */
  egress: string[]
}

export function summarizeAccountGroup(
  items: readonly AccountLike[],
  proxyName: (proxyId: string) => string,
): AccountGroupSummary {
  let available = 0
  let unavailable = 0
  let disabled = 0
  const egress = new Map<string, number>()
  for (const up of items) {
    if (!up.config.enabled) disabled += 1
    else if (up.state === 'available') available += 1
    else if (up.state !== 'connecting') unavailable += 1
    const proxyId =
      typeof up.config.connParams?.proxyId === 'string' ? up.config.connParams.proxyId : ''
    const label = proxyId === '' ? '直连' : proxyName(proxyId)
    egress.set(label, (egress.get(label) ?? 0) + 1)
  }
  return {
    total: items.length,
    available,
    unavailable,
    disabled,
    allDisabled: items.length > 0 && disabled === items.length,
    egress: Array.from(egress.entries()).map(([label, count]) =>
      items.length === 1 ? label : `${label} ×${count}`,
    ),
  }
}
