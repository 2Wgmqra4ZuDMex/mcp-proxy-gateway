<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { getAPIKeyToolAccess, updateAPIKeyToolAccess, type UpstreamAccessMode } from '@/api/apikeys'
import { getAggregatedTools } from '@/api/tools'
import { useConfirm } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import {
  filterToolGroups,
  groupToolsForAccess,
  type ToolAccessGroup,
} from '@/utils/toolAccessGroups'

const props = defineProps<{ apiKeyId: string }>()

const toast = useToast()
const { confirm } = useConfirm()

const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const mode = ref<UpstreamAccessMode>('all')
const selected = ref<Set<string>>(new Set())
const groups = ref<ToolAccessGroup[]>([])
const search = ref('')
const collapsed = ref<Set<string>>(new Set())
let loadSeq = 0

const visibleGroups = computed(() => filterToolGroups(groups.value, search.value))
const totalTools = computed(() => groups.value.reduce((sum, group) => sum + group.tools.length, 0))
const knownNames = computed(
  () => new Set(groups.value.flatMap((group) => group.tools.map((tool) => tool.name))),
)
// 白名单中已不在当前目录里的工具名（上游下线或改名），保存时会一并保留，单独提示。
const missingNames = computed(() =>
  Array.from(selected.value)
    .filter((name) => !knownNames.value.has(name))
    .sort(),
)
const selectedKnownCount = computed(() => selected.value.size - missingNames.value.length)

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message !== '' ? err.message : fallback
}

async function load(id: string): Promise<void> {
  const seq = ++loadSeq
  loading.value = true
  loadError.value = ''
  try {
    const [access, tools] = await Promise.all([getAPIKeyToolAccess(id), getAggregatedTools()])
    if (seq !== loadSeq) return
    mode.value = access.mode
    selected.value = new Set(access.toolNames)
    groups.value = groupToolsForAccess(tools.toolDetails)
  } catch (err) {
    if (seq === loadSeq) loadError.value = errorText(err, '加载工具权限失败')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch(
  () => props.apiKeyId,
  (id) => {
    search.value = ''
    collapsed.value = new Set()
    if (id !== '') void load(id)
  },
  { immediate: true },
)

function isSelected(name: string): boolean {
  return selected.value.has(name)
}

function toggleTool(name: string): void {
  const next = new Set(selected.value)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  selected.value = next
}

function groupState(group: ToolAccessGroup): 'all' | 'some' | 'none' {
  const count = group.tools.filter((tool) => selected.value.has(tool.name)).length
  if (count === 0) return 'none'
  return count === group.tools.length ? 'all' : 'some'
}

function toggleGroup(group: ToolAccessGroup): void {
  const next = new Set(selected.value)
  const selectAll = groupState(group) !== 'all'
  for (const tool of group.tools) {
    if (selectAll) next.add(tool.name)
    else next.delete(tool.name)
  }
  selected.value = next
}

function toggleCollapsed(key: string): void {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsed.value = next
}

function selectVisible(): void {
  const next = new Set(selected.value)
  for (const group of visibleGroups.value) for (const tool of group.tools) next.add(tool.name)
  selected.value = next
}

function clearSelection(): void {
  selected.value = new Set()
}

function removeMissing(): void {
  const next = new Set(selected.value)
  for (const name of missingNames.value) next.delete(name)
  selected.value = next
}

async function save(): Promise<void> {
  const count = selected.value.size
  const denyAll = mode.value === 'selected' && count === 0
  const accepted = await confirm({
    title: denyAll ? '确认不暴露任何工具' : '确认更新工具权限',
    message:
      mode.value === 'all'
        ? '保存后该 API Key 可看到全部工具，仍受上游权限和屏蔽规则限制。'
        : denyAll
          ? '当前没有勾选任何工具，保存后该 API Key 将看不到也无法调用任何工具。'
          : `保存后该 API Key 只能看到并调用已勾选的 ${count} 个工具；之后新增的工具不会自动放行。`,
    confirmText: '确认保存',
    tone: denyAll || mode.value === 'all' ? 'warning' : 'info',
  })
  if (!accepted) return
  saving.value = true
  try {
    const saved = await updateAPIKeyToolAccess(props.apiKeyId, {
      mode: mode.value,
      toolNames: mode.value === 'selected' ? Array.from(selected.value) : [],
    })
    mode.value = saved.mode
    selected.value = new Set(saved.toolNames)
    toast.success('工具权限已更新')
  } catch (err) {
    toast.error(errorText(err, '更新工具权限失败'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <div
      v-if="loading"
      class="rounded-xl border border-gray-200 px-4 py-8 text-center text-sm text-gray-400 dark:border-gray-800"
    >
      加载中…
    </div>
    <p
      v-else-if="loadError !== ''"
      class="bg-error-50 text-error-600 dark:bg-error-500/10 dark:text-error-400 rounded-lg px-4 py-2.5 text-sm"
    >
      {{ loadError }}
    </p>
    <template v-else>
      <div
        class="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-white/[0.03]"
      >
        <h4 class="text-sm font-semibold text-gray-800 dark:text-white/90">可用工具</h4>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
          勾选这个 API Key 能看到和调用的工具。同名工具的多个账号会一起放行，并继续轮流使用。
        </p>
        <div class="mt-4 grid gap-2 sm:grid-cols-2">
          <button
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="
              mode === 'all'
                ? 'border-brand-400 bg-brand-50 dark:border-brand-500/50 dark:bg-brand-500/10'
                : 'border-gray-200 hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-800'
            "
            @click="mode = 'all'"
          >
            <span class="block text-sm font-medium text-gray-800 dark:text-white/90">全部工具</span>
            <span class="mt-1 block text-xs text-gray-500">新增工具会自动出现</span>
          </button>
          <button
            type="button"
            class="rounded-xl border p-3 text-left transition"
            :class="
              mode === 'selected'
                ? 'border-brand-400 bg-brand-50 dark:border-brand-500/50 dark:bg-brand-500/10'
                : 'border-gray-200 hover:bg-gray-50 dark:border-gray-700 dark:hover:bg-gray-800'
            "
            @click="mode = 'selected'"
          >
            <span class="block text-sm font-medium text-gray-800 dark:text-white/90"
              >只用勾选的工具</span
            >
            <span class="mt-1 block text-xs text-gray-500">新增工具默认不出现，需要手动勾选</span>
          </button>
        </div>
      </div>

      <div v-if="mode === 'selected'" class="space-y-3">
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
          <input
            v-model="search"
            type="search"
            placeholder="搜索工具名、描述或服务"
            class="min-w-0 flex-1 rounded-lg border border-gray-300 px-3 py-2 text-sm dark:border-gray-700 dark:bg-gray-800"
          />
          <div class="flex items-center gap-2 text-xs">
            <button type="button" class="text-brand-600 dark:text-brand-400" @click="selectVisible">
              {{ search.trim() === '' ? '全选' : '选中搜索结果' }}
            </button>
            <span class="text-gray-300 dark:text-gray-700">|</span>
            <button type="button" class="text-gray-500" @click="clearSelection">清空</button>
          </div>
        </div>

        <div
          v-if="missingNames.length > 0"
          class="bg-warning-50 text-warning-700 dark:bg-warning-500/10 dark:text-warning-400 flex flex-col gap-2 rounded-lg px-4 py-2.5 text-xs sm:flex-row sm:items-center sm:justify-between"
        >
          <span class="break-all">
            {{ missingNames.length }} 个已勾选的工具当前不在目录中：{{ missingNames.join('、') }}
          </span>
          <button type="button" class="shrink-0 font-medium underline" @click="removeMissing">
            移除
          </button>
        </div>

        <div
          v-if="visibleGroups.length === 0"
          class="rounded-xl border border-dashed border-gray-300 px-4 py-8 text-center text-sm text-gray-400 dark:border-gray-700"
        >
          {{ totalTools === 0 ? '工具目录为空，请先在上游管理中刷新工具' : '没有匹配的工具' }}
        </div>
        <div v-else class="space-y-2">
          <div
            v-for="group in visibleGroups"
            :key="group.key"
            class="overflow-hidden rounded-xl border border-gray-200 dark:border-gray-800"
          >
            <div class="flex items-center gap-3 bg-gray-50 px-3 py-2.5 dark:bg-white/[0.02]">
              <button
                type="button"
                class="flex h-5 w-5 shrink-0 items-center justify-center rounded border text-xs"
                :class="
                  groupState(group) === 'none'
                    ? 'border-gray-300 dark:border-gray-600'
                    : 'border-brand-500 bg-brand-500 text-white'
                "
                :aria-label="`切换 ${group.label} 全部工具`"
                @click="toggleGroup(group)"
              >
                {{ groupState(group) === 'all' ? '✓' : groupState(group) === 'some' ? '–' : '' }}
              </button>
              <button
                type="button"
                class="flex min-w-0 flex-1 items-center justify-between gap-2 text-left"
                :aria-expanded="!collapsed.has(group.key)"
                @click="toggleCollapsed(group.key)"
              >
                <span class="truncate text-sm font-medium text-gray-800 dark:text-white/90">{{
                  group.label
                }}</span>
                <span class="shrink-0 text-xs text-gray-400">
                  {{ group.tools.filter((tool) => isSelected(tool.name)).length }} /
                  {{ group.tools.length }}
                  {{ collapsed.has(group.key) ? '▸' : '▾' }}
                </span>
              </button>
            </div>
            <div
              v-if="!collapsed.has(group.key)"
              class="divide-y divide-gray-100 dark:divide-gray-800"
            >
              <button
                v-for="tool in group.tools"
                :key="tool.name"
                type="button"
                class="flex w-full items-start gap-3 px-3 py-2.5 text-left transition"
                :class="
                  isSelected(tool.name)
                    ? 'bg-brand-50/60 dark:bg-brand-500/10'
                    : 'hover:bg-gray-50 dark:hover:bg-gray-800'
                "
                @click="toggleTool(tool.name)"
              >
                <span
                  class="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded border text-xs"
                  :class="
                    isSelected(tool.name)
                      ? 'border-brand-500 bg-brand-500 text-white'
                      : 'border-gray-300 dark:border-gray-600'
                  "
                  aria-hidden="true"
                  >{{ isSelected(tool.name) ? '✓' : '' }}</span
                >
                <span class="min-w-0 flex-1">
                  <span class="block truncate font-mono text-sm text-gray-800 dark:text-white/90">{{
                    tool.name
                  }}</span>
                  <span v-if="tool.description" class="mt-0.5 line-clamp-2 text-xs text-gray-400">{{
                    tool.description
                  }}</span>
                </span>
                <span v-if="tool.sourceCount > 1" class="shrink-0 text-xs text-gray-400">
                  {{ tool.sourceCount }} 个账号
                </span>
              </button>
            </div>
          </div>
        </div>
      </div>

      <div
        class="flex flex-col gap-3 border-t border-gray-200 pt-4 sm:flex-row sm:items-center sm:justify-between dark:border-gray-800"
      >
        <p class="text-xs text-gray-500">
          {{
            mode === 'all'
              ? `将开放全部 ${totalTools} 个工具`
              : `已勾选 ${selectedKnownCount} / ${totalTools} 个工具`
          }}
        </p>
        <button
          type="button"
          class="bg-brand-500 hover:bg-brand-600 rounded-lg px-4 py-2.5 text-sm font-medium text-white transition disabled:bg-gray-300 disabled:text-gray-500 dark:disabled:bg-gray-700 dark:disabled:text-gray-400"
          :disabled="saving"
          @click="save"
        >
          {{ saving ? '保存中…' : '保存工具权限' }}
        </button>
      </div>
    </template>
  </div>
</template>
