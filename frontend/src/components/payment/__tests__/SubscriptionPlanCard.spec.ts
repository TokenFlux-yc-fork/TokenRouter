import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { SubscriptionPlan } from '@/types/payment'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number | null | undefined) => String(value ?? ''),
  }),
}))

import SubscriptionPlanCard from '../SubscriptionPlanCard.vue'

// 统一构造套餐卡片，便于覆盖平台和币种的组合展示。
const mountPlanCard = (
  groupPlatform: string,
  currency = '',
  originalPrice?: number,
  overrides: Partial<SubscriptionPlan> = {},
) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        group_id: 10,
        group_platform: groupPlatform,
        name: 'Pro',
        description: '',
        price: 10,
        original_price: originalPrice,
        currency,
        validity_days: 30,
        validity_unit: 'day',
        features: [],
        for_sale: true,
        sort_order: 1,
        supported_model_scopes: ['claude', 'gemini_text', 'gemini_image'],
        ...overrides,
      },
    },
  })

describe('SubscriptionPlanCard', () => {
  it('does not show Antigravity model scopes for OpenAI plans', () => {
    const text = mountPlanCard('openai').text()

    expect(text).not.toContain('Claude')
    expect(text).not.toContain('Gemini')
    expect(text).not.toContain('Imagen')
  })

  it('shows model scopes for Antigravity plans', () => {
    const text = mountPlanCard('antigravity').text()

    expect(text).toContain('Claude')
    expect(text).toContain('Gemini')
    expect(text).toContain('Imagen')
  })

  // 卡片必须同时兼容历史单数单位和管理端曾写入的复数单位。
  it('renders plural validity units instead of mislabeled days', () => {
    expect(mountPlanCard('openai', '', undefined, { validity_days: 1, validity_unit: 'months' }).text()).toContain('/ payment.perMonth')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 3, validity_unit: 'months' }).text()).toContain('/ 3payment.months')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 2, validity_unit: 'weeks' }).text()).toContain('/ 2payment.weeks')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 30, validity_unit: 'day' }).text()).toContain('/ 30payment.days')
  })

  it('uses the configured currency symbol on current and original prices', () => {
    const text = mountPlanCard('openai', 'NZD', 20).text()

    expect(text).toContain('NZ$10NZD')
    expect(text).toContain('NZ$20NZD')
    expect(mountPlanCard('openai', 'CNY', 20).text()).toContain('¥10CNY')
    expect(mountPlanCard('openai').text()).toContain('$10')
  })
})
