<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      @click="open = !open"
      class="flex items-center gap-1.5 rounded-lg p-1.5 text-gray-600 transition-colors hover:bg-primary-100 hover:text-primary-900 dark:text-dark-100/80 dark:hover:bg-dark-800 dark:hover:text-white"
      :aria-label="t('theme.label')"
      :aria-expanded="open"
    >
      <span class="h-4 w-4 rounded-full ring-1 ring-current" :style="{ background: swatch }" />
    </button>

    <transition name="dropdown">
      <ul v-if="open" class="dropdown right-0 mt-2 w-48 py-1" role="menu">
        <li v-for="opt in options" :key="opt.value">
          <button
            type="button"
            role="menuitemradio"
            :aria-checked="isActive(opt.value)"
            data-test="theme-option"
            @click="choose(opt.value)"
            class="dropdown-item w-full justify-between"
          >
            <span class="flex items-center gap-2">
              <span class="h-3.5 w-3.5 rounded-full ring-1 ring-black/10 dark:ring-white/10" :style="{ background: opt.swatch }" />
              {{ t(opt.label) }}
            </span>
            <svg v-if="isActive(opt.value)" class="h-4 w-4 text-primary-500" viewBox="0 0 20 20" fill="currentColor">
              <path fill-rule="evenodd" d="M16.7 5.3a1 1 0 0 1 0 1.4l-7.5 7.5a1 1 0 0 1-1.4 0L3.3 9.7a1 1 0 1 1 1.4-1.4l3.1 3.1 6.8-6.8a1 1 0 0 1 1.4 0z" clip-rule="evenodd" />
            </svg>
          </button>
        </li>
      </ul>
    </transition>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTheme } from '@/composables/useTheme'
import type { ThemeMode } from '@/composables/useTheme'

const { t } = useI18n()
const { theme, setTheme } = useTheme()

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const options: ReadonlyArray<{ value: ThemeMode; label: string; swatch: string }> = [
  { value: 'system', label: 'theme.system', swatch: 'linear-gradient(135deg,#fff 50%,#0E1626 50%)' },
  { value: 'light', label: 'theme.light', swatch: '#ffffff' },
  { value: 'midnight', label: 'theme.midnight', swatch: '#0E1626' },
  { value: 'carbon', label: 'theme.carbon', swatch: '#0E0F12' },
  { value: 'oled', label: 'theme.oled', swatch: '#000000' }
]

const swatch = computed(() => options.find((o) => o.value === theme.value)?.swatch ?? '#000')

function isActive(value: ThemeMode): boolean {
  if (theme.value === 'system') return value === 'system'
  return value === theme.value
}

function choose(value: ThemeMode) {
  setTheme(value)
  open.value = false
}

function onClickOutside(e: MouseEvent) {
  if (rootRef.value && !rootRef.value.contains(e.target as Node)) open.value = false
}
onMounted(() => document.addEventListener('click', onClickOutside))
onBeforeUnmount(() => document.removeEventListener('click', onClickOutside))
</script>

<style scoped>
.dropdown-enter-active, .dropdown-leave-active { transition: all 0.2s ease; }
.dropdown-enter-from, .dropdown-leave-to { opacity: 0; transform: scale(0.95) translateY(-4px); }
</style>
