<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
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

const toast = useToast()

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

onMounted(load)

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
