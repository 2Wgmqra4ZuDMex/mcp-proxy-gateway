<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import AdminLayout from '@/components/layout/AdminLayout.vue'
import PageBreadcrumb from '@/components/common/PageBreadcrumb.vue'
import { ChevronDownIcon, RefreshIcon } from '@/icons'
import { listOutboundProxies, type OutboundProxy } from '@/api/outboundProxies'
import { listAPIKeys, type APIKey } from '@/api/apikeys'
import {
  getSmartDiscovery,
  listEnrichedTools,
  listSmartDiscoveryModels,
  previewSmartSearch,
  saveSmartDiscovery,
  startSmartDiscoveryEnrich,
  testSmartDiscovery,
  type APIStyle,
  type EnrichedTool,
  type SmartDiscoveryConfig,
  type SmartDiscoveryInput,
  type SmartDiscoveryPreview,
  type SmartDiscoveryStatus,
  type SmartDiscoveryTestResult,
} from '@/api/smartDiscovery'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'

const toast = useToast()
const { confirm } = useConfirm()

const loading = ref(true)
const loadError = ref('')
const config = ref<SmartDiscoveryConfig | null>(null)
const status = ref<SmartDiscoveryStatus | null>(null)
const proxies = ref<OutboundProxy[]>([])
const apiKeys = ref<APIKey[]>([])

const form = reactive<SmartDiscoveryInput>({
  baseUrl: '',
  apiStyle: 'chat_completions',
  model: '',
  apiKey: '',
  proxyId: '',
  timeoutS: 60,
  enrichEnabled: false,
  rerankEnabled: false,
  rerankModel: '',
  fallbackModels: [],
})
const saving = ref(false)
const availableModels = ref<string[]>([])
const fetchingModels = ref(false)
const modelsError = ref('')
const newFallbackModel = ref('')
const testing = ref(false)
const testResult = ref<SmartDiscoveryTestResult | null>(null)
const testError = ref('')
const formErrors = reactive<Record<string, string>>({})

const enrichedTools = ref<EnrichedTool[]>([])
const serviceSummaries = ref<Record<string, string>>({})
const toolFilter = ref('')
const expandedServices = ref<Set<string>>(new Set())

const previewQuery = ref('')
const previewKeyId = ref('')
const previewing = ref(false)
const preview = ref<SmartDiscoveryPreview | null>(null)
const previewSamples = [
  '抓取网页内容',
  'react useEffect 怎么用',
  '查一下 GitHub 仓库的文档',
  '搜索最新的新闻',
  '论文',
]

let pollTimer: ReturnType<typeof setInterval> | null = null

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback
}

function applyConfig(next: SmartDiscoveryConfig): void {
  config.value = next
  form.baseUrl = next.baseUrl
  form.apiStyle = next.apiStyle || 'chat_completions'
  form.model = next.model
  form.apiKey = ''
  form.proxyId = next.proxyId
  form.timeoutS = next.timeoutS || 60
  form.enrichEnabled = next.enrichEnabled
  form.rerankEnabled = next.rerankEnabled
  form.rerankModel = next.rerankModel ?? ''
  form.fallbackModels = [...(next.fallbackModels ?? [])]
}

function formPayload(): SmartDiscoveryInput {
  return {
    ...form,
    baseUrl: form.baseUrl.trim(),
    model: form.model.trim(),
    rerankModel: form.rerankModel.trim(),
    fallbackModels: [...form.fallbackModels],
  }
}

async function fetchModels(): Promise<void> {
  modelsError.value = ''
  if (form.baseUrl.trim() === '') {
    modelsError.value = '请先填写接口地址'
    return
  }
  if (!config.value?.hasKey && form.apiKey.trim() === '') {
    modelsError.value = '请先填写 API Key'
    return
  }
  fetchingModels.value = true
  try {
    availableModels.value = await listSmartDiscoveryModels(formPayload())
    if (availableModels.value.length === 0)
      modelsError.value = '接口没有返回任何模型，可以直接手动填写'
    else toast.success(`获取到 ${availableModels.value.length} 个模型`)
  } catch (err) {
    modelsError.value = errorText(err, '获取模型列表失败，可以直接手动填写')
  } finally {
    fetchingModels.value = false
  }
}

function addFallbackModel(): void {
  const name = newFallbackModel.value.trim()
  if (name === '') return
  const first = form.rerankModel.trim() || form.model.trim()
  if (name === first) {
    toast.error('备用模型不能和挑选使用的模型相同')
    return
  }
  if (form.fallbackModels.includes(name)) {
    toast.error('已在备用模型中')
    return
  }
  if (form.fallbackModels.length >= 5) {
    toast.error('最多 5 个备用模型')
    return
  }
  form.fallbackModels.push(name)
  newFallbackModel.value = ''
}

function removeFallbackModel(index: number): void {
  form.fallbackModels.splice(index, 1)
}

function moveFallbackModel(index: number, delta: number): void {
  const target = index + delta
  if (target < 0 || target >= form.fallbackModels.length) return
  const list = [...form.fallbackModels]
  ;[list[index], list[target]] = [list[target]!, list[index]!]
  form.fallbackModels = list
}

async function loadTools(): Promise<void> {
  try {
    const data = await listEnrichedTools()
    enrichedTools.value = data.tools ?? []
    serviceSummaries.value = data.services ?? {}
  } catch {
    // 列表加载失败不影响配置。
  }
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [state, proxyList, keys] = await Promise.all([
      getSmartDiscovery(),
      listOutboundProxies().catch(() => [] as OutboundProxy[]),
      listAPIKeys().catch(() => [] as APIKey[]),
    ])
    applyConfig(state.config)
    status.value = state.status
    proxies.value = proxyList
    apiKeys.value = keys
    await loadTools()
    syncPolling()
  } catch (err) {
    loadError.value = errorText(err, '加载失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
onBeforeUnmount(stopPolling)

function stopPolling(): void {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function syncPolling(): void {
  if (status.value?.running) {
    if (pollTimer === null) pollTimer = setInterval(refreshStatus, 2000)
  } else {
    stopPolling()
  }
}

async function refreshStatus(): Promise<void> {
  try {
    const wasRunning = status.value?.running ?? false
    const state = await getSmartDiscovery()
    status.value = state.status
    if (wasRunning && !state.status.running) {
      await loadTools()
      if (state.status.lastError) toast.error(`生成完成，但有问题：${state.status.lastError}`)
      else toast.success(`生成完成：新增 ${state.status.lastAdded ?? 0} 个工具说明`)
    }
    syncPolling()
  } catch {
    stopPolling()
  }
}

function validate(): boolean {
  for (const key of Object.keys(formErrors)) delete formErrors[key]
  if (form.baseUrl.trim() === '') formErrors.baseUrl = '请填写接口地址'
  else if (!/^https?:\/\//i.test(form.baseUrl.trim()))
    formErrors.baseUrl = '地址需以 http:// 或 https:// 开头'
  if (form.model.trim() === '') formErrors.model = '请填写模型名称'
  if (!config.value?.hasKey && form.apiKey.trim() === '') formErrors.apiKey = '请填写 API Key'
  if (!(form.timeoutS >= 1 && form.timeoutS <= 300)) formErrors.timeoutS = '1～300 秒'
  return Object.keys(formErrors).length === 0
}

async function save(): Promise<void> {
  if (!validate()) return
  saving.value = true
  try {
    const state = await saveSmartDiscovery(formPayload())
    applyConfig(state.config)
    status.value = state.status
    toast.success('已保存')
    syncPolling()
  } catch (err) {
    toast.error(errorText(err, '保存失败'))
  } finally {
    saving.value = false
  }
}

async function runTest(): Promise<void> {
  if (!validate()) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    testResult.value = await testSmartDiscovery(formPayload())
  } catch (err) {
    testError.value = errorText(err, '连接失败')
  } finally {
    testing.value = false
  }
}

async function enrich(force: boolean): Promise<void> {
  if (force) {
    const ok = await confirm({
      title: '全部重新生成',
      message: `会为全部 ${status.value?.total ?? 0} 个工具重新调用模型生成说明，消耗对应的模型额度。`,
      confirmText: '重新生成',
      tone: 'warning',
    })
    if (!ok) return
  }
  try {
    status.value = await startSmartDiscoveryEnrich(force)
    toast.success('已开始生成')
    syncPolling()
  } catch (err) {
    toast.error(errorText(err, '启动失败'))
  }
}

async function runPreview(query?: string): Promise<void> {
  if (query !== undefined) previewQuery.value = query
  const q = previewQuery.value.trim()
  if (q === '') return
  previewing.value = true
  try {
    preview.value = await previewSmartSearch(q, previewKeyId.value)
  } catch (err) {
    toast.error(errorText(err, '搜索失败'))
  } finally {
    previewing.value = false
  }
}

const savedDiffers = computed(() => {
  const c = config.value
  if (c === null) return false
  return (
    form.baseUrl !== c.baseUrl ||
    form.apiStyle !== c.apiStyle ||
    form.model !== c.model ||
    form.apiKey !== '' ||
    form.proxyId !== c.proxyId ||
    form.timeoutS !== c.timeoutS ||
    form.enrichEnabled !== c.enrichEnabled ||
    form.rerankEnabled !== c.rerankEnabled ||
    form.rerankModel !== (c.rerankModel ?? '') ||
    form.fallbackModels.join('\n') !== (c.fallbackModels ?? []).join('\n')
  )
})

const progressPercent = computed(() => {
  const s = status.value
  if (!s || s.progressTotal === 0) return 0
  return Math.round((s.progress / s.progressTotal) * 100)
})

const coveragePercent = computed(() => {
  const s = status.value
  if (!s || s.total === 0) return 0
  return Math.round((s.enriched / s.total) * 100)
})

const toolGroups = computed(() => {
  const q = toolFilter.value.trim().toLowerCase()
  const map = new Map<string, EnrichedTool[]>()
  for (const tool of enrichedTools.value) {
    if (q !== '') {
      const hay = [tool.name, tool.service, tool.summary, tool.useWhen ?? '', ...tool.keywords]
        .join(' ')
        .toLowerCase()
      if (!hay.includes(q)) continue
    }
    const list = map.get(tool.service) ?? []
    list.push(tool)
    map.set(tool.service, list)
  }
  return Array.from(map.entries()).map(([service, tools]) => ({
    service,
    tools,
    enriched: tools.filter((t) => t.enriched).length,
    summary: serviceSummaries.value[service] ?? '',
  }))
})

function toggleService(service: string): void {
  const next = new Set(expandedServices.value)
  if (next.has(service)) next.delete(service)
  else next.add(service)
  expandedServices.value = next
}

function isExpanded(service: string): boolean {
  return toolFilter.value.trim() !== '' || expandedServices.value.has(service)
}

const apiStyleOptions: { value: APIStyle; label: string }[] = [
  { value: 'chat_completions', label: 'Chat Completions（绝大多数接口）' },
  { value: 'responses', label: 'Responses（OpenAI 新接口）' },
]

const cardClass =
  'rounded-2xl border border-gray-200 bg-white p-5 dark:border-gray-800 dark:bg-white/[0.03]'
const inputClass =
  'h-10 w-full rounded-lg border border-gray-300 bg-white px-3 text-sm text-gray-800 focus:border-brand-300 focus:outline-none dark:border-gray-700 dark:bg-gray-900 dark:text-white/90'
const labelClass = 'mb-1.5 block text-sm font-medium text-gray-700 dark:text-gray-300'
const btnPrimary =
  'bg-brand-500 hover:bg-brand-600 inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-medium text-white transition disabled:bg-gray-300 disabled:text-gray-500 dark:disabled:bg-gray-700 dark:disabled:text-gray-400'
const btnOutline =
  'inline-flex items-center justify-center gap-2 rounded-lg border border-gray-300 bg-white px-4 py-2.5 text-sm font-medium text-gray-700 transition hover:bg-gray-50 disabled:opacity-50 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-300 dark:hover:bg-white/[0.03]'
</script>

<template>
  <AdminLayout>
    <PageBreadcrumb pageTitle="智能发现" />

    <p
      v-if="loadError !== ''"
      class="bg-error-50 text-error-600 dark:bg-error-500/10 dark:text-error-400 mb-4 rounded-lg px-4 py-2.5 text-sm"
    >
      {{ loadError }}
    </p>
    <div v-if="loading" class="py-16 text-center text-sm text-gray-500">加载中…</div>

    <div v-else class="space-y-4">
      <!-- AI 接口与功能开关 -->
      <section :class="cardClass">
        <div class="flex flex-col gap-1">
          <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">AI 接口</h3>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            填写一个兼容 OpenAI
            格式的接口，用于帮智能模式找工具。不需要很强的模型，便宜的小模型就够用。
          </p>
        </div>

        <form class="mt-5 grid grid-cols-1 gap-4 md:grid-cols-2" @submit.prevent="save">
          <div class="md:col-span-2">
            <label :class="labelClass" for="sd-base-url">接口地址</label>
            <input
              id="sd-base-url"
              v-model="form.baseUrl"
              :class="inputClass"
              placeholder="例如 https://api.deepseek.com/v1"
              autocomplete="off"
              spellcheck="false"
            />
            <p v-if="formErrors.baseUrl" class="text-error-500 mt-1 text-xs">
              {{ formErrors.baseUrl }}
            </p>
            <p v-else class="mt-1 text-xs text-gray-400">
              填到 /v1 这一级即可，会自动拼接 /chat/completions。
            </p>
          </div>
          <div>
            <label :class="labelClass" for="sd-key">API Key</label>
            <input
              id="sd-key"
              v-model="form.apiKey"
              type="password"
              :class="inputClass"
              :placeholder="
                config?.hasKey ? `已保存（${config.keyHint || '已隐藏'}），留空不修改` : 'sk-…'
              "
              autocomplete="new-password"
            />
            <p v-if="formErrors.apiKey" class="text-error-500 mt-1 text-xs">
              {{ formErrors.apiKey }}
            </p>
          </div>
          <div>
            <div class="mb-1.5 flex items-center justify-between gap-2">
              <label
                class="block text-sm font-medium text-gray-700 dark:text-gray-300"
                for="sd-model"
                >模型</label
              >
              <button
                type="button"
                class="text-brand-600 dark:text-brand-400 text-xs font-medium hover:underline disabled:opacity-50"
                :disabled="fetchingModels"
                @click="fetchModels"
              >
                {{
                  fetchingModels
                    ? '获取中…'
                    : availableModels.length > 0
                      ? `重新获取（${availableModels.length} 个）`
                      : '获取模型列表'
                }}
              </button>
            </div>
            <input
              id="sd-model"
              v-model="form.model"
              :class="inputClass"
              list="sd-model-options"
              placeholder="例如 deepseek-chat、qwen-turbo"
              autocomplete="off"
              spellcheck="false"
            />
            <datalist id="sd-model-options">
              <option v-for="m in availableModels" :key="m" :value="m" />
            </datalist>
            <p v-if="formErrors.model" class="text-error-500 mt-1 text-xs">
              {{ formErrors.model }}
            </p>
            <p v-else-if="modelsError" class="text-warning-600 dark:text-warning-400 mt-1 text-xs">
              {{ modelsError }}
            </p>
            <p v-else-if="availableModels.length > 0" class="mt-1 text-xs text-gray-400">
              点击输入框可从列表中选择，也可以直接输入。
            </p>
          </div>
          <div>
            <label :class="labelClass" for="sd-style">接口格式</label>
            <select id="sd-style" v-model="form.apiStyle" :class="inputClass">
              <option v-for="opt in apiStyleOptions" :key="opt.value" :value="opt.value">
                {{ opt.label }}
              </option>
            </select>
          </div>
          <div class="grid grid-cols-[minmax(0,1fr)_7rem] gap-3">
            <div>
              <label :class="labelClass" for="sd-proxy">出站代理</label>
              <select id="sd-proxy" v-model="form.proxyId" :class="inputClass">
                <option value="">直连</option>
                <option v-for="p in proxies" :key="p.id" :value="p.id" :disabled="!p.enabled">
                  {{ p.name }}{{ p.enabled ? '' : '（已停用）' }}
                </option>
              </select>
            </div>
            <div>
              <label :class="labelClass" for="sd-timeout">超时（秒）</label>
              <input
                id="sd-timeout"
                v-model.number="form.timeoutS"
                type="number"
                min="1"
                max="300"
                :class="inputClass"
              />
              <p v-if="formErrors.timeoutS" class="text-error-500 mt-1 text-xs">
                {{ formErrors.timeoutS }}
              </p>
            </div>
          </div>

          <div class="grid grid-cols-1 gap-3 md:col-span-2 lg:grid-cols-2">
            <label
              class="flex cursor-pointer items-start gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-800"
              :class="
                form.enrichEnabled
                  ? 'border-brand-300 bg-brand-50/40 dark:border-brand-500/40 dark:bg-brand-500/5'
                  : ''
              "
            >
              <input
                v-model="form.enrichEnabled"
                type="checkbox"
                class="accent-brand-500 mt-0.5 h-4 w-4 shrink-0"
              />
              <span class="min-w-0">
                <span class="block text-sm font-medium text-gray-800 dark:text-white/90"
                  >工具说明增强</span
                >
                <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">
                  让 AI
                  为每个工具写中文简介、中英文关键词和适用场景，搜索时一起匹配，中文和换个说法也能搜到。每个工具只生成一次，新增工具会自动补上。
                </span>
              </span>
            </label>
            <label
              class="flex cursor-pointer items-start gap-3 rounded-xl border border-gray-200 p-4 dark:border-gray-800"
              :class="
                form.rerankEnabled
                  ? 'border-brand-300 bg-brand-50/40 dark:border-brand-500/40 dark:bg-brand-500/5'
                  : ''
              "
            >
              <input
                v-model="form.rerankEnabled"
                type="checkbox"
                class="accent-brand-500 mt-0.5 h-4 w-4 shrink-0"
              />
              <span class="min-w-0">
                <span class="block text-sm font-medium text-gray-800 dark:text-white/90"
                  >AI 挑选工具</span
                >
                <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">
                  客户端搜索工具时，让 AI
                  从候选里挑出最合适的几个，并说明调用顺序。每次搜索调用一次模型，多等 1～4
                  秒；搜索内容会发给模型服务商。模型都失败时自动改用关键词搜索。
                </span>
              </span>
            </label>
          </div>

          <div
            v-if="form.rerankEnabled"
            class="grid grid-cols-1 gap-4 rounded-xl bg-gray-50 p-4 md:col-span-2 lg:grid-cols-2 dark:bg-white/[0.02]"
          >
            <div>
              <label :class="labelClass" for="sd-rerank-model">挑选使用的模型</label>
              <input
                id="sd-rerank-model"
                v-model="form.rerankModel"
                :class="inputClass"
                list="sd-model-options"
                :placeholder="`留空则使用上面的模型（${form.model || '未填写'}）`"
                autocomplete="off"
                spellcheck="false"
              />
              <p class="mt-1 text-xs text-gray-400">
                挑选工具对速度敏感，可以选一个更快的小模型；工具说明仍用上面的模型生成。
              </p>
            </div>
            <div>
              <label :class="labelClass" for="sd-fallback-input">备用模型</label>
              <div class="flex gap-2">
                <input
                  id="sd-fallback-input"
                  v-model="newFallbackModel"
                  :class="inputClass"
                  list="sd-model-options"
                  placeholder="输入或选择模型后点添加"
                  autocomplete="off"
                  spellcheck="false"
                  @keydown.enter.prevent="addFallbackModel"
                />
                <button
                  type="button"
                  :class="[btnOutline, 'shrink-0 px-3']"
                  :disabled="newFallbackModel.trim() === ''"
                  @click="addFallbackModel"
                >
                  添加
                </button>
              </div>
              <p class="mt-1 text-xs text-gray-400">
                挑选模型失败、超时或回复无法解析时，按顺序依次改用，最多 5 个。
              </p>
              <ol v-if="form.fallbackModels.length > 0" class="mt-2 space-y-1.5">
                <li
                  v-for="(m, i) in form.fallbackModels"
                  :key="m"
                  class="flex items-center gap-2 rounded-lg bg-white px-3 py-1.5 text-sm dark:bg-gray-900"
                >
                  <span class="text-xs text-gray-400">{{ i + 1 }}</span>
                  <span
                    class="min-w-0 flex-1 truncate font-mono text-gray-800 dark:text-white/90"
                    >{{ m }}</span
                  >
                  <button
                    type="button"
                    class="px-1 text-xs text-gray-500 hover:text-gray-800 disabled:opacity-30 dark:hover:text-white"
                    :disabled="i === 0"
                    :aria-label="`${m} 上移`"
                    v-tooltip="'上移'"
                    @click="moveFallbackModel(i, -1)"
                  >
                    ↑
                  </button>
                  <button
                    type="button"
                    class="px-1 text-xs text-gray-500 hover:text-gray-800 disabled:opacity-30 dark:hover:text-white"
                    :disabled="i === form.fallbackModels.length - 1"
                    :aria-label="`${m} 下移`"
                    v-tooltip="'下移'"
                    @click="moveFallbackModel(i, 1)"
                  >
                    ↓
                  </button>
                  <button
                    type="button"
                    class="text-error-500 px-1 text-xs hover:underline"
                    :aria-label="`移除备用模型 ${m}`"
                    @click="removeFallbackModel(i)"
                  >
                    移除
                  </button>
                </li>
              </ol>
            </div>
          </div>

          <div class="flex flex-col gap-3 sm:flex-row sm:items-center md:col-span-2">
            <button type="submit" :class="btnPrimary" :disabled="saving">
              {{ saving ? '保存中…' : '保存' }}
            </button>
            <button type="button" :class="btnOutline" :disabled="testing" @click="runTest">
              {{ testing ? '测试中…' : '测试连接' }}
            </button>
            <span v-if="savedDiffers" class="text-warning-600 dark:text-warning-400 text-xs"
              >有未保存的修改</span
            >
            <span v-if="testResult" class="text-success-600 dark:text-success-400 text-sm">
              连接成功 · {{ testResult.latencyMs }} ms · 模型回复「{{ testResult.reply }}」
            </span>
            <span
              v-else-if="testError"
              class="text-error-600 dark:text-error-400 text-sm break-all"
              >{{ testError }}</span
            >
          </div>
        </form>
      </section>

      <!-- 生成进度 -->
      <section :class="cardClass">
        <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div class="min-w-0">
            <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">工具说明</h3>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
              已为
              <span class="font-medium text-gray-800 dark:text-white/90">{{
                status?.enriched ?? 0
              }}</span>
              / {{ status?.total ?? 0 }} 个工具、{{ status?.services ?? 0 }} 个服务生成说明。
              <template v-if="config?.configured && !config.enrichEnabled">
                「工具说明增强」未开启，已生成的说明暂不参与搜索。
              </template>
            </p>
            <div
              class="mt-3 h-2 w-full max-w-md overflow-hidden rounded-full bg-gray-100 dark:bg-white/5"
            >
              <div
                class="bg-brand-500 h-full rounded-full transition-all"
                :style="{ width: `${status?.running ? progressPercent : coveragePercent}%` }"
              ></div>
            </div>
            <p v-if="status?.running" class="mt-2 text-xs text-gray-500">
              生成中：{{ status.progress }} / {{ status.progressTotal }}
            </p>
            <p v-else-if="status?.lastRunAt" class="mt-2 text-xs text-gray-400">
              上次生成：{{ new Date(status.lastRunAt).toLocaleString('zh-CN') }} · 新增
              {{ status.lastAdded ?? 0 }} 个
              <template v-if="status.inputTokens">
                · 用量 {{ status.inputTokens + (status.outputTokens ?? 0) }} tokens</template
              >
            </p>
            <p
              v-if="status?.lastError"
              class="text-error-600 dark:text-error-400 mt-2 text-xs break-all"
            >
              {{ status.lastError }}
            </p>
          </div>
          <div class="flex shrink-0 flex-wrap gap-2">
            <button
              type="button"
              :class="btnPrimary"
              :disabled="!config?.configured || status?.running || (status?.pending ?? 0) === 0"
              @click="enrich(false)"
            >
              生成缺少的 {{ status?.pending ?? 0 }} 个
            </button>
            <button
              type="button"
              :class="btnOutline"
              :disabled="!config?.configured || status?.running"
              @click="enrich(true)"
            >
              全部重新生成
            </button>
          </div>
        </div>
        <p v-if="!config?.configured" class="mt-3 text-xs text-gray-400">
          先在上面填写并保存 AI 接口。
        </p>
      </section>

      <!-- 试搜 -->
      <section :class="cardClass">
        <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">试搜</h3>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          模拟客户端在智能模式下调用 search_tools，对比关键词搜索和 AI 挑选的结果。
        </p>
        <form class="mt-4 flex flex-col gap-3 md:flex-row" @submit.prevent="runPreview()">
          <select
            v-model="previewKeyId"
            :class="[inputClass, 'md:w-48']"
            aria-label="按哪个 API Key 的权限搜索"
          >
            <option value="">全局视角</option>
            <option v-for="k in apiKeys" :key="k.id" :value="k.id">{{ k.name }}</option>
          </select>
          <input
            v-model="previewQuery"
            :class="[inputClass, 'md:flex-1']"
            placeholder="像客户端那样描述需求，例如：抓取网页内容"
            aria-label="搜索内容"
          />
          <button
            type="submit"
            :class="btnPrimary"
            :disabled="previewing || previewQuery.trim() === ''"
          >
            {{ previewing ? '搜索中…' : '搜索' }}
          </button>
        </form>
        <div class="mt-2 flex flex-wrap gap-2">
          <button
            v-for="sample in previewSamples"
            :key="sample"
            type="button"
            class="rounded-full bg-gray-100 px-2.5 py-1 text-xs text-gray-600 transition hover:bg-gray-200 dark:bg-white/5 dark:text-gray-400 dark:hover:bg-white/10"
            @click="runPreview(sample)"
          >
            {{ sample }}
          </button>
        </div>

        <div v-if="preview" class="mt-4 grid grid-cols-1 gap-3 lg:grid-cols-2">
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-white/[0.02]">
            <p class="text-sm font-medium text-gray-800 dark:text-white/90">
              关键词搜索
              <span class="ml-1 text-xs font-normal text-gray-400"
                >{{ preview.keyword.tools.length }} 个结果</span
              >
            </p>
            <ol v-if="preview.keyword.tools.length > 0" class="mt-2 space-y-1.5">
              <li
                v-for="(tool, i) in preview.keyword.tools.slice(0, 8)"
                :key="tool.name"
                class="text-sm"
              >
                <span class="mr-1 text-gray-400">{{ i + 1 }}.</span>
                <span class="font-mono text-gray-800 dark:text-white/90">{{ tool.name }}</span>
                <span class="ml-1 text-xs text-gray-400">{{ tool.upstream }}</span>
                <span v-if="i < 3 && tool.related?.length" class="ml-1 text-xs text-gray-400"
                  >· 配合 {{ tool.related.join('、') }}</span
                >
              </li>
            </ol>
            <p v-else class="mt-2 text-sm text-gray-400">
              {{ preview.keyword.hint || '没有找到工具' }}
            </p>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-white/[0.02]">
            <p class="text-sm font-medium text-gray-800 dark:text-white/90">
              AI 挑选
              <span v-if="preview.ai" class="ml-1 text-xs font-normal text-gray-400"
                >{{ preview.aiLatencyMs }} ms<template v-if="preview.aiInfo?.model">
                  · {{ preview.aiInfo.model }}</template
                ><template v-if="preview.aiInfo?.cached"> · 缓存</template></span
              >
            </p>
            <p
              v-if="preview.aiInfo?.fallbackUsed"
              class="text-warning-600 dark:text-warning-400 mt-1 text-xs break-all"
            >
              主模型失败，已改用备用模型：{{ (preview.aiInfo.failures ?? []).join('；') }}
            </p>
            <p v-if="!preview.aiEnabled" class="mt-2 text-sm text-gray-400">
              未开启「AI 挑选工具」。
            </p>
            <p
              v-else-if="preview.aiError"
              class="text-error-600 dark:text-error-400 mt-2 text-sm break-all"
            >
              失败，客户端会改用关键词结果：{{ preview.aiError }}
            </p>
            <template v-else-if="preview.ai">
              <ol v-if="preview.ai.tools.length > 0" class="mt-2 space-y-1.5">
                <li v-for="(tool, i) in preview.ai.tools" :key="tool.name" class="text-sm">
                  <span class="mr-1 text-gray-400">{{ i + 1 }}.</span>
                  <span class="font-mono text-gray-800 dark:text-white/90">{{ tool.name }}</span>
                  <span class="ml-1 text-xs text-gray-400">{{ tool.upstream }}</span>
                </li>
              </ol>
              <p v-else class="mt-2 text-sm text-gray-400">
                AI 认为没有合适的工具，客户端会看到关键词结果。
              </p>
              <p v-if="preview.ai.hint" class="text-brand-600 dark:text-brand-400 mt-2 text-xs">
                {{ preview.ai.hint }}
              </p>
            </template>
          </div>
        </div>
      </section>

      <!-- 已生成的说明 -->
      <section :class="cardClass">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <h3 class="text-base font-semibold text-gray-800 dark:text-white/90">已生成的说明</h3>
          <div class="flex gap-2">
            <input
              v-model="toolFilter"
              :class="[inputClass, 'sm:w-64']"
              placeholder="筛选工具、关键词"
              aria-label="筛选工具说明"
            />
            <button
              type="button"
              :class="[btnOutline, 'px-3']"
              aria-label="刷新说明列表"
              v-tooltip="'刷新'"
              @click="loadTools"
            >
              <RefreshIcon class="h-4 w-4" aria-hidden="true" />
            </button>
          </div>
        </div>
        <p v-if="toolGroups.length === 0" class="mt-4 text-sm text-gray-400">没有工具。</p>
        <div v-else class="mt-4 space-y-2">
          <div
            v-for="group in toolGroups"
            :key="group.service"
            class="overflow-hidden rounded-xl border border-gray-100 dark:border-gray-800"
          >
            <button
              type="button"
              class="flex w-full items-center justify-between gap-3 px-4 py-3 text-left"
              :aria-expanded="isExpanded(group.service)"
              @click="toggleService(group.service)"
            >
              <span class="min-w-0">
                <span class="text-sm font-semibold text-gray-800 dark:text-white/90">{{
                  group.service
                }}</span>
                <span class="ml-2 text-xs text-gray-400"
                  >{{ group.enriched }} / {{ group.tools.length }} 已生成</span
                >
                <span
                  v-if="group.summary"
                  class="mt-0.5 block truncate text-xs text-gray-500 dark:text-gray-400"
                >
                  {{ group.summary }}
                </span>
              </span>
              <ChevronDownIcon
                class="h-4 w-4 shrink-0 text-gray-400 transition-transform"
                :class="isExpanded(group.service) ? '' : '-rotate-90'"
                aria-hidden="true"
              />
            </button>
            <div
              v-show="isExpanded(group.service)"
              class="divide-y divide-gray-100 border-t border-gray-100 dark:divide-gray-800 dark:border-gray-800"
            >
              <div v-for="tool in group.tools" :key="tool.name" class="px-4 py-3">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-mono text-sm text-gray-800 dark:text-white/90">{{
                    tool.name
                  }}</span>
                  <span
                    v-if="!tool.enriched"
                    class="rounded-full bg-gray-100 px-2 py-0.5 text-[11px] text-gray-500 dark:bg-white/5 dark:text-gray-400"
                    >未生成</span
                  >
                </div>
                <template v-if="tool.enriched">
                  <p class="mt-1 text-sm text-gray-600 dark:text-gray-300">{{ tool.summary }}</p>
                  <p v-if="tool.useWhen" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    适用：{{ tool.useWhen }}
                  </p>
                  <p
                    v-if="tool.related?.length"
                    class="mt-1 text-xs text-gray-500 dark:text-gray-400"
                  >
                    配合：<span class="font-mono">{{ tool.related.join('、') }}</span>
                  </p>
                  <div class="mt-2 flex flex-wrap gap-1.5">
                    <span
                      v-for="kw in tool.keywords"
                      :key="kw"
                      class="bg-brand-50 text-brand-700 dark:bg-brand-500/10 dark:text-brand-400 rounded-full px-2 py-0.5 text-[11px]"
                      >{{ kw }}</span
                    >
                  </div>
                </template>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>
  </AdminLayout>
</template>
