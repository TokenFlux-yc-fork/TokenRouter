import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get },
  buildGatewayUrl: vi.fn(),
}))

import { getAttemptTimeline, getLatencyHistogram, getTokenStats } from '@/api/admin/ops'

describe('admin ops dashboard API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: {} })
  })

  it('将时长边界序列化为后端约定的逗号参数', async () => {
    await getLatencyHistogram({
      time_range: '1h',
      group_id: 7,
      bucket_boundaries_ms: [1000, 2000, 5000, 10000, 20000],
    })

    expect(get).toHaveBeenCalledWith('/admin/ops/dashboard/latency-histogram', {
      params: {
        time_range: '1h',
        group_id: 7,
        bucket_boundaries_ms: '1000,2000,5000,10000,20000',
      },
      signal: undefined,
    })
  })

  it('request attempt timeline uses encoded request ID endpoint', async () => {
    get.mockResolvedValue({ data: [{ index: 0, at_unix_ms: 1 }] })

    await expect(getAttemptTimeline('req/a')).resolves.toEqual([{ index: 0, at_unix_ms: 1 }])
    expect(get).toHaveBeenCalledWith('/admin/ops/requests/req%2Fa/attempt-timeline')
  })
  it('Token 统计使用新的通用路由', async () => {
    await getTokenStats({ time_range: '30d', platform: 'anthropic', group_id: 7, top_n: 20 })

    expect(get).toHaveBeenCalledWith('/admin/ops/dashboard/token-stats', {
      params: {
        time_range: '30d',
        platform: 'anthropic',
        group_id: 7,
        top_n: 20,
      },
      signal: undefined,
    })
  })
})
