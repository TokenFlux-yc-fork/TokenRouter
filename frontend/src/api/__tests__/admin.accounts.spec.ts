import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post
  }
}))

import {
  getOpenAIOAuthPoolCapacity,
  syncFromCrs,
  type OpenAIOAuthPoolCapacitySummary
} from '@/api/admin/accounts'

describe('admin accounts API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('fetches the OpenAI OAuth pool capacity summary', async () => {
    const windowSummary = {
      estimated_limit_usd: 2400,
      estimated_used_usd: 600,
      estimated_remaining_usd: 1800,
      observed_remaining_usd: 1800,
      unobserved_limit_usd: 0,
      observed_account_count: 1,
      missing_snapshot_count: 0,
      stale_snapshot_count: 0
    }
    const response: OpenAIOAuthPoolCapacitySummary = {
      generated_at: '2026-07-14T00:00:00Z',
      five_hour_ratio: 0.15,
      managed_account_count: 1,
      included_account_count: 1,
      excluded_account_count: 0,
      shadow_account_count: 0,
      unknown_plan_account_count: 0,
      unknown_plan_types: [],
      totals: {
        parent: windowSummary,
        five_hour: windowSummary,
        weekly: windowSummary,
        monthly: { ...windowSummary, estimated_limit_usd: 0, estimated_remaining_usd: 0 }
      },
      plans: [
        {
          plan_type: 'pro',
          period: 'weekly',
          account_count: 1,
          limit_per_account_usd: 2400,
          five_hour_limit_per_account_usd: 360,
          parent: windowSummary,
          five_hour: windowSummary
        }
      ],
      groups: []
    }
    get.mockResolvedValue({ data: response })

    const result = await getOpenAIOAuthPoolCapacity()

    expect(get).toHaveBeenCalledWith('/admin/accounts/openai-oauth-capacity')
    expect(result).toEqual(response)
  })

  it('uses a dedicated 180 second timeout for CRS synchronization', async () => {
    const payload = {
      base_url: 'https://crs.example.com',
      username: 'admin',
      password: 'secret',
      sync_proxies: true,
      selected_account_ids: ['crs-1']
    }
    const response = {
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      items: []
    }
    post.mockResolvedValue({ data: response })

    const result = await syncFromCrs(payload)

    expect(post).toHaveBeenCalledWith('/admin/accounts/sync/crs', payload, {
      timeout: 180_000
    })
    expect(result).toEqual(response)
  })
})
