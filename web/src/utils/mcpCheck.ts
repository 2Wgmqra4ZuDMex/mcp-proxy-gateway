/**
 * MCP 检测：为每个服务挑选一个低消耗的检测工具，并按工具参数定义生成最小调用参数。
 * 结果仅作默认值，界面上可以改。
 *
 * 注意：本文件供 node:test 直接加载，只使用相对路径导入。
 */

/** 已知服务的首选检测工具（按优先级）。 */
const PREFERRED_PROBE_TOOLS = [
  'tavily_search',
  'web_search_exa',
  'brave_web_search',
  'firecrawl_scrape',
  'resolve-library-id',
  'read_wiki_structure',
]

/** 名称里含这些词的工具通常更贵或需要高级套餐，自动挑选时避开。 */
const COSTLY_HINTS = [
  'advanced',
  'research',
  'crawl',
  'agent',
  'batch',
  'deep',
  'monitor',
  'summar',
  'extract',
]

export function pickProbeTool(toolNames: readonly string[]): string {
  for (const name of PREFERRED_PROBE_TOOLS) {
    if (toolNames.includes(name)) return name
  }
  const cheap = toolNames.filter(
    (name) => !COSTLY_HINTS.some((hint) => name.toLowerCase().includes(hint)),
  )
  const pool = cheap.length > 0 ? cheap : [...toolNames]
  return pool.find((name) => /search|query|list|get|read/i.test(name)) ?? pool[0] ?? ''
}

interface SchemaProperty {
  type?: string | string[]
  minimum?: number
  enum?: unknown[]
  default?: unknown
}

interface ObjectSchema {
  properties?: Record<string, SchemaProperty>
  required?: string[]
}

/** 结果条数类参数：存在时设为 1，尽量少消耗额度。 */
const LIMIT_FIELDS = [
  'max_results',
  'maxResults',
  'numResults',
  'num_results',
  'count',
  'limit',
  'tokens',
]

function sampleString(name: string): string {
  const key = name.toLowerCase()
  if (key.includes('url')) return 'https://example.com'
  if (key.includes('repo')) return 'facebook/react'
  if (key.includes('library')) return 'react'
  return 'test'
}

function sampleValue(name: string, prop: SchemaProperty): unknown {
  if (prop.default !== undefined) return prop.default
  if (Array.isArray(prop.enum) && prop.enum.length > 0) return prop.enum[0]
  const type = Array.isArray(prop.type) ? prop.type.find((t) => t !== 'null') : prop.type
  switch (type) {
    case 'number':
    case 'integer':
      return typeof prop.minimum === 'number' ? Math.max(prop.minimum, 1) : 1
    case 'boolean':
      return false
    case 'array':
      return []
    case 'object':
      return {}
    default:
      return sampleString(name)
  }
}

export function buildProbeArgs(schema: unknown): Record<string, unknown> {
  const s = (schema ?? {}) as ObjectSchema
  const props = s.properties ?? {}
  const args: Record<string, unknown> = {}
  for (const name of s.required ?? []) {
    args[name] = sampleValue(name, props[name] ?? {})
  }
  // 有的工具把 url 设为“与其他字段二选一”，不在 required 里但实际必须提供。
  if (props.url !== undefined && args.url === undefined) args.url = sampleString('url')
  for (const field of LIMIT_FIELDS) {
    const prop = props[field]
    if (prop === undefined) continue
    const type = Array.isArray(prop.type) ? prop.type : [prop.type]
    if (type.includes('number') || type.includes('integer')) args[field] = 1
  }
  return args
}
