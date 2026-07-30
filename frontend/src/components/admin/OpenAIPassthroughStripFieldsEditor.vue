<template>
  <div class="space-y-3">
    <div>
      <label class="input-label mb-0">{{ t(`${i18nPrefix}.title`) }}</label>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t(`${i18nPrefix}.description`) }}
      </p>
    </div>

    <label
      v-if="allowInherit"
      :for="`${idPrefix}-inherit`"
      class="flex cursor-pointer items-start gap-2"
    >
      <input
        :id="`${idPrefix}-inherit`"
        type="checkbox"
        :checked="inherit"
        class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500"
        @change="updateInherit"
      />
      <span>
        <span class="block text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t(`${i18nPrefix}.inherit`) }}
        </span>
        <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">
          {{ t(`${i18nPrefix}.inheritHint`) }}
        </span>
      </span>
    </label>

    <template v-if="!allowInherit || !inherit">
      <div v-if="modelValue.length" class="flex flex-wrap gap-2">
        <span
          v-for="field in modelValue"
          :key="field"
          class="inline-flex min-h-8 max-w-full items-center gap-1 rounded border border-gray-200 bg-gray-50 pl-2.5 text-xs text-gray-700 dark:border-dark-500 dark:bg-dark-700 dark:text-gray-200"
        >
          <code class="break-all">{{ field }}</code>
          <button
            type="button"
            class="inline-flex h-8 w-8 flex-none items-center justify-center text-gray-400 transition-colors hover:text-red-500 focus:outline-none focus:ring-2 focus:ring-primary-500"
            :aria-label="t(`${i18nPrefix}.remove`, { field })"
            :title="t(`${i18nPrefix}.remove`, { field })"
            @click="removeField(field)"
          >
            <Icon name="x" size="xs" :stroke-width="2" />
          </button>
        </span>
      </div>
      <p v-else class="text-xs text-gray-500 dark:text-gray-400">
        {{ t(`${i18nPrefix}.empty`) }}
      </p>

      <div class="flex items-stretch gap-2">
        <label :for="`${idPrefix}-field`" class="sr-only">
          {{ t(`${i18nPrefix}.fieldLabel`) }}
        </label>
        <input
          :id="`${idPrefix}-field`"
          v-model="draft"
          type="text"
          class="input min-w-0 flex-1 font-mono"
          :placeholder="t(`${i18nPrefix}.placeholder`)"
          @keydown.enter.prevent="addField"
        />
        <button
          type="button"
          class="btn btn-secondary inline-flex h-10 w-10 flex-none items-center justify-center p-0"
          :disabled="!draft.trim() || modelValue.length >= maxFields"
          :aria-label="t(`${i18nPrefix}.add`)"
          :title="t(`${i18nPrefix}.add`)"
          @click="addField"
        >
          <Icon name="plus" size="sm" :stroke-width="2" />
        </button>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ t(`${i18nPrefix}.syntaxHint`) }}
      </p>
      <p v-if="error" class="text-xs text-red-600 dark:text-red-400" aria-live="polite">
        {{ error }}
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const props = withDefaults(defineProps<{
  idPrefix: string
  modelValue: string[]
  context?: 'group' | 'account'
  allowInherit?: boolean
  inherit?: boolean
}>(), {
  context: 'group',
  allowInherit: false,
  inherit: false,
})

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
  'update:inherit': [value: boolean]
}>()

const { t } = useI18n()
const draft = ref('')
const error = ref('')
const maxFields = 32
const pathPattern = /^[A-Za-z_][A-Za-z0-9_-]*(?:\[(?:\d+)?\])?(?:\.[A-Za-z_][A-Za-z0-9_-]*(?:\[(?:\d+)?\])?)*$/

const i18nPrefix = computed(() => props.context === 'account'
  ? 'admin.accounts.openai.passthroughStripFields'
  : 'admin.groups.openaiPassthroughStripFields')

const isValidPath = (path: string): boolean => {
  if (!pathPattern.test(path) || path.length > 160 || path.endsWith(']')) return false
  return !['model', 'input', 'stream'].includes(path)
}

const addField = () => {
  const field = draft.value.trim()
  if (!field) return
  if (props.modelValue.length >= maxFields) {
    error.value = t(`${i18nPrefix.value}.limitReached`, { count: maxFields })
    return
  }
  if (!isValidPath(field)) {
    error.value = t(`${i18nPrefix.value}.invalidPath`)
    return
  }
  if (props.modelValue.includes(field)) {
    error.value = t(`${i18nPrefix.value}.duplicatePath`)
    return
  }
  emit('update:modelValue', [...props.modelValue, field])
  draft.value = ''
  error.value = ''
}

const removeField = (field: string) => {
  emit('update:modelValue', props.modelValue.filter((candidate) => candidate !== field))
  error.value = ''
}

const updateInherit = (event: Event) => {
  emit('update:inherit', (event.target as HTMLInputElement).checked)
  error.value = ''
}
</script>
