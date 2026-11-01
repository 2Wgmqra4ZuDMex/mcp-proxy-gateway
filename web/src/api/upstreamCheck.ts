import { requestData } from '@/api/request'

export interface UpstreamCheckProbe {
  tool: string
  args: Record<string, unknown>
}

export interface UpstreamCallCheck {
  tool: string
  ok: boolean
  isError: boolean
  latencyMs: number
  message?: string
  preview?: string
}

export interface UpstreamCheckResult {
  upstreamId: string
  name: string
  connect: {
    ok: boolean
    stage: string
    durationMs: number
    count: number
    message?: string
  }
  call?: UpstreamCallCheck
}

/** 用已保存配置检测某个上游；传入 probe 时会真实调用一次（消耗该账号额度）。 */
export const checkUpstream = (id: string, probe?: UpstreamCheckProbe) =>
  requestData<UpstreamCheckResult>({
    url: `/upstreams/${encodeURIComponent(id)}/check`,
    method: 'POST',
    data: { probe: probe ?? null },
    timeout: 120000,
  })
