<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AdminLayout from '@/components/layout/AdminLayout.vue'
import PageBreadcrumb from '@/components/common/PageBreadcrumb.vue'
import ConnStateBadge from '@/components/upstreams/ConnStateBadge.vue'
import { PlusIcon, RefreshIcon } from '@/icons'
import {
  assignOutboundProxy,
  createOutboundProxy,
  deleteOutboundProxy,
  distributeOutboundProxies,
  listOutboundProxies,
  testOutboundProxy,
  updateOutboundProxy,
  type OutboundProxy,
  type OutboundProxyAssignResult,
  type OutboundProxyTestResult,
} from '@/api/outboundProxies'
import { listUpstreams, type Upstream } from '@/api/upstreams'
import { ApiError } from '@/api/request'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { serviceNameOf } from '@/utils/toolAccessGroups'

const toast = useToast()
const { confirm } = useConfirm()

const loading = ref(false)
const loadError = ref('')
const proxies = ref<OutboundProxy[]>([])
const upstreams = ref<Upstream[]>([])
const testResults = reactive<Record<string, OutboundProxyTestResult | 'testing'>>({})
const savingRow = reactive<Record<string, boolean>>({})

// ── 代理编辑弹窗 ────────────────────────────────────────────────────────────
const editorOpen = ref(false)
const editingId = ref<string | null>(null)
const editorSaving = ref(false)
const form = reactive({ name: '', url: '', note: '', enabled: true })
const formErrors = reactive<Record<string, string>>({})

// ── 分配 ────────────────────────────────────────────────────────────────────
const selected = ref<Set<string>>(new Set())
const bulkProxyId = ref('')
const distributeOpen = ref(false)
const distributeProxyIds = ref<string[]>([])
const bulkBusy = ref(false)

const proxyById = computed(() => new Map(proxies.value.map((p) => [p.id, p])))
const enabledProxies = computed(() => proxies.value.filter((p) => p.enabled))

interface UpstreamGroup {
  key: string
  label: string
  items: Upstream[]
}

const groups = computed<UpstreamGroup[]>(() => {
  const map = new Map<string, UpstreamGroup>()
  for (const up of upstreams.value) {
    const label = serviceNameOf(up.config.name)
    const key = label.toLowerCase()
    let group = map.get(key)
    if (!group) {
      group = { key, label, items: [] }
      map.set(key, group)
    }
    group.items.push(up)
  }
  const out = Array.from(map.values())
  for (const g of out)
    g.items.sort((a, b) => a.config.name.localeCompare(b.config.name, undefined, { numeric: true }))
  return out.sort((a, b) => a.label.localeCompare(b.label))
})

function proxyIdOf(up: Upstream): string {
  const id = up.config.connParams?.proxyId
  return typeof id === 'string' ? id : ''
}

function proxyLabel(id: string): string {
  if (id === '') return '直连'
  return proxyById.value.get(id)?.name ?? '未知代理'
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message !== '' ? err.message : fallback
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [p, u] = await Promise.all([listOutboundProxies(), listUpstreams()])
    proxies.value = p
    upstreams.value = u
    const ids = new Set(u.map((x) => x.id))
    selected.value = new Set(Array.from(selected.value).filter((id) => ids.has(id)))
  } catch (err) {
    loadError.value = errorText(err, '加载出站代理失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)

// ── 代理增删改 ──────────────────────────────────────────────────────────────
function openEditor(p?: OutboundProxy): void {
  editingId.value = p?.id ?? null
  form.name = p?.name ?? ''
  form.url = p?.url ?? ''
  form.note = p?.note ?? ''
  form.enabled = p?.enabled ?? true
  for (const k of Object.keys(formErrors)) delete formErrors[k]
  editorOpen.value = true
}

async function saveEditor(): Promise<void> {
  for (const k of Object.keys(formErrors)) delete formErrors[k]
  editorSaving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      url: form.url.trim(),
      note: form.note.trim(),
      enabled: form.enabled,
    }
    if (editingId.value) await updateOutboundProxy(editingId.value, payload)
    else await createOutboundProxy(payload)
    toast.success(editingId.value ? '代理已更新' : '代理已添加')
    editorOpen.value = false
    await load()
  } catch (err) {
    if (err instanceof ApiError && Object.keys(err.fields).length > 0)
      Object.assign(formErrors, err.fields)
    else toast.error(errorText(err, '保存代理失败'))
  } finally {
    editorSaving.value = false
  }
}

async function toggleProxy(p: OutboundProxy): Promise<void> {
  if (p.enabled && p.upstreamIds.length > 0) {
    const ok = await confirm({
      title: '确认停用代理',
      message: `有 ${p.upstreamIds.length} 个上游在使用「${p.name}」。停用后这些上游会连接失败（不会改用直连），轮询会跳到同组其他账号。`,
      confirmText: '停用',
      tone: 'warning',
    })
    if (!ok) return
  }
  try {
    await updateOutboundProxy(p.id, { name: p.name, url: p.url, note: p.note, enabled: !p.enabled })
    toast.success(p.enabled ? '代理已停用' : '代理已启用')
    await load()
  } catch (err) {
    toast.error(errorText(err, '更新代理失败'))
  }
}

async function removeProxy(p: OutboundProxy): Promise<void> {
  const ok = await confirm({
    title: '确认删除代理',
    message: `确定删除「${p.name}」吗？此操作不可恢复。`,
    confirmText: '删除',
    tone: 'danger',
  })
  if (!ok) return
  try {
    await deleteOutboundProxy(p.id)
    toast.success('代理已删除')
    await load()
  } catch (err) {
    toast.error(errorText(err, '删除代理失败'))
  }
}

async function runTest(p: OutboundProxy): Promise<void> {
  testResults[p.id] = 'testing'
  try {
    testResults[p.id] = await testOutboundProxy(p.id)
  } catch (err) {
    testResults[p.id] = { ok: false, latencyMs: 0, message: errorText(err, '检测失败') }
  }
}

function testResultOf(id: string): OutboundProxyTestResult | null {
  const r = testResults[id]
  return r === undefined || r === 'testing' ? null : r
}

// ── 分配 ────────────────────────────────────────────────────────────────────
function reportAssign(result: OutboundProxyAssignResult, action: string): void {
  const failed = Object.entries(result.failed ?? {})
  if (failed.length === 0) toast.success(`${action}：已更新 ${result.updated} 个上游`)
  else
    toast.error(
      `${action}：成功 ${result.updated} 个，失败 ${failed.length} 个（${failed.map(([n, m]) => `${n}：${m}`).join('；')}）`,
    )
}

async function assignOne(up: Upstream, proxyId: string): Promise<void> {
  if (proxyId === proxyIdOf(up)) return
  savingRow[up.id] = true
  try {
    reportAssign(
      await assignOutboundProxy([up.id], proxyId),
      `${up.config.name} 改为${proxyLabel(proxyId)}`,
    )
    await load()
  } catch (err) {
    toast.error(errorText(err, '分配代理失败'))
  } finally {
    savingRow[up.id] = false
  }
}

function isSelected(id: string): boolean {
  return selected.value.has(id)
}

function toggleSelected(id: string): void {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function groupState(g: UpstreamGroup): 'all' | 'some' | 'none' {
  const n = g.items.filter((u) => selected.value.has(u.id)).length
  return n === 0 ? 'none' : n === g.items.length ? 'all' : 'some'
}

function toggleGroup(g: UpstreamGroup): void {
  const next = new Set(selected.value)
  const selectAll = groupState(g) !== 'all'
  for (const u of g.items) {
    if (selectAll) next.add(u.id)
    else next.delete(u.id)
  }
  selected.value = next
}

/** 按页面展示顺序返回已选上游，保证轮流分配的顺序与看到的一致。 */
function orderedSelection(): string[] {
  return groups.value.flatMap((g) =>
    g.items.filter((u) => selected.value.has(u.id)).map((u) => u.id),
  )
}

async function applyBulk(): Promise<void> {
  const ids = orderedSelection()
  if (ids.length === 0) return
  bulkBusy.value = true
  try {
    reportAssign(
      await assignOutboundProxy(ids, bulkProxyId.value),
      `批量设为${proxyLabel(bulkProxyId.value)}`,
    )
    selected.value = new Set()
    await load()
  } catch (err) {
    toast.error(errorText(err, '批量分配失败'))
  } finally {
    bulkBusy.value = false
  }
}

function openDistribute(): void {
  distributeProxyIds.value = enabledProxies.value.map((p) => p.id)
  distributeOpen.value = true
}

function toggleDistributeProxy(id: string): void {
  const list = distributeProxyIds.value
  distributeProxyIds.value = list.includes(id) ? list.filter((x) => x !== id) : [...list, id]
}

const distributePreview = computed(() => {
  const ids = orderedSelection()
  const order = enabledProxies.value.filter((p) => distributeProxyIds.value.includes(p.id))
  if (order.length === 0) return []
  const nameOf = new Map(upstreams.value.map((u) => [u.id, u.config.name]))
  return ids.map((id, i) => ({
    upstream: nameOf.get(id) ?? id,
    proxy: order[i % order.length]!.name,
  }))
})

async function applyDistribute(): Promise<void> {
  const ids = orderedSelection()
  const order = enabledProxies.value
    .filter((p) => distributeProxyIds.value.includes(p.id))
    .map((p) => p.id)
  if (ids.length === 0 || order.length === 0) return
  bulkBusy.value = true
  try {
    reportAssign(await distributeOutboundProxies(ids, order), '轮流分配')
    distributeOpen.value = false
    selected.value = new Set()
    await load()
  } catch (err) {
    toast.error(errorText(err, '轮流分配失败'))
  } finally {
    bulkBusy.value = false
  }
}

const cardClass =
  'rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-800 dark:bg-white/[0.03]'
const inputClass =
  'h-11 w-full rounded-lg border border-gray-300 bg-transparent px-4 text-sm text-gray-800 shadow-sm placeholder:text-gray-400 focus:border-brand-300 focus:ring-3 focus:ring-brand-500/10 focus:outline-none dark:border-gray-700 dark:text-white/90 dark:placeholder:text-white/30'
const selectClass =
  'h-9 w-full rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-800 focus:border-brand-300 focus:outline-none disabled:opacity-60 sm:w-48 dark:border-gray-700 dark:bg-gray-900 dark:text-white/90'
const labelClass = 'mb-1.5 block text-sm font-medium text-gray-700 dark:text-gray-400'
const btnPrimary =
  'bg-brand-500 hover:bg-brand-600 inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-medium text-white transition disabled:bg-gray-300 disabled:text-gray-500 dark:disabled:bg-gray-700 dark:disabled:text-gray-400'
const btnOutline =
  'inline-flex items-center justify-center gap-2 rounded-lg border border-gray-300 px-4 py-2.5 text-sm font-medium text-gray-700 transition hover:bg-gray-50 disabled:opacity-60 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-white/[0.03]'
const linkBtn = 'text-sm font-medium transition disabled:opacity-50'
</script>

<template>
  <AdminLayout>
    <PageBreadcrumb pageTitle="出站代理" />

    <p
      v-if="loadError !== ''"
      class="bg-error-50 text-error-600 dark:bg-error-500/10 dark:text-error-400 mb-4 rounded-lg px-4 py-2.5 text-sm"
    >
      {{ loadError }}
    </p>

    <!-- 代理列表 -->
    <section :class="cardClass">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">代理列表</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            网关访问上游时使用的出口。代理不可用时，使用它的上游会连接失败，不会改用直连。
          </p>
        </div>
        <div class="flex shrink-0 gap-2">
          <button
            type="button"
            :class="btnOutline"
            :disabled="loading"
            aria-label="刷新"
            @click="load"
          >
            <RefreshIcon class="h-4 w-4" aria-hidden="true" />
          </button>
          <button type="button" :class="btnPrimary" @click="openEditor()">
            <PlusIcon class="h-4 w-4" aria-hidden="true" />
            添加代理
          </button>
        </div>
      </div>

      <div
        v-if="!loading && proxies.length === 0"
        class="mt-5 rounded-xl border border-dashed border-gray-300 px-4 py-10 text-center text-sm text-gray-400 dark:border-gray-700"
      >
        还没有代理。添加后即可在下方把上游分配给它。
      </div>
      <div v-else class="mt-5 grid grid-cols-1 gap-3 lg:grid-cols-2 2xl:grid-cols-3">
        <article
          v-for="p in proxies"
          :key="p.id"
          class="flex flex-col gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-800"
          :class="p.enabled ? '' : 'opacity-70'"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="truncate text-sm font-semibold text-gray-800 dark:text-white/90">{{
                  p.name
                }}</span>
                <span
                  class="rounded-full px-2 py-0.5 text-xs font-medium"
                  :class="
                    p.enabled
                      ? 'bg-success-50 text-success-700 dark:bg-success-500/10 dark:text-success-400'
                      : 'bg-gray-100 text-gray-500 dark:bg-white/5 dark:text-gray-400'
                  "
                  >{{ p.enabled ? '启用' : '已停用' }}</span
                >
              </div>
              <p class="mt-1 font-mono text-xs break-all text-gray-500 dark:text-gray-400">
                {{ p.displayUrl }}
              </p>
              <p v-if="p.note" class="mt-1 text-xs text-gray-400">{{ p.note }}</p>
            </div>
            <span class="shrink-0 text-xs text-gray-400">{{ p.upstreamIds.length }} 个上游</span>
          </div>

          <div class="rounded-lg bg-gray-50 px-3 py-2 text-xs dark:bg-white/[0.03]">
            <template v-if="testResults[p.id] === 'testing'">
              <span class="text-gray-500">检测中…</span>
            </template>
            <template v-else-if="testResultOf(p.id)?.ok">
              <span class="text-success-600 dark:text-success-400 font-medium">可用</span>
              <span class="text-gray-500">
                · {{ testResultOf(p.id)!.latencyMs }} ms · 出口 IP
                <span class="font-mono text-gray-700 dark:text-gray-300">{{
                  testResultOf(p.id)!.exitIp
                }}</span>
              </span>
            </template>
            <template v-else-if="testResultOf(p.id)">
              <span class="text-error-600 dark:text-error-400 font-medium">不可用</span>
              <span class="break-all text-gray-500"> · {{ testResultOf(p.id)!.message }}</span>
            </template>
            <span v-else class="text-gray-400">未检测</span>
          </div>

          <div class="flex flex-wrap gap-x-4 gap-y-1">
            <button
              type="button"
              :class="[linkBtn, 'text-brand-600 dark:text-brand-400']"
              :disabled="testResults[p.id] === 'testing'"
              @click="runTest(p)"
            >
              检测
            </button>
            <button
              type="button"
              :class="[linkBtn, 'text-gray-600 dark:text-gray-300']"
              @click="openEditor(p)"
            >
              编辑
            </button>
            <button
              type="button"
              :class="[linkBtn, 'text-gray-600 dark:text-gray-300']"
              @click="toggleProxy(p)"
            >
              {{ p.enabled ? '停用' : '启用' }}
            </button>
            <button
              type="button"
              :class="[linkBtn, 'text-error-600 dark:text-error-400']"
              @click="removeProxy(p)"
            >
              删除
            </button>
          </div>
        </article>
      </div>
    </section>

    <!-- 分配上游 -->
    <section :class="cardClass" class="mt-6">
      <div>
        <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">分配上游</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          每个上游单独选择出口；勾选多个后可以批量设置，或在几个代理之间轮流分配，让同一服务的不同账号走不同出口。
        </p>
      </div>

      <div
        v-if="selected.size > 0"
        class="border-brand-200 bg-brand-50/60 dark:border-brand-500/30 dark:bg-brand-500/10 mt-4 flex flex-col gap-3 rounded-xl border p-3 lg:flex-row lg:items-center lg:justify-between"
      >
        <span class="text-sm text-gray-700 dark:text-gray-300"
          >已选 {{ selected.size }} 个上游</span
        >
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
          <select v-model="bulkProxyId" :class="selectClass" aria-label="批量设置的代理">
            <option value="">直连</option>
            <option v-for="p in enabledProxies" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
          <button type="button" :class="btnPrimary" :disabled="bulkBusy" @click="applyBulk">
            批量设置
          </button>
          <button
            type="button"
            :class="btnOutline"
            :disabled="bulkBusy || enabledProxies.length === 0"
            @click="openDistribute"
          >
            轮流分配…
          </button>
          <button type="button" :class="[linkBtn, 'text-gray-500']" @click="selected = new Set()">
            取消选择
          </button>
        </div>
      </div>

      <div
        v-if="!loading && groups.length === 0"
        class="mt-5 rounded-xl border border-dashed border-gray-300 px-4 py-10 text-center text-sm text-gray-400 dark:border-gray-700"
      >
        还没有上游 MCP。
      </div>
      <div v-else class="mt-4 space-y-3">
        <div
          v-for="g in groups"
          :key="g.key"
          class="overflow-hidden rounded-xl border border-gray-200 dark:border-gray-800"
        >
          <div class="flex items-center gap-3 bg-gray-50 px-3 py-2.5 dark:bg-white/[0.02]">
            <button
              type="button"
              class="flex h-5 w-5 shrink-0 items-center justify-center rounded border text-xs"
              :class="
                groupState(g) === 'none'
                  ? 'border-gray-300 dark:border-gray-600'
                  : 'border-brand-500 bg-brand-500 text-white'
              "
              :aria-label="`选择 ${g.label} 全部上游`"
              @click="toggleGroup(g)"
            >
              {{ groupState(g) === 'all' ? '✓' : groupState(g) === 'some' ? '–' : '' }}
            </button>
            <span class="text-sm font-medium text-gray-800 dark:text-white/90">{{ g.label }}</span>
            <span class="text-xs text-gray-400">{{ g.items.length }} 个账号</span>
          </div>
          <div class="divide-y divide-gray-100 dark:divide-gray-800">
            <div
              v-for="up in g.items"
              :key="up.id"
              class="flex flex-col gap-2 px-3 py-2.5 sm:flex-row sm:items-center sm:justify-between"
              :class="isSelected(up.id) ? 'bg-brand-50/40 dark:bg-brand-500/5' : ''"
            >
              <label class="flex min-w-0 cursor-pointer items-center gap-3">
                <input
                  type="checkbox"
                  class="accent-brand-500 h-4 w-4 shrink-0"
                  :checked="isSelected(up.id)"
                  @change="toggleSelected(up.id)"
                />
                <span class="truncate text-sm text-gray-800 dark:text-white/90">{{
                  up.config.name
                }}</span>
                <ConnStateBadge :state="up.state" />
                <span v-if="!up.config.enabled" class="text-xs text-gray-400">已停用</span>
              </label>
              <div class="flex items-center gap-2 pl-7 sm:pl-0">
                <select
                  :value="proxyIdOf(up)"
                  :class="selectClass"
                  :disabled="savingRow[up.id]"
                  :aria-label="`${up.config.name} 的出站代理`"
                  @change="assignOne(up, ($event.target as HTMLSelectElement).value)"
                >
                  <option value="">直连</option>
                  <option
                    v-for="p in proxies"
                    :key="p.id"
                    :value="p.id"
                    :disabled="!p.enabled && p.id !== proxyIdOf(up)"
                  >
                    {{ p.name }}{{ p.enabled ? '' : '（已停用）' }}
                  </option>
                </select>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 代理编辑弹窗 -->
    <div
      v-if="editorOpen"
      class="fixed inset-0 z-[100000] flex items-center justify-center bg-gray-900/40 p-4 backdrop-blur-[1px]"
    >
      <form
        class="w-full max-w-lg rounded-2xl border border-gray-200 bg-white p-6 shadow-xl dark:border-gray-800 dark:bg-gray-900"
        @submit.prevent="saveEditor"
      >
        <h3 class="text-lg font-semibold text-gray-800 dark:text-white/90">
          {{ editingId ? '编辑代理' : '添加代理' }}
        </h3>
        <div class="mt-5 space-y-4">
          <div>
            <label for="proxy-name" :class="labelClass">名称</label>
            <input
              id="proxy-name"
              v-model="form.name"
              type="text"
              :class="inputClass"
              placeholder="例如：家里-1"
            />
            <p v-if="formErrors.name" class="text-error-500 mt-1 text-xs">{{ formErrors.name }}</p>
          </div>
          <div>
            <label for="proxy-url" :class="labelClass">代理地址</label>
            <input
              id="proxy-url"
              v-model="form.url"
              type="text"
              autocomplete="off"
              :class="inputClass"
              placeholder="socks5://用户名:密码@代理地址:1080"
            />
            <p class="mt-1 text-xs text-gray-400">
              支持 http://、https://、socks5://、socks5h://。网关在 Docker 中运行时，本机代理请用
              host.docker.internal 代替 127.0.0.1。
            </p>
            <p v-if="formErrors.url" class="text-error-500 mt-1 text-xs">{{ formErrors.url }}</p>
          </div>
          <div>
            <label for="proxy-note" :class="labelClass">备注</label>
            <input
              id="proxy-note"
              v-model="form.note"
              type="text"
              :class="inputClass"
              placeholder="可选"
            />
            <p v-if="formErrors.note" class="text-error-500 mt-1 text-xs">{{ formErrors.note }}</p>
          </div>
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.enabled" type="checkbox" class="accent-brand-500 h-4 w-4" />
            启用
          </label>
        </div>
        <div class="mt-6 flex justify-end gap-2">
          <button type="button" :class="btnOutline" @click="editorOpen = false">取消</button>
          <button type="submit" :class="btnPrimary" :disabled="editorSaving">
            {{ editorSaving ? '保存中…' : '保存' }}
          </button>
        </div>
      </form>
    </div>

    <!-- 轮流分配弹窗 -->
    <div
      v-if="distributeOpen"
      class="fixed inset-0 z-[100000] flex items-center justify-center bg-gray-900/40 p-4 backdrop-blur-[1px]"
    >
      <div
        class="flex max-h-[88vh] w-full max-w-lg flex-col rounded-2xl border border-gray-200 bg-white p-6 shadow-xl dark:border-gray-800 dark:bg-gray-900"
      >
        <h3 class="text-lg font-semibold text-gray-800 dark:text-white/90">轮流分配</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          已选的 {{ selected.size }} 个上游按顺序依次分到下面勾选的代理，代理用完后从头再来。
        </p>
        <div class="mt-4 flex flex-wrap gap-2">
          <button
            v-for="p in enabledProxies"
            :key="p.id"
            type="button"
            class="rounded-lg border px-3 py-1.5 text-sm transition"
            :class="
              distributeProxyIds.includes(p.id)
                ? 'border-brand-400 bg-brand-50 text-brand-700 dark:border-brand-500/50 dark:bg-brand-500/10 dark:text-brand-300'
                : 'border-gray-200 text-gray-600 hover:bg-gray-50 dark:border-gray-700 dark:text-gray-300 dark:hover:bg-white/[0.03]'
            "
            :aria-pressed="distributeProxyIds.includes(p.id)"
            @click="toggleDistributeProxy(p.id)"
          >
            {{ p.name }}
          </button>
        </div>
        <div
          class="custom-scrollbar mt-4 min-h-0 flex-1 overflow-y-auto rounded-lg bg-gray-50 p-3 dark:bg-white/[0.03]"
        >
          <p v-if="distributePreview.length === 0" class="text-sm text-gray-400">
            请至少勾选一个代理
          </p>
          <div
            v-for="row in distributePreview"
            :key="row.upstream"
            class="flex justify-between gap-3 py-1 text-sm"
          >
            <span class="truncate text-gray-700 dark:text-gray-300">{{ row.upstream }}</span>
            <span class="shrink-0 text-gray-500">→ {{ row.proxy }}</span>
          </div>
        </div>
        <div class="mt-5 flex justify-end gap-2">
          <button type="button" :class="btnOutline" @click="distributeOpen = false">取消</button>
          <button
            type="button"
            :class="btnPrimary"
            :disabled="bulkBusy || distributePreview.length === 0"
            @click="applyDistribute"
          >
            {{ bulkBusy ? '分配中…' : '确认分配' }}
          </button>
        </div>
      </div>
    </div>
  </AdminLayout>
</template>
