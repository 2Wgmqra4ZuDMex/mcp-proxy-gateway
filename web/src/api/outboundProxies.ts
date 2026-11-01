import { requestData } from '@/api/request'

/** 出站代理：上游经 connParams.proxyId 引用；url 含明文凭证，displayUrl 已隐去密码。 */
export interface OutboundProxy {
  id: string
  name: string
  url: string
  displayUrl: string
  note: string
  enabled: boolean
  createdAt: string
  /** 当前引用该代理的上游标识。 */
  upstreamIds: string[]
}

export interface OutboundProxyInput {
  name: string
  url: string
  note: string
  enabled: boolean
}

export interface OutboundProxyTestResult {
  ok: boolean
  latencyMs: number
  exitIp?: string
  message?: string
}

export interface OutboundProxyAssignResult {
  updated: number
  /** 失败的上游名称 → 原因。 */
  failed?: Record<string, string>
}

export async function listOutboundProxies(): Promise<OutboundProxy[]> {
  const data = await requestData<{ proxies: OutboundProxy[] | null }>({ url: '/outbound-proxies' })
  return data.proxies ?? []
}

export const createOutboundProxy = (data: OutboundProxyInput) =>
  requestData<OutboundProxy>({ url: '/outbound-proxies', method: 'POST', data })

export const updateOutboundProxy = (id: string, data: OutboundProxyInput) =>
  requestData<OutboundProxy>({
    url: `/outbound-proxies/${encodeURIComponent(id)}`,
    method: 'PUT',
    data,
  })

export const deleteOutboundProxy = (id: string) =>
  requestData<void>({ url: `/outbound-proxies/${encodeURIComponent(id)}`, method: 'DELETE' })

export const testOutboundProxy = (id: string) =>
  requestData<OutboundProxyTestResult>({
    url: `/outbound-proxies/${encodeURIComponent(id)}/test`,
    method: 'POST',
    timeout: 30000,
  })

/** 将一组上游设为同一代理；proxyId 为空串表示直连。 */
export const assignOutboundProxy = (upstreamIds: string[], proxyId: string) =>
  requestData<OutboundProxyAssignResult>({
    url: '/outbound-proxies/assign',
    method: 'POST',
    data: { upstreamIds, proxyId },
    timeout: 60000,
  })

/** 将上游按顺序轮流分配到给定代理。 */
export const distributeOutboundProxies = (upstreamIds: string[], proxyIds: string[]) =>
  requestData<OutboundProxyAssignResult>({
    url: '/outbound-proxies/distribute',
    method: 'POST',
    data: { upstreamIds, proxyIds },
    timeout: 60000,
  })
