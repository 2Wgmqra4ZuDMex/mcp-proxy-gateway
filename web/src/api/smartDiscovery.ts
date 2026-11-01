import { requestData } from '@/api/request'

export type APIStyle = 'chat_completions' | 'responses'

export interface SmartDiscoveryConfig {
  configured: boolean
  baseUrl: string
  apiStyle: APIStyle
  model: string
  hasKey: boolean
  keyHint?: string
  proxyId: string
  timeoutS: number
  enrichEnabled: boolean
  rerankEnabled: boolean
}

export interface SmartDiscoveryInput {
  baseUrl: string
  apiStyle: APIStyle
  model: string
  /** 留空表示保留已保存的 Key。 */
  apiKey: string
  proxyId: string
  timeoutS: number
  enrichEnabled: boolean
  rerankEnabled: boolean
}

export interface SmartDiscoveryStatus {
  running: boolean
  total: number
  enriched: number
  pending: number
  services: number
  progress: number
  progressTotal: number
  failed: number
  lastError?: string
  lastRunAt?: string
  lastDurationMs?: number
  lastAdded?: number
  inputTokens?: number
  outputTokens?: number
}

export interface SmartDiscoveryState {
  config: SmartDiscoveryConfig
  status: SmartDiscoveryStatus
}

export interface SmartDiscoveryTestResult {
  latencyMs: number
  reply: string
  model: string
  inputTokens: number
  outputTokens: number
}

export interface EnrichedTool {
  name: string
  service: string
  summary: string
  keywords: string[]
  useWhen?: string
  related?: string[]
  enriched: boolean
}

export interface SearchToolSummary {
  name: string
  description: string
  upstream?: string
  sourceCount?: number
}

export interface SearchPage {
  tools: SearchToolSummary[]
  hint?: string
  rankedBy?: string
}

export interface SmartDiscoveryPreview {
  keyword: SearchPage
  ai?: SearchPage
  aiEnabled: boolean
  aiError?: string
  aiLatencyMs?: number
}

export const getSmartDiscovery = () => requestData<SmartDiscoveryState>({ url: '/smart-discovery' })

export const saveSmartDiscovery = (data: SmartDiscoveryInput) =>
  requestData<SmartDiscoveryState>({ url: '/smart-discovery', method: 'PUT', data })

export const testSmartDiscovery = (data: SmartDiscoveryInput) =>
  requestData<SmartDiscoveryTestResult>({
    url: '/smart-discovery/test',
    method: 'POST',
    data,
    timeout: 60000,
  })

export const startSmartDiscoveryEnrich = (force: boolean) =>
  requestData<SmartDiscoveryStatus>({
    url: '/smart-discovery/enrich',
    method: 'POST',
    data: { force },
  })

export const listEnrichedTools = () =>
  requestData<{ tools: EnrichedTool[] | null; services: Record<string, string> | null }>({
    url: '/smart-discovery/tools',
  })

export const previewSmartSearch = (query: string, apiKeyId: string) =>
  requestData<SmartDiscoveryPreview>({
    url: '/smart-discovery/preview',
    method: 'POST',
    data: { query, apiKeyId },
    timeout: 60000,
  })
