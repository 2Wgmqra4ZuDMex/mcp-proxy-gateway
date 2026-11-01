<template>
  <aside
    :class="[
      'fixed top-0 left-0 z-99999 mt-16 flex h-[calc(100dvh-4rem)] flex-col border-r border-gray-200 bg-white px-5 text-gray-900 transition-all duration-300 ease-in-out lg:mt-0 lg:h-screen dark:border-gray-800 dark:bg-gray-900',
      {
        'lg:w-[290px]': isExpanded || isMobileOpen || isHovered,
        'lg:w-[90px]': !isExpanded && !isHovered,
        'w-[min(290px,calc(100vw_-_24px))] translate-x-0': isMobileOpen,
        '-translate-x-full': !isMobileOpen,
        'lg:translate-x-0': true,
      },
    ]"
    @mouseenter="!isExpanded && (isHovered = true)"
    @mouseleave="isHovered = false"
  >
    <div :class="['flex py-8', !isExpanded && !isHovered ? 'lg:justify-center' : 'justify-start']">
      <router-link to="/" @click="closeMobileSidebar">
        <img
          v-if="isExpanded || isHovered || isMobileOpen"
          class="dark:hidden"
          src="/images/logo/logo.svg"
          alt="Logo"
          width="150"
          height="40"
        />
        <img
          v-if="isExpanded || isHovered || isMobileOpen"
          class="hidden dark:block"
          src="/images/logo/logo-dark.svg"
          alt="Logo"
          width="150"
          height="40"
        />
        <img v-else src="/images/logo/logo-icon.svg" alt="Logo" width="32" height="32" />
      </router-link>
    </div>
    <div class="no-scrollbar flex flex-col overflow-y-auto duration-300 ease-linear">
      <nav class="mb-6">
        <div class="flex flex-col gap-4">
          <div v-for="(menuGroup, groupIndex) in menuGroups" :key="groupIndex">
            <h2
              :class="[
                'mb-4 flex text-xs leading-[20px] text-gray-400 uppercase',
                !isExpanded && !isHovered ? 'lg:justify-center' : 'justify-start',
              ]"
            >
              <button
                v-if="menuGroup.collapsible && (isExpanded || isHovered || isMobileOpen)"
                type="button"
                class="flex w-full items-center justify-between uppercase transition hover:text-gray-600 dark:hover:text-gray-300"
                :aria-expanded="isGroupOpen(menuGroup)"
                @click="toggleGroup(menuGroup)"
              >
                {{ menuGroup.title }}
                <ChevronDownIcon
                  class="h-4 w-4 transition-transform duration-200"
                  :class="isGroupOpen(menuGroup) ? 'rotate-0' : '-rotate-90'"
                  aria-hidden="true"
                />
              </button>
              <template v-else-if="isExpanded || isHovered || isMobileOpen">
                {{ menuGroup.title }}
              </template>
              <HorizontalDots v-else />
            </h2>
            <ul v-show="isGroupOpen(menuGroup)" class="flex flex-col gap-4">
              <li v-for="item in menuGroup.items" :key="item.name">
                <router-link
                  v-if="item.path"
                  :to="item.path"
                  :class="[
                    'menu-item group',
                    {
                      'menu-item-active': isActive(item.path),
                      'menu-item-inactive': !isActive(item.path),
                    },
                  ]"
                  @click="closeMobileSidebar"
                >
                  <span
                    :class="[
                      'shrink-0',
                      isActive(item.path) ? 'menu-item-icon-active' : 'menu-item-icon-inactive',
                    ]"
                  >
                    <component :is="item.icon" />
                  </span>
                  <span
                    v-if="isExpanded || isHovered || isMobileOpen"
                    class="menu-item-text min-w-0 flex-1 truncate whitespace-nowrap"
                    >{{ item.name }}</span
                  >
                </router-link>
              </li>
            </ul>
          </div>
        </div>
      </nav>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, ref, type Component } from 'vue'
import { useRoute } from 'vue-router'
import {
  GridIcon,
  HorizontalDots,
  PlugInIcon,
  ListIcon,
  TableIcon,
  SettingsIcon,
  PieChartIcon,
  DocsIcon,
  ErrorHexaIcon,
  FlagIcon,
  Message2Line,
  InfoCircleIcon,
  UserGroupIcon,
  SendIcon,
  BoxCubeIcon,
  DraftIcon,
  ChevronDownIcon,
  PaperclipIcon,
  TaskIcon,
} from '@/icons'
import { useSidebar } from '@/composables/useSidebar'
import { useRuntimeCapability } from '@/composables/useRuntimeCapability'

const route = useRoute()

const { isExpanded, isMobileOpen, isHovered, closeMobileSidebar } = useSidebar()
const { localRuntimeSupported } = useRuntimeCapability()

interface MenuItem {
  icon: Component
  name: string
  path: string
  requiresLocalRuntime?: boolean
}

interface MenuGroup {
  title: string
  items: MenuItem[]
  /** 可折叠分组：默认收起，当前页面位于组内时自动展开。 */
  collapsible?: boolean
}

const allMenuGroups: MenuGroup[] = [
  {
    title: '总览',
    items: [
      { icon: GridIcon, name: '仪表盘', path: '/' },
      { icon: PieChartIcon, name: '调用统计', path: '/statistics' },
    ],
  },
  {
    title: 'MCP',
    items: [
      { icon: PlugInIcon, name: '上游 MCP', path: '/upstreams' },
      { icon: TaskIcon, name: 'MCP 检测', path: '/mcp-check' },
      { icon: TableIcon, name: '工具目录', path: '/tools' },
      { icon: ListIcon, name: '规则管理', path: '/rules' },
      { icon: SendIcon, name: '出站代理', path: '/outbound-proxies' },
    ],
  },
  {
    title: '接入',
    items: [
      { icon: UserGroupIcon, name: 'API Key', path: '/apikeys' },
      { icon: PaperclipIcon, name: '接入地址', path: '/api-service' },
      { icon: DocsIcon, name: '调用记录', path: '/call-records' },
    ],
  },
  {
    title: '系统',
    items: [
      { icon: SettingsIcon, name: '系统设置', path: '/settings' },
      { icon: BoxCubeIcon, name: '运行环境', path: '/runtime' },
      // 受管脚本要靠 Node / Python 执行，精简镜像里没有解释器可用。
      { icon: DraftIcon, name: '脚本中心', path: '/scripts', requiresLocalRuntime: true },
    ],
  },
  {
    title: '运维',
    collapsible: true,
    items: [
      { icon: ErrorHexaIcon, name: '安全中心', path: '/security' },
      { icon: FlagIcon, name: '审计日志', path: '/audit' },
      { icon: Message2Line, name: '系统日志', path: '/system-logs' },
      { icon: InfoCircleIcon, name: '关于', path: '/about' },
    ],
  },
]

// 精简镜像没有本地运行时，隐藏依赖解释器的入口；空分组一并去掉避免留下孤立标题。
const menuGroups = computed(() =>
  allMenuGroups
    .map((group) => ({
      ...group,
      items: group.items.filter(
        (item) => !item.requiresLocalRuntime || localRuntimeSupported.value,
      ),
    }))
    .filter((group) => group.items.length > 0),
)

const isActive = (path: string) => {
  if (path === '/') return route.path === '/'
  return route.path === path || route.path.startsWith(`${path}/`)
}

const OPEN_GROUPS_KEY = 'mpg.sidebar.openGroups'
const openGroups = ref<Set<string>>(new Set(readOpenGroups()))

function readOpenGroups(): string[] {
  try {
    const raw = localStorage.getItem(OPEN_GROUPS_KEY)
    const parsed: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

function isGroupOpen(group: MenuGroup): boolean {
  if (!group.collapsible) return true
  // 侧边栏收成图标模式时没有分组标题可点，始终展示图标。
  if (!isExpanded.value && !isHovered.value && !isMobileOpen.value) return true
  return openGroups.value.has(group.title) || group.items.some((item) => isActive(item.path))
}

function toggleGroup(group: MenuGroup): void {
  const next = new Set(openGroups.value)
  if (isGroupOpen(group)) next.delete(group.title)
  else next.add(group.title)
  openGroups.value = next
  try {
    localStorage.setItem(OPEN_GROUPS_KEY, JSON.stringify(Array.from(next)))
  } catch {
    // 本地存储不可用时仅本次会话生效。
  }
}
</script>
