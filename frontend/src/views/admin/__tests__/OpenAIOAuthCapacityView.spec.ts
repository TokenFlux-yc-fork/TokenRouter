import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIOAuthCapacityView from '../OpenAIOAuthCapacityView.vue'
import type {
  OpenAIOAuthPoolCapacityGroupSummary,
  OpenAIOAuthPoolCapacityPlanSummary,
  OpenAIOAuthPoolCapacitySummary,
  OpenAIOAuthPoolCapacityWindowSummary
} from '@/api/admin/accounts'
import Select from '@/components/common/Select.vue'

const { getOpenAIOAuthPoolCapacity } = vi.hoisted(() => ({
  getOpenAIOAuthPoolCapacity: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getOpenAIOAuthPoolCapacity
    }
  }
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatUsdAmount: (value: number) => `$${value.toFixed(2)}`
  })
}))

vi.mock('@/utils/format', () => ({
  formatDateTime: (value: string) => value,
  formatNumber: (value: number) => String(value)
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) =>
        [key, ...Object.values(params ?? {})].join(' ')
    })
  }
})

const makeWindow = (
  overrides: Partial<OpenAIOAuthPoolCapacityWindowSummary> = {}
): OpenAIOAuthPoolCapacityWindowSummary => ({
  estimated_limit_usd: 100,
  estimated_used_usd: 25,
  estimated_remaining_usd: 75,
  observed_remaining_usd: 60,
  unobserved_limit_usd: 15,
  observed_account_count: 1,
  missing_snapshot_count: 0,
  stale_snapshot_count: 0,
  ...overrides
})

const makeProPlan = (): OpenAIOAuthPoolCapacityPlanSummary => ({
  plan_type: 'pro',
  period: 'weekly',
  account_count: 1,
  limit_per_account_usd: 2400,
  five_hour_limit_per_account_usd: 360,
  parent: makeWindow({ estimated_limit_usd: 2400, estimated_remaining_usd: 1800 }),
  five_hour: makeWindow({ estimated_limit_usd: 360, estimated_remaining_usd: 270 })
})

const makeFreePlan = (): OpenAIOAuthPoolCapacityPlanSummary => ({
  plan_type: 'free',
  period: 'monthly',
  account_count: 1,
  limit_per_account_usd: 5,
  five_hour_limit_per_account_usd: 0.75,
  parent: makeWindow({ estimated_limit_usd: 5, estimated_remaining_usd: 4 }),
  five_hour: makeWindow({ estimated_limit_usd: 0.75, estimated_remaining_usd: 0.6 })
})

const makeGroup = (
  overrides: Partial<OpenAIOAuthPoolCapacityGroupSummary> = {}
): OpenAIOAuthPoolCapacityGroupSummary => ({
  group_id: 10,
  group_name: 'Primary',
  group_status: 'active',
  sort_order: 0,
  managed_account_count: 1,
  included_account_count: 1,
  excluded_account_count: 0,
  shadow_account_count: 0,
  unknown_plan_account_count: 0,
  unknown_plan_types: [],
  totals: {
    parent: makeWindow({ estimated_limit_usd: 2400, estimated_remaining_usd: 1800 }),
    five_hour: makeWindow({ estimated_limit_usd: 360, estimated_remaining_usd: 270 }),
    weekly: makeWindow({ estimated_limit_usd: 2400, estimated_remaining_usd: 1800 }),
    monthly: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 })
  },
  plans: [makeProPlan()],
  ...overrides
})

const makeSummary = (): OpenAIOAuthPoolCapacitySummary => ({
  generated_at: '2026-07-14T00:00:00Z',
  five_hour_ratio: 0.15,
  managed_account_count: 5,
  included_account_count: 2,
  excluded_account_count: 3,
  shadow_account_count: 1,
  unknown_plan_account_count: 2,
  unknown_plan_types: [{ plan_type: 'legacy', account_count: 2 }],
  totals: {
    parent: makeWindow({ estimated_limit_usd: 2405, estimated_remaining_usd: 1804 }),
    five_hour: makeWindow({ estimated_limit_usd: 360.75, estimated_remaining_usd: 270.6 }),
    weekly: makeWindow({ estimated_limit_usd: 2400, estimated_remaining_usd: 1800 }),
    monthly: makeWindow({ estimated_limit_usd: 5, estimated_remaining_usd: 4 })
  },
  plans: [makeProPlan(), makeFreePlan()],
  groups: [
    makeGroup({
      group_id: 0,
      group_name: '',
      managed_account_count: 1,
      totals: {
        parent: makeWindow({ estimated_limit_usd: 5, estimated_remaining_usd: 4 }),
        five_hour: makeWindow({ estimated_limit_usd: 0.75, estimated_remaining_usd: 0.6 }),
        weekly: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 }),
        monthly: makeWindow({ estimated_limit_usd: 5, estimated_remaining_usd: 4 })
      },
      plans: [makeFreePlan()]
    }),
    makeGroup(),
    makeGroup({
      group_id: 20,
      group_name: 'Paused group',
      group_status: 'disabled',
      sort_order: 10,
      managed_account_count: 0,
      included_account_count: 0,
      totals: {
        parent: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 }),
        five_hour: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 }),
        weekly: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 }),
        monthly: makeWindow({ estimated_limit_usd: 0, estimated_remaining_usd: 0 })
      },
      plans: []
    })
  ]
})

const mountView = () =>
  mount(OpenAIOAuthCapacityView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true
      }
    }
  })

describe('OpenAIOAuthCapacityView', () => {
  beforeEach(() => {
    getOpenAIOAuthPoolCapacity.mockReset()
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders pool totals, plan details, scope counts, and unknown plans', async () => {
    getOpenAIOAuthPoolCapacity.mockResolvedValue(makeSummary())

    const wrapper = mountView()
    await flushPromises()

    expect(getOpenAIOAuthPoolCapacity).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$1804.00')
    expect(wrapper.get('[data-test="capacity-card-five-hour"]').text()).toContain('$270.60')
    expect(wrapper.get('[data-test="capacity-card-weekly"]').text()).toContain('$1800.00')
    expect(wrapper.get('[data-test="capacity-card-monthly"]').text()).toContain('$4.00')
    expect(wrapper.get('[data-test="capacity-plan-pro"]').text()).toContain('$2400.00')
    expect(wrapper.get('[data-test="capacity-plan-pro"]').text()).toContain('$360.00')
    expect(wrapper.get('[data-test="unknown-plan-warning"]').text()).toContain('legacy')
    expect(wrapper.get('[data-test="unknown-plan-warning"]').text()).toContain('2')
    expect(wrapper.findAll('[data-test^="capacity-group-row-"]')).toHaveLength(3)
    expect(wrapper.get('[data-test="capacity-group-row-20"]').text()).toContain('Paused group')
    expect(wrapper.find('[data-test="capacity-loading"]').exists()).toBe(false)
  })

  it('switches between global, grouped, and ungrouped capacity without another request', async () => {
    getOpenAIOAuthPoolCapacity.mockResolvedValue(makeSummary())

    const wrapper = mountView()
    await flushPromises()

    const selector = wrapper.getComponent(Select)
    expect(selector.get('button').attributes('aria-label')).toBe('admin.openaiOAuthCapacity.groupFilter')
    expect(selector.props('options')).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ value: 'all' }),
        expect.objectContaining({ value: 0 }),
        expect.objectContaining({ value: 10 })
      ])
    )

    selector.vm.$emit('update:modelValue', 10)
    await wrapper.vm.$nextTick()

    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$1800.00')
    expect(wrapper.get('[data-test="capacity-card-five-hour"]').text()).toContain('$270.00')
    expect(wrapper.get('[data-test="capacity-scope-managed"]').text()).toContain('1')
    expect(wrapper.get('[data-test="capacity-scope-included"]').text()).toContain('1')
    expect(wrapper.find('[data-test="unknown-plan-warning"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="capacity-plan-pro"]').text()).toContain('$2400.00')
    expect(wrapper.find('[data-test="capacity-plan-free"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="capacity-group-row-10"]').classes()).toContain('bg-primary-50/70')

    await wrapper.get('[data-test="capacity-group-row-0"] button').trigger('click')

    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$4.00')
    expect(wrapper.getComponent(Select).props('modelValue')).toBe(0)
    expect(getOpenAIOAuthPoolCapacity).toHaveBeenCalledTimes(1)
  })

  it('shows an inline failure state and retries the aggregate request', async () => {
    getOpenAIOAuthPoolCapacity
      .mockRejectedValueOnce(new Error('network error'))
      .mockResolvedValueOnce(makeSummary())

    const wrapper = mountView()
    await flushPromises()

    const errorState = wrapper.get('[data-test="capacity-load-error"]')
    expect(errorState.exists()).toBe(true)

    await errorState.get('button').trigger('click')
    await flushPromises()

    expect(getOpenAIOAuthPoolCapacity).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="capacity-load-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$1804.00')
  })

  it('renders an explicit empty plan state', async () => {
    const response = makeSummary()
    response.plans = response.plans.map((plan) => ({ ...plan, account_count: 0 }))
    response.unknown_plan_account_count = 0
    response.unknown_plan_types = []
    getOpenAIOAuthPoolCapacity.mockResolvedValue(response)

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="capacity-plans-empty"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="unknown-plan-warning"]').exists()).toBe(false)
  })

  it('keeps the previous estimate visible when a refresh fails', async () => {
    getOpenAIOAuthPoolCapacity.mockResolvedValue(makeSummary())

    const wrapper = mountView()
    await flushPromises()

    getOpenAIOAuthPoolCapacity.mockRejectedValueOnce(new Error('refresh failed'))
    await wrapper.get('[data-test="refresh-capacity"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="capacity-refresh-error"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$1804.00')
  })

  it('returns to the global summary when the selected group disappears after refresh', async () => {
    const initial = makeSummary()
    const refreshed = makeSummary()
    refreshed.groups = refreshed.groups.filter((group) => group.group_id !== 10)
    getOpenAIOAuthPoolCapacity.mockResolvedValueOnce(initial).mockResolvedValueOnce(refreshed)

    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent(Select).vm.$emit('update:modelValue', 10)
    await wrapper.vm.$nextTick()
    await wrapper.get('[data-test="refresh-capacity"]').trigger('click')
    await flushPromises()

    expect(wrapper.getComponent(Select).props('modelValue')).toBe('all')
    expect(wrapper.get('[data-test="capacity-card-parent"]').text()).toContain('$1804.00')
  })
})
