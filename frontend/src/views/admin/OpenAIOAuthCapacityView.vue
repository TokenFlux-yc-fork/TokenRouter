<template>
  <AppLayout>
    <div class="space-y-6">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">
            {{ t('admin.openaiOAuthCapacity.title') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.openaiOAuthCapacity.description') }}
          </p>
          <p v-if="summary" class="mt-1 text-xs text-gray-400 dark:text-gray-500">
            {{
              t('admin.openaiOAuthCapacity.generatedAt', {
                time: formatDateTime(summary.generated_at) || '-'
              })
            }}
          </p>
        </div>

        <button
          type="button"
          class="btn btn-secondary inline-flex flex-shrink-0 items-center justify-center gap-2 self-start"
          data-test="refresh-capacity"
          :disabled="loading"
          @click="loadSummary"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          {{ t('admin.openaiOAuthCapacity.refresh') }}
        </button>
      </header>

      <template v-if="loading && !summary">
        <div data-test="capacity-loading" class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <div v-for="index in 4" :key="index" class="card min-h-[190px] animate-pulse p-4">
            <div class="h-4 w-28 rounded bg-gray-200 dark:bg-dark-700"></div>
            <div class="mt-5 h-8 w-40 rounded bg-gray-200 dark:bg-dark-700"></div>
            <div class="mt-3 h-3 w-32 rounded bg-gray-100 dark:bg-dark-800"></div>
            <div class="mt-6 grid grid-cols-2 gap-3">
              <div class="h-12 rounded bg-gray-100 dark:bg-dark-800"></div>
              <div class="h-12 rounded bg-gray-100 dark:bg-dark-800"></div>
            </div>
          </div>
        </div>
        <div class="grid grid-cols-2 gap-4 border-y border-gray-200 py-4 sm:grid-cols-3 xl:grid-cols-5 dark:border-dark-700">
          <div v-for="index in 5" :key="index" class="animate-pulse">
            <div class="h-3 w-20 rounded bg-gray-200 dark:bg-dark-700"></div>
            <div class="mt-2 h-6 w-12 rounded bg-gray-200 dark:bg-dark-700"></div>
          </div>
        </div>
      </template>

      <div
        v-else-if="loadFailed && !summary"
        role="alert"
        data-test="capacity-load-error"
        class="flex flex-col gap-4 border-y border-red-200 bg-red-50/70 px-4 py-6 sm:flex-row sm:items-center sm:justify-between dark:border-red-900/60 dark:bg-red-950/20"
      >
        <div>
          <p class="text-sm font-semibold text-red-700 dark:text-red-300">
            {{ t('admin.openaiOAuthCapacity.loadFailed') }}
          </p>
          <p class="mt-1 text-xs text-red-600/80 dark:text-red-400">
            {{ t('admin.openaiOAuthCapacity.loadFailedHint') }}
          </p>
        </div>
        <button type="button" class="btn btn-secondary self-start" @click="loadSummary">
          <Icon name="refresh" size="sm" class="mr-1.5" />
          {{ t('admin.openaiOAuthCapacity.retry') }}
        </button>
      </div>

      <template v-else-if="summary">
        <div
          v-if="loadFailed"
          role="status"
          data-test="capacity-refresh-error"
          class="flex flex-col gap-3 border-l-4 border-amber-400 bg-amber-50 px-4 py-3 sm:flex-row sm:items-center sm:justify-between dark:border-amber-500 dark:bg-amber-950/20"
        >
          <p class="text-sm font-medium text-amber-800 dark:text-amber-200">
            {{ t('admin.openaiOAuthCapacity.refreshFailed') }}
          </p>
          <button type="button" class="btn btn-secondary btn-sm self-start" @click="loadSummary">
            <Icon name="refresh" size="sm" class="mr-1" />
            {{ t('admin.openaiOAuthCapacity.retry') }}
          </button>
        </div>

        <section :aria-label="t('admin.openaiOAuthCapacity.capacitySummary')">
          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <article
              v-for="card in summaryCards"
              :key="card.key"
              class="card min-w-0 overflow-hidden p-4"
              :data-test="`capacity-card-${card.key}`"
            >
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                    {{ card.label }}
                  </p>
                  <p class="mt-2 break-words text-2xl font-semibold text-gray-900 dark:text-white">
                    {{ formatMoney(card.window.estimated_remaining_usd) }}
                  </p>
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{
                      t('admin.openaiOAuthCapacity.estimatedLimit', {
                        amount: formatMoney(card.window.estimated_limit_usd)
                      })
                    }}
                  </p>
                </div>
                <span
                  class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg"
                  :class="card.iconClass"
                >
                  <Icon :name="card.icon" size="sm" :stroke-width="2" />
                </span>
              </div>

              <div class="mt-4 grid grid-cols-2 gap-3 border-t border-gray-100 pt-3 dark:border-dark-700">
                <div class="min-w-0">
                  <p class="text-[11px] text-gray-400 dark:text-gray-500">
                    {{ t('admin.openaiOAuthCapacity.observedRemaining') }}
                  </p>
                  <p class="mt-1 break-words text-sm font-semibold text-gray-700 dark:text-gray-200">
                    {{ formatMoney(card.window.observed_remaining_usd) }}
                  </p>
                </div>
                <div class="min-w-0">
                  <p class="text-[11px] text-gray-400 dark:text-gray-500">
                    {{ t('admin.openaiOAuthCapacity.unobservedLimit') }}
                  </p>
                  <p class="mt-1 break-words text-sm font-semibold text-gray-700 dark:text-gray-200">
                    {{ formatMoney(card.window.unobserved_limit_usd) }}
                  </p>
                </div>
              </div>

              <p class="mt-3 text-[11px] leading-4 text-gray-400 dark:text-gray-500">
                {{
                  t('admin.openaiOAuthCapacity.windowCoverage', {
                    observed: formatCount(card.window.observed_account_count),
                    missing: formatCount(card.window.missing_snapshot_count),
                    stale: formatCount(card.window.stale_snapshot_count)
                  })
                }}
              </p>
            </article>
          </div>
        </section>

        <section :aria-label="t('admin.openaiOAuthCapacity.accountScope')">
          <dl class="grid grid-cols-2 gap-x-6 gap-y-4 border-y border-gray-200 py-4 sm:grid-cols-3 xl:grid-cols-5 dark:border-dark-700">
            <div v-for="item in accountScopeItems" :key="item.key" class="min-w-0">
              <dt class="truncate text-xs text-gray-500 dark:text-gray-400" :title="item.label">
                {{ item.label }}
              </dt>
              <dd class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">
                {{ formatCount(item.value) }}
              </dd>
            </div>
          </dl>
        </section>

        <div
          v-if="summary.unknown_plan_account_count > 0"
          data-test="unknown-plan-warning"
          class="border-l-4 border-amber-400 bg-amber-50 px-4 py-3 dark:border-amber-500 dark:bg-amber-950/20"
        >
          <p class="text-sm font-medium text-amber-800 dark:text-amber-200">
            {{
              t('admin.openaiOAuthCapacity.unknownPlanWarning', {
                count: formatCount(summary.unknown_plan_account_count)
              })
            }}
          </p>
          <div v-if="unknownPlanTypes.length" class="mt-2 flex flex-wrap gap-2">
            <span
              v-for="item in unknownPlanTypes"
              :key="item.plan_type || 'unknown'"
              class="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-800 dark:bg-amber-900/40 dark:text-amber-200"
            >
              {{ item.plan_type || t('admin.openaiOAuthCapacity.unknownPlan') }}:
              {{ formatCount(item.account_count) }}
            </span>
          </div>
        </div>

        <section class="space-y-4" :aria-label="t('admin.openaiOAuthCapacity.planBreakdown')">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.openaiOAuthCapacity.planBreakdown') }}
            </h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{
                t('admin.openaiOAuthCapacity.planBreakdownDescription', {
                  ratio: formatRatio(summary.five_hour_ratio)
                })
              }}
            </p>
          </div>

          <div
            v-if="visiblePlans.length"
            data-test="capacity-plans"
            class="grid grid-cols-1 gap-4 lg:grid-cols-2 xl:grid-cols-3"
          >
            <article
              v-for="plan in visiblePlans"
              :key="`${plan.plan_type}-${plan.period}`"
              class="card min-w-0 p-4"
              :data-test="`capacity-plan-${normalizePlanType(plan.plan_type)}`"
            >
              <div class="flex min-w-0 items-start justify-between gap-3">
                <div class="min-w-0">
                  <h3 class="truncate text-base font-semibold text-gray-900 dark:text-white" :title="planLabel(plan.plan_type)">
                    {{ planLabel(plan.plan_type) }}
                  </h3>
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ t('admin.openaiOAuthCapacity.accountCount', { count: formatCount(plan.account_count) }) }}
                  </p>
                </div>
                <span class="inline-flex flex-shrink-0 items-center rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                  {{ periodLabel(plan.period) }}
                </span>
              </div>

              <dl class="mt-4 grid grid-cols-2 gap-3 border-y border-gray-100 py-3 dark:border-dark-700">
                <div class="min-w-0">
                  <dt class="text-[11px] text-gray-400 dark:text-gray-500">
                    {{ t('admin.openaiOAuthCapacity.limitPerAccount') }}
                  </dt>
                  <dd class="mt-1 break-words text-sm font-semibold text-gray-700 dark:text-gray-200">
                    {{ formatMoney(plan.limit_per_account_usd) }}
                  </dd>
                </div>
                <div class="min-w-0">
                  <dt class="text-[11px] text-gray-400 dark:text-gray-500">
                    {{ t('admin.openaiOAuthCapacity.fiveHourLimitPerAccount') }}
                  </dt>
                  <dd class="mt-1 break-words text-sm font-semibold text-gray-700 dark:text-gray-200">
                    {{ formatMoney(plan.five_hour_limit_per_account_usd) }}
                  </dd>
                </div>
              </dl>

              <div class="mt-4 space-y-4">
                <div v-for="windowItem in planWindowItems(plan)" :key="windowItem.key">
                  <div class="flex min-w-0 items-baseline justify-between gap-3">
                    <span class="truncate text-xs font-medium text-gray-600 dark:text-gray-300">
                      {{ windowItem.label }}
                    </span>
                    <span class="break-words text-right text-sm font-semibold text-gray-900 dark:text-white">
                      {{ formatMoney(windowItem.window.estimated_remaining_usd) }}
                    </span>
                  </div>
                  <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                    <div
                      class="h-full rounded-full transition-[width] duration-300"
                      :class="windowItem.barClass"
                      :style="{ width: `${remainingPercent(windowItem.window)}%` }"
                    ></div>
                  </div>
                  <div class="mt-1.5 flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-[11px] text-gray-400 dark:text-gray-500">
                    <span>
                      {{
                        t('admin.openaiOAuthCapacity.ofLimit', {
                          amount: formatMoney(windowItem.window.estimated_limit_usd)
                        })
                      }}
                    </span>
                    <span>
                      {{
                        t('admin.openaiOAuthCapacity.missingAndStale', {
                          missing: formatCount(windowItem.window.missing_snapshot_count),
                          stale: formatCount(windowItem.window.stale_snapshot_count)
                        })
                      }}
                    </span>
                  </div>
                </div>
              </div>
            </article>
          </div>

          <div
            v-else
            data-test="capacity-plans-empty"
            class="border-y border-gray-200 py-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
          >
            {{ t('admin.openaiOAuthCapacity.noPlans') }}
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type {
  OpenAIOAuthPoolCapacityPlanSummary,
  OpenAIOAuthPoolCapacitySummary,
  OpenAIOAuthPoolCapacityWindowSummary
} from '@/api/admin/accounts'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatDateTime, formatNumber } from '@/utils/format'

type SummaryCard = {
  key: 'parent' | 'five-hour' | 'weekly' | 'monthly'
  label: string
  icon: 'dollar' | 'clock' | 'chartBar' | 'calendar'
  iconClass: string
  window: OpenAIOAuthPoolCapacityWindowSummary
}

type PlanWindowItem = {
  key: 'parent' | 'five-hour'
  label: string
  barClass: string
  window: OpenAIOAuthPoolCapacityWindowSummary
}

const { t } = useI18n()
const { formatUsdAmount } = useBalanceDisplay()

const summary = ref<OpenAIOAuthPoolCapacitySummary | null>(null)
const loading = ref(false)
const loadFailed = ref(false)

const formatMoney = (value: number) => formatUsdAmount(value, { fractionDigits: 2 })
const formatCount = (value: number) => formatNumber(value)
const formatRatio = (value: number) => `${(value * 100).toLocaleString(undefined, { maximumFractionDigits: 2 })}%`

const summaryCards = computed<SummaryCard[]>(() => {
  if (!summary.value) return []
  return [
    {
      key: 'parent',
      label: t('admin.openaiOAuthCapacity.parentRemaining'),
      icon: 'dollar',
      iconClass: 'bg-sky-100 text-sky-600 dark:bg-sky-900/30 dark:text-sky-400',
      window: summary.value.totals.parent
    },
    {
      key: 'five-hour',
      label: t('admin.openaiOAuthCapacity.fiveHourRemaining'),
      icon: 'clock',
      iconClass: 'bg-emerald-100 text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400',
      window: summary.value.totals.five_hour
    },
    {
      key: 'weekly',
      label: t('admin.openaiOAuthCapacity.weeklyRemaining'),
      icon: 'chartBar',
      iconClass: 'bg-violet-100 text-violet-600 dark:bg-violet-900/30 dark:text-violet-400',
      window: summary.value.totals.weekly
    },
    {
      key: 'monthly',
      label: t('admin.openaiOAuthCapacity.monthlyRemaining'),
      icon: 'calendar',
      iconClass: 'bg-amber-100 text-amber-600 dark:bg-amber-900/30 dark:text-amber-400',
      window: summary.value.totals.monthly
    }
  ]
})

const accountScopeItems = computed(() => {
  if (!summary.value) return []
  return [
    {
      key: 'managed',
      label: t('admin.openaiOAuthCapacity.managedAccounts'),
      value: summary.value.managed_account_count
    },
    {
      key: 'included',
      label: t('admin.openaiOAuthCapacity.includedAccounts'),
      value: summary.value.included_account_count
    },
    {
      key: 'excluded',
      label: t('admin.openaiOAuthCapacity.excludedAccounts'),
      value: summary.value.excluded_account_count
    },
    {
      key: 'shadow',
      label: t('admin.openaiOAuthCapacity.shadowAccounts'),
      value: summary.value.shadow_account_count
    },
    {
      key: 'unknown-plan',
      label: t('admin.openaiOAuthCapacity.unknownPlanAccounts'),
      value: summary.value.unknown_plan_account_count
    }
  ]
})

const unknownPlanTypes = computed(() => summary.value?.unknown_plan_types ?? [])
const visiblePlans = computed(() => summary.value?.plans.filter((plan) => plan.account_count > 0) ?? [])

const normalizePlanType = (planType: string) =>
  planType.trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-') || 'unknown'

const planLabel = (planType: string) => {
  const normalized = normalizePlanType(planType)
  const knownPlanKeys: Record<string, string> = {
    k12: 'k12',
    pro: 'pro',
    team: 'team',
    plus: 'plus',
    free: 'free'
  }
  const key = knownPlanKeys[normalized]
  return key ? t(`admin.openaiOAuthCapacity.plans.${key}`) : planType || t('admin.openaiOAuthCapacity.unknownPlan')
}

const periodLabel = (period: OpenAIOAuthPoolCapacityPlanSummary['period']) =>
  t(`admin.openaiOAuthCapacity.periods.${period}`)

const planWindowItems = (plan: OpenAIOAuthPoolCapacityPlanSummary): PlanWindowItem[] => [
  {
    key: 'parent',
    label: periodLabel(plan.period),
    barClass: 'bg-sky-500',
    window: plan.parent
  },
  {
    key: 'five-hour',
    label: t('admin.openaiOAuthCapacity.fiveHourWindow'),
    barClass: 'bg-emerald-500',
    window: plan.five_hour
  }
]

const remainingPercent = (window: OpenAIOAuthPoolCapacityWindowSummary) => {
  if (window.estimated_limit_usd <= 0) return 0
  return Math.min(100, Math.max(0, (window.estimated_remaining_usd / window.estimated_limit_usd) * 100))
}

const loadSummary = async () => {
  if (loading.value) return
  loading.value = true
  loadFailed.value = false
  try {
    summary.value = await adminAPI.accounts.getOpenAIOAuthPoolCapacity()
  } catch (error) {
    loadFailed.value = true
    console.error('Failed to load OpenAI OAuth pool capacity:', error)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void loadSummary()
})
</script>
