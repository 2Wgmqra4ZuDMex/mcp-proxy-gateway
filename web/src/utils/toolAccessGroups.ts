/**
 * API Key 工具白名单的展示分组：按工具首个来源的上游名分组，
 * 去掉末尾的账号序号（如 tavily01、context7-02），让多账号的同一服务归为一组。
 */

export interface ToolAccessSourceLike {
  upstreamName: string
}

export interface ToolAccessDetailLike {
  tool: { name: string; description: string }
  sources: ToolAccessSourceLike[] | null
}

export interface ToolAccessItem {
  name: string
  description: string
  sourceCount: number
}

export interface ToolAccessGroup {
  key: string
  label: string
  tools: ToolAccessItem[]
}

/** 去掉上游名末尾的账号序号与分隔符；去完为空时保留原名。 */
export function serviceNameOf(upstreamName: string): string {
  const trimmed = upstreamName.trim()
  const stripped = trimmed.replace(/[\s_-]*\d+$/, '')
  return stripped === '' ? trimmed : stripped
}

export function groupToolsForAccess(details: ToolAccessDetailLike[]): ToolAccessGroup[] {
  const groups = new Map<string, ToolAccessGroup>()
  for (const detail of details) {
    const sources = detail.sources ?? []
    const label = sources.length > 0 ? serviceNameOf(sources[0]!.upstreamName) : '未知来源'
    const key = label.toLowerCase()
    let group = groups.get(key)
    if (group === undefined) {
      group = { key, label, tools: [] }
      groups.set(key, group)
    }
    group.tools.push({
      name: detail.tool.name,
      description: detail.tool.description,
      sourceCount: sources.length,
    })
  }
  const out = Array.from(groups.values())
  for (const group of out) group.tools.sort((a, b) => a.name.localeCompare(b.name))
  out.sort((a, b) => a.label.localeCompare(b.label))
  return out
}

/** 按关键字过滤分组（匹配工具名、描述或分组名），不保留空分组。 */
export function filterToolGroups(groups: ToolAccessGroup[], keyword: string): ToolAccessGroup[] {
  const needle = keyword.trim().toLowerCase()
  if (needle === '') return groups
  const out: ToolAccessGroup[] = []
  for (const group of groups) {
    if (group.label.toLowerCase().includes(needle)) {
      out.push(group)
      continue
    }
    const tools = group.tools.filter(
      (tool) =>
        tool.name.toLowerCase().includes(needle) || tool.description.toLowerCase().includes(needle),
    )
    if (tools.length > 0) out.push({ ...group, tools })
  }
  return out
}
