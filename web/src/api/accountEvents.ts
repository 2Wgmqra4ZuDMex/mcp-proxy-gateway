import { requestData } from '@/api/request'

export type AccountEventKind = 'rate_limited' | 'quota_exhausted' | 'unauthorized'

export interface AccountEvent {
  id: number
  upstreamId: string
  upstreamName: string
  kind: AccountEventKind
  httpStatus?: number
  message: string
  tool?: string
  source: 'call' | 'check'
  occurredAt: string
}

export interface AccountCooldown {
  upstreamId: string
  kind: AccountEventKind
  message: string
  until: string
}

export interface AccountEventSummary {
  upstreamId: string
  upstreamName: string
  last24h: number
  last7d: number
  lastKind: AccountEventKind
  lastStatus?: number
  lastMessage: string
  lastTool?: string
  lastAt: string
}

export interface AccountEventStatus {
  cooldowns: AccountCooldown[]
  accounts: AccountEventSummary[]
  events: AccountEvent[]
}

export const accountEventKindLabel: Record<AccountEventKind, string> = {
  rate_limited: '被限流',
  quota_exhausted: '额度不足',
  unauthorized: 'Key 无效或无权限',
}

export async function getAccountEvents(): Promise<AccountEventStatus> {
  const data = await requestData<AccountEventStatus>({ url: '/account-events' })
  return {
    cooldowns: data.cooldowns ?? [],
    accounts: data.accounts ?? [],
    events: data.events ?? [],
  }
}

/** 清除记录并结束冷却；不传 upstreamId 时清除全部。 */
export const clearAccountEvents = (upstreamId?: string) =>
  requestData<void>({
    url: '/account-events',
    method: 'DELETE',
    params: upstreamId ? { upstreamId } : undefined,
  })
