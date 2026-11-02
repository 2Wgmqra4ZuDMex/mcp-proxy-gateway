<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import AdminLayout from '@/components/layout/AdminLayout.vue'
import PageBreadcrumb from '@/components/common/PageBreadcrumb.vue'
import { ChevronDownIcon } from '@/icons'
import { listUpstreams, type Upstream } from '@/api/upstreams'
import { getAggregatedTools } from '@/api/tools'
import { checkUpstream, type UpstreamCheckResult } from '@/api/upstreamCheck'
import { useToast } from '@/composables/useToast'
import { groupUpstreamsByAccount } from '@/utils/accountGroups'
import { serviceNameOf } from '@/utils/toolAccessGroups'
import { buildProbeArgs, pickProbeTool } from '@/utils/mcpCheck'
import {
  accountEventKindLabel,
  clearAccountEvents,
  getAccountEvents,
  type AccountEventStatus,
} from '@/api/accountEvents'
import { useConfirm } from '@/composables/useConfirm'

const toast = useToast()
const { confirm } = useConfirm()

// ── 账号异常记录 ────────────────────────────────────────────────────────────
const accountStatus = ref<AccountEventStatus>({ cooldowns: [], accounts: [], events: [] })
const accountLoading = ref(false)
const expandedAccounts = ref<Set<string>>(new Set())
const nowTick = ref(Date.now())
let tickTimer: ReturnType<typeof setInterval> | null = null

async function loadAccountEvents(): Promise<void> {
  accountLoading.value = true
  try {
    accountStatus.value = await getAccountEvents()
    nowTick.value = Date.now()
  } catch (err) {
    toast.error(err instanceof Error ? err.message : '读取账号异常记录失败')
  } finally {
    accountLoading.value = false
  }
}

const upstreamNameById = computed(() => {
  const map = new Map<string, string>()
  for (const up of upstreams.value) map.set(up.id, up.config.name)
  return map
})

function accountName(id: string, fallback = ''): string {
  return upstreamNameById.value.get(id) ?? (fallback || id.slice(0, 8))
}

const cooldownById = computed(() => {
  const map = new Map<string, AccountEventStatus['cooldowns'][number]>()
  for (const c of accountStatus.value.cooldowns) {
    if (new Date(c.until).getTime() > nowTick.value) map.set(c.upstreamId, c)
  }
  return map
})

/** 有记录的账号，以及只在冷却中、尚无持久记录的账号。 */
const problemAccounts = computed(() => {
  const rows = accountStatus.value.accounts.map((a) => ({
    ...a,
    cooling: cooldownById.value.get(a.upstreamId),
  }))
  for (const [id, c] of cooldownById.value) {
    if (!rows.some((r) => r.upstreamId === id)) {
      rows.unshift({
        upstreamId: id,
        upstreamName: '',
        last24h: 0,
        last7d: 0,
        lastKind: c.kind,
        lastMessage: c.message,
        lastAt: '',
        cooling: c,
      })
    }
  }
  return rows.sort((a, b) => Number(Boolean(b.cooling)) - Number(Boolean(a.cooling)))
})

function eventsOf(id: string) {
  return accountStatus.value.events.filter((e) => e.upstreamId === id).slice(0, 20)
}

function toggleAccount(id: string): void {
  const next = new Set(expandedAccounts.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedAccounts.value = next
}

function durationText(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 360) / 10} 小时`
  if (seconds >= 60) return `${Math.round(seconds / 60)} 分钟`
  return `${seconds} 秒`
}

function remaining(until: string): string {
  const s = Math.max(0, Math.round((new Date(until).getTime() - nowTick.value) / 1000))
  return s >= 60 ? `约 ${Math.ceil(s / 60)} 分钟` : `${s} 秒`
}

function ago(at: string): string {
  if (!at) return ''
  const s = Math.max(0, Math.round((nowTick.value - new Date(at).getTime()) / 1000))
  if (s < 60) return `${s} 秒前`
  if (s < 3600) return `${Math.round(s / 60)} 分钟前`
  if (s < 86400) return `${Math.round(s / 3600)} 小时前`
  return `${Math.round(s / 86400)} 天前`
}

async function clearAccount(id?: string): Promise<void> {
  const ok = await confirm({
    title: id ? '清除该账号的记录' : '清除全部账号异常记录',
    message:
      '会删除异常记录，并让正在冷却的账号立即恢复参与轮询。账号如果仍有问题，下次被拒时会重新记录。',
    confirmText: '清除',
    tone: 'warning',
  })
  if (!ok) return
  try {
    await clearAccountEvents(id)
    toast.success('已清除')
    await loadAccountEvents()
  } catch (err) {
    toast.error(err instanceof Error ? err.message : '清除失败')
  }
}

const kindTone: Record<string, string> = {
  rate_limited: 'bg-warning-50 text-warning-700 dark:bg-warning-500/15 dark:text-warning-400',
  quota_exhausted: 'bg-error-50 text-error-700 dark:bg-error-500/15 dark:text-error-400',
  unauthorized: 'bg-error-50 text-error-700 dark:bg-error-500/15 dark:text-error-400',
}

const PROBE_STORAGE_KEY = 'mpg.mcpCheck.probes'
const CONCURRENCY = 3

interface ServiceTool {
  name: string
  schema: unknown
}

interface ProbeConfig {
  tool: string
  argsText: string
}

type RowState =
  | { status: 'idle' }
  | { status: 'running' }
  | { status: 'done'; result: UpstreamCheckResult }
  | { status: 'failed'; message: string }

const loading = ref(false)
const loadError = ref('')
const upstreams = ref<Upstream[]>([])
const toolsByService = ref<Record<string, ServiceTool[]>>({})
const callCheck = ref(false)
const running = ref(false)
const rows = reactive<Record<string, RowState>>({})
const probes = reactive<Record<string, ProbeConfig>>({})
const probeErrors = reactive<Record<string, string>>({})
const collapsed = ref<Set<string>>(new Set())

const groups = computed(() => groupUpstreamsByAccount(upstreams.value))

function serviceKey(label: string): string {
  return label.toLowerCase()
}

function readSavedProbes(): Record<string, ProbeConfig> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(PROBE_STORAGE_KEY) ?? '{}')
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, ProbeConfig>) : {}
  } catch {
    return {}
  }
}

function saveProbes(): void {
  try {
    localStorage.setItem(PROBE_STORAGE_KEY, JSON.stringify(probes))
  } catch {
    // 仅本次会话生效。
  }
}

function defaultProbe(key: string): ProbeConfig {
  const tools = toolsByService.value[key] ?? []
  const tool = pickProbeTool(tools.map((t) => t.name))
  const schema = tools.find((t) => t.name === tool)?.schema
  return { tool, argsText: JSON.stringify(buildProbeArgs(schema), null, 2) }
}

function resetProbe(key: string): void {
  probes[key] = defaultProbe(key)
  delete probeErrors[key]
  saveProbes()
}

function onProbeToolChange(key: string, tool: string): void {
  const schema = (toolsByService.value[key] ?? []).find((t) => t.name === tool)?.schema
  probes[key] = { tool, argsText: JSON.stringify(buildProbeArgs(schema), null, 2) }
  delete probeErrors[key]
  saveProbes()
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [list, tools] = await Promise.all([listUpstreams(), getAggregatedTools()])
    upstreams.value = list
    const byService: Record<string, Map<string, unknown>> = {}
    for (const detail of tools.toolDetails) {
      for (const source of detail.sources ?? []) {
        const key = serviceKey(serviceNameOf(source.upstreamName || source.upstreamId))
        const map = (byService[key] ??= new Map())
        if (!map.has(source.originalName)) map.set(source.originalName, source.inputSchema)
      }
    }
    const next: Record<string, ServiceTool[]> = {}
    for (const [key, map] of Object.entries(byService)) {
      next[key] = Array.from(map.entries())
        .map(([name, schema]) => ({ name, schema }))
        .sort((a, b) => a.name.localeCompare(b.name))
    }
    toolsByService.value = next
    const saved = readSavedProbes()
    for (const group of groups.value) {
      const key = serviceKey(group.label)
      const savedProbe = saved[key]
      const tools = next[key] ?? []
      probes[key] =
        savedProbe && tools.some((t) => t.name === savedProbe.tool) ? savedProbe : defaultProbe(key)
    }
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  void loadAccountEvents()
  tickTimer = setInterval(() => {
    nowTick.value = Date.now()
  }, 5000)
})
onBeforeUnmount(() => {
  if (tickTimer !== null) clearInterval(tickTimer)
})

function parseProbe(key: string): { tool: string; args: Record<string, unknown> } | null {
  const probe = probes[key]
  if (!probe || probe.tool === '') {
    probeErrors[key] = '这个服务没有可用的检测工具，请先刷新上游的工具列表'
    return null
  }
  try {
    const args: unknown = JSON.parse(probe.argsText || '{}')
    if (args === null || typeof args !== 'object' || Array.isArray(args))
      throw new Error('not object')
    delete probeErrors[key]
    return { tool: probe.tool, args: args as Record<string, unknown> }
  } catch {
    probeErrors[key] = '调用参数必须是 JSON 对象'
    return null
  }
}

async function runChecks(targets: Upstream[]): Promise<void> {
  if (targets.length === 0) return
  const probeByUpstream = new Map<
    string,
    { tool: string; args: Record<string, unknown> } | undefined
  >()
  if (callCheck.value) {
    for (const up of targets) {
      const key = serviceKey(serviceNameOf(up.config.name))
      const probe = parseProbe(key)
      if (probe === null) {
        toast.error('有服务的检测调用参数不正确，请检查后重试')
        return
      }
      probeByUpstream.set(up.id, probe)
    }
  }
  running.value = true
  for (const up of targets) rows[up.id] = { status: 'running' }
  const queue = [...targets]
  const worker = async () => {
    for (let up = queue.shift(); up !== undefined; up = queue.shift()) {
      try {
        rows[up.id] = {
          status: 'done',
          result: await checkUpstream(up.id, probeByUpstream.get(up.id)),
        }
      } catch (err) {
        rows[up.id] = { status: 'failed', message: err instanceof Error ? err.message : '检测失败' }
      }
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(CONCURRENCY, targets.length) }, worker))
  } finally {
    running.value = false
  }
  const bad = targets.filter((up) => verdict(up.id).tone === 'error').length
  if (bad === 0) toast.success(`检测完成：${targets.length} 个全部正常`)
  else toast.error(`检测完成：${bad} / ${targets.length} 个有问题`)
}

function runAll(): void {
  void runChecks(upstreams.value)
}

function runGroup(items: Upstream[]): void {
  void runChecks(items)
}

interface Verdict {
  tone: 'idle' | 'running' | 'ok' | 'warn' | 'error'
  label: string
}

function verdict(id: string): Verdict {
  const row = rows[id]
  if (row === undefined || row.status === 'idle') return { tone: 'idle', label: '未检测' }
  if (row.status === 'running') return { tone: 'running', label: '检测中…' }
  if (row.status === 'failed') return { tone: 'error', label: '检测失败' }
  const r = row.result
  if (!r.connect.ok) return { tone: 'error', label: '无法连接' }
  if (r.call === undefined) return { tone: 'ok', label: '可连接' }
  if (r.call.ok) return { tone: 'ok', label: 'Key 有效' }
  if (r.call.message === '该上游没有这个工具') return { tone: 'warn', label: '缺少检测工具' }
  return { tone: 'error', label: '调用失败' }
}

function resultOf(id: string): UpstreamCheckResult | null {
  const row = rows[id]
  return row?.status === 'done' ? row.result : null
}

function failedMessage(id: string): string {
  const row = rows[id]
  return row?.status === 'failed' ? row.message : ''
}

const summary = computed(() => {
  const counts = { ok: 0, warn: 0, error: 0, checked: 0 }
  for (const up of upstreams.value) {
    const v = verdict(up.id)
    if (v.tone === 'ok' || v.tone === 'warn' || v.tone === 'error') {
      counts.checked += 1
      counts[v.tone] += 1
    }
  }
  return counts
})

function toggleCollapsed(key: string): void {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsed.value = next
}

const toneClass: Record<Verdict['tone'], string> = {
  idle: 'bg-gray-100 text-gray-500 dark:bg-white/5 dark:text-gray-400',
  running:
    'bg-blue-light-50 text-blue-light-700 dark:bg-blue-light-500/15 dark:text-blue-light-400',
  ok: 'bg-success-50 text-success-700 dark:bg-success-500/15 dark:text-success-400',
  warn: 'bg-warning-50 text-warning-700 dark:bg-warning-500/15 dark:text-warning-400',
  error: 'bg-error-50 text-error-700 dark:bg-error-500/15 dark:text-error-400',
}

const cardClass =
  'rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-800 dark:bg-white/[0.03]'
const btnPrimary =
  'bg-brand-500 hover:bg-brand-600 inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-medium text-white transition disabled:bg-gray-300 disabled:text-gray-500 dark:disabled:bg-gray-700 dark:disabled:text-gray-400'
const inputClass =
  'w-full rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-800 focus:border-brand-300 focus:outline-none dark:border-gray-700 dark:bg-gray-900 dark:text-white/90'
</script>

<template>
  <AdminLayout>
    <PageBreadcrumb pageTitle="MCP 检测" />

    <p
      v-if="loadError !== ''"
      class="bg-error-50 text-error-600 dark:bg-error-500/10 dark:text-error-400 mb-4 rounded-lg px-4 py-2.5 text-sm"
    >
      {{ loadError }}
    </p>

    <section :class="cardClass">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div class="min-w-0">
          <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">检测上游 MCP</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            逐个账号单独建立临时连接，不经过轮询，结果对应到具体的某个账号。
          </p>
          <label class="mt-3 flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input
              v-model="callCheck"
              type="checkbox"
              class="accent-brand-500 mt-0.5 h-4 w-4 shrink-0"
            />
            <span>
              同时调用检测 Key 是否有效
              <span class="block text-xs text-gray-400">
                只检测连接时，很多服务即使 Key
                无效也能连上。勾选后每个账号会真实调用一次下方的检测工具，会消耗 1 次额度。
              </span>
            </span>
          </label>
        </div>
        <div class="flex shrink-0 flex-col items-start gap-2 lg:items-end">
          <button
            type="button"
            :class="btnPrimary"
            :disabled="running || loading || upstreams.length === 0"
            @click="runAll"
          >
            {{ running ? '检测中…' : `检测全部 ${upstreams.length} 个` }}
          </button>
          <p v-if="summary.checked > 0" class="text-xs text-gray-500">
            已检测 {{ summary.checked }} 个：
            <span class="text-success-600 dark:text-success-400">{{ summary.ok }} 正常</span>
            <template v-if="summary.warn > 0">
              · <span class="text-warning-600">{{ summary.warn }} 需注意</span></template
            >
            <template v-if="summary.error > 0">
              ·
              <span class="text-error-600 dark:text-error-400"
                >{{ summary.error }} 有问题</span
              ></template
            >
          </p>
        </div>
      </div>
    </section>

    <section :class="[cardClass, 'mt-4']">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">账号异常</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            调用或检测时被上游拒绝（限流、额度不足、Key 无效或无权限）的账号会记录在这里，保留 30
            天。被拒的账号暂停参与轮询，连续被拒时逐级延长：限流 1 分钟 → 5 分钟 → 30 分钟 → 2 小时
            → 6 小时 → 24 小时；额度不足或 Key 无效 10 分钟 → 1 小时 → 6 小时 → 24
            小时。成功调用一次就回到第一级。
          </p>
        </div>
        <div class="flex shrink-0 gap-2">
          <button
            type="button"
            class="text-brand-600 dark:text-brand-400 text-sm font-medium hover:underline disabled:opacity-50"
            :disabled="accountLoading"
            @click="loadAccountEvents"
          >
            {{ accountLoading ? '刷新中…' : '刷新' }}
          </button>
          <button
            v-if="problemAccounts.length > 0"
            type="button"
            class="text-error-500 text-sm font-medium hover:underline"
            @click="clearAccount()"
          >
            全部清除
          </button>
        </div>
      </div>
      <p
        v-if="problemAccounts.length === 0"
        class="bg-success-50 text-success-700 dark:bg-success-500/10 dark:text-success-400 mt-4 rounded-lg px-4 py-2.5 text-sm"
      >
        近 7 天没有账号被拒，全部正常。
      </p>
      <div v-else class="mt-4 space-y-2">
        <div
          v-for="acc in problemAccounts"
          :key="acc.upstreamId"
          class="overflow-hidden rounded-xl border border-gray-100 dark:border-gray-800"
        >
          <div class="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-start">
            <button
              type="button"
              class="flex min-w-0 flex-1 items-start gap-3 text-left"
              :aria-expanded="expandedAccounts.has(acc.upstreamId)"
              @click="toggleAccount(acc.upstreamId)"
            >
              <ChevronDownIcon
                class="mt-0.5 h-4 w-4 shrink-0 text-gray-400 transition-transform"
                :class="expandedAccounts.has(acc.upstreamId) ? '' : '-rotate-90'"
                aria-hidden="true"
              />
              <span class="min-w-0 flex-1">
                <span class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-semibold text-gray-800 dark:text-white/90">{{
                    accountName(acc.upstreamId, acc.upstreamName)
                  }}</span>
                  <span
                    class="rounded-full px-2 py-0.5 text-[11px] font-medium"
                    :class="kindTone[acc.lastKind]"
                  >
                    {{ accountEventKindLabel[acc.lastKind]
                    }}<template v-if="acc.lastStatus"> · {{ acc.lastStatus }}</template>
                  </span>
                  <span
                    v-if="acc.cooling"
                    class="bg-warning-50 text-warning-700 dark:bg-warning-500/15 dark:text-warning-400 rounded-full px-2 py-0.5 text-[11px]"
                    >暂停中 · {{ remaining(acc.cooling.until) }}后恢复<template
                      v-if="acc.cooling.level > 1"
                      >（连续第 {{ acc.cooling.level }} 次）</template
                    ></span
                  >
                  <span v-else class="text-xs text-gray-400">已恢复轮询</span>
                </span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
                  <template v-if="acc.lastAt">
                    24 小时内 {{ acc.last24h }} 次 · 7 天内 {{ acc.last7d }} 次 · 最近
                    {{ ago(acc.lastAt)
                    }}<template v-if="acc.lastTool">（{{ acc.lastTool }}）</template>
                  </template>
                  <template v-else>刚被拒，记录写入中</template>
                </span>
                <span class="mt-1 line-clamp-2 block text-xs break-all text-gray-400">{{
                  acc.lastMessage
                }}</span>
              </span>
            </button>
            <button
              type="button"
              class="text-error-500 shrink-0 self-start text-xs hover:underline sm:mt-0.5"
              @click="clearAccount(acc.upstreamId)"
            >
              清除
            </button>
          </div>
          <ol
            v-if="expandedAccounts.has(acc.upstreamId)"
            class="divide-y divide-gray-100 border-t border-gray-100 dark:divide-gray-800 dark:border-gray-800"
          >
            <li v-for="ev in eventsOf(acc.upstreamId)" :key="ev.id" class="px-4 py-2 text-xs">
              <span class="text-gray-500 dark:text-gray-400">{{
                new Date(ev.occurredAt).toLocaleString('zh-CN')
              }}</span>
              <span class="ml-2 rounded px-1.5 py-0.5" :class="kindTone[ev.kind]"
                >{{ accountEventKindLabel[ev.kind]
                }}<template v-if="ev.httpStatus"> {{ ev.httpStatus }}</template></span
              >
              <span class="ml-2 text-gray-500"
                >{{ ev.source === 'check' ? 'MCP 检测' : '调用'
                }}<template v-if="ev.tool"> · {{ ev.tool }}</template
                ><template v-if="ev.cooldownS">
                  · 暂停 {{ durationText(ev.cooldownS)
                  }}<template v-if="(ev.level ?? 0) > 1"
                    >（连续第 {{ ev.level }} 次）</template
                  ></template
                ></span
              >
              <span class="mt-1 block break-all text-gray-400">{{ ev.message }}</span>
            </li>
            <li
              v-if="eventsOf(acc.upstreamId).length === 0"
              class="px-4 py-2 text-xs text-gray-400"
            >
              暂无持久记录（只有冷却状态）。
            </li>
          </ol>
        </div>
      </div>
    </section>

    <div class="mt-4 space-y-3">
      <section
        v-for="group in groups"
        :key="group.key"
        class="overflow-hidden rounded-2xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-white/[0.03]"
      >
        <div
          class="flex flex-col gap-2 border-b border-gray-100 px-4 py-3 sm:flex-row sm:items-center sm:px-5 dark:border-gray-800"
        >
          <button
            type="button"
            class="flex min-w-0 flex-1 items-center justify-between gap-3 text-left"
            :aria-expanded="!collapsed.has(group.key)"
            @click="toggleCollapsed(group.key)"
          >
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <span class="text-sm font-semibold text-gray-800 dark:text-white/90">{{
                group.label
              }}</span>
              <span class="text-xs text-gray-400">{{ group.items.length }} 个账号</span>
              <span
                v-for="up in group.items"
                :key="`v:${up.id}`"
                class="rounded-full px-2 py-0.5 text-[11px] font-medium"
                :class="toneClass[verdict(up.id).tone]"
                :title="up.config.name"
                >{{
                  group.items.length > 1 ? up.config.name.slice(-2) : verdict(up.id).label
                }}</span
              >
            </div>
            <ChevronDownIcon
              class="h-4 w-4 shrink-0 text-gray-400 transition-transform"
              :class="collapsed.has(group.key) ? '-rotate-90' : ''"
              aria-hidden="true"
            />
          </button>
          <button
            type="button"
            class="text-brand-600 dark:text-brand-400 shrink-0 text-sm font-medium transition hover:underline disabled:opacity-50"
            :disabled="running"
            @click="runGroup(group.items)"
          >
            检测本组
          </button>
        </div>

        <div v-show="!collapsed.has(group.key)">
          <div
            v-if="callCheck && probes[group.label.toLowerCase()]"
            class="grid grid-cols-1 gap-3 border-b border-gray-100 bg-gray-50/60 px-4 py-3 sm:px-5 lg:grid-cols-[minmax(0,16rem)_minmax(0,1fr)] dark:border-gray-800 dark:bg-white/[0.02]"
          >
            <label class="block text-xs text-gray-500">
              检测工具
              <select
                :value="probes[group.label.toLowerCase()]!.tool"
                :class="[inputClass, 'mt-1 h-9']"
                @change="
                  onProbeToolChange(
                    group.label.toLowerCase(),
                    ($event.target as HTMLSelectElement).value,
                  )
                "
              >
                <option
                  v-if="(toolsByService[group.label.toLowerCase()] ?? []).length === 0"
                  value=""
                >
                  （没有工具）
                </option>
                <option
                  v-for="tool in toolsByService[group.label.toLowerCase()] ?? []"
                  :key="tool.name"
                  :value="tool.name"
                >
                  {{ tool.name }}
                </option>
              </select>
            </label>
            <label class="block text-xs text-gray-500">
              <span class="flex justify-between">
                调用参数（JSON）
                <button
                  type="button"
                  class="text-brand-600 dark:text-brand-400"
                  @click="resetProbe(group.label.toLowerCase())"
                >
                  恢复默认
                </button>
              </span>
              <textarea
                v-model="probes[group.label.toLowerCase()]!.argsText"
                rows="3"
                :class="[inputClass, 'mt-1 py-2 font-mono text-xs']"
                spellcheck="false"
                @change="saveProbes"
              ></textarea>
              <span v-if="probeErrors[group.label.toLowerCase()]" class="text-error-500 mt-1 block">
                {{ probeErrors[group.label.toLowerCase()] }}
              </span>
            </label>
          </div>

          <div class="divide-y divide-gray-100 dark:divide-gray-800">
            <div
              v-for="up in group.items"
              :key="up.id"
              class="grid grid-cols-1 gap-2 px-4 py-3 sm:px-5 md:grid-cols-[minmax(0,12rem)_auto_minmax(0,1fr)] md:items-start"
            >
              <div class="flex min-w-0 items-center gap-2">
                <span class="truncate text-sm text-gray-800 dark:text-white/90">{{
                  up.config.name
                }}</span>
                <span v-if="!up.config.enabled" class="shrink-0 text-xs text-gray-400">已停用</span>
                <span
                  v-if="cooldownById.get(up.id)"
                  v-tooltip="cooldownById.get(up.id)!.message"
                  class="bg-warning-50 text-warning-700 dark:bg-warning-500/15 dark:text-warning-400 shrink-0 rounded-full px-2 py-0.5 text-[11px]"
                  >{{ accountEventKindLabel[cooldownById.get(up.id)!.kind] }} ·
                  {{ remaining(cooldownById.get(up.id)!.until) }}后恢复</span
                >
              </div>
              <span
                class="w-fit rounded-full px-2.5 py-0.5 text-xs font-medium"
                :class="toneClass[verdict(up.id).tone]"
              >
                {{ verdict(up.id).label }}
              </span>
              <div class="min-w-0 space-y-1 text-xs text-gray-500 dark:text-gray-400">
                <template v-if="resultOf(up.id)">
                  <p>
                    连接：
                    <template v-if="resultOf(up.id)!.connect.ok">
                      成功 · {{ resultOf(up.id)!.connect.count }} 个工具 ·
                      {{ resultOf(up.id)!.connect.durationMs }} ms
                    </template>
                    <span v-else class="text-error-600 dark:text-error-400 break-all">
                      失败（{{ resultOf(up.id)!.connect.stage }}）：{{
                        resultOf(up.id)!.connect.message
                      }}
                    </span>
                  </p>
                  <p v-if="resultOf(up.id)!.call">
                    调用 {{ resultOf(up.id)!.call!.tool }}：
                    <template v-if="resultOf(up.id)!.call!.ok"
                      >成功 · {{ resultOf(up.id)!.call!.latencyMs }} ms</template
                    >
                    <span v-else class="text-error-600 dark:text-error-400 break-all">{{
                      resultOf(up.id)!.call!.message || '失败'
                    }}</span>
                  </p>
                  <p
                    v-if="resultOf(up.id)!.call?.ok && resultOf(up.id)!.call?.preview"
                    class="line-clamp-2 break-all text-gray-400"
                  >
                    {{ resultOf(up.id)!.call!.preview }}
                  </p>
                </template>
                <p
                  v-else-if="failedMessage(up.id)"
                  class="text-error-600 dark:text-error-400 break-all"
                >
                  {{ failedMessage(up.id) }}
                </p>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>
  </AdminLayout>
</template>
