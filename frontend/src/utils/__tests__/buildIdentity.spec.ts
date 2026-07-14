import { describe, expect, it } from 'vitest'

import { formatBuildIdentity } from '@/utils/buildIdentity'

describe('formatBuildIdentity', () => {
  it('renders the upstream version and fork id as separate identity parts', () => {
    expect(formatBuildIdentity('0.1.224', 'yc-fork')).toBe('v0.1.224 · yc-fork')
  })

  it('omits the separator for an upstream build without a fork id', () => {
    expect(formatBuildIdentity('0.1.224', '')).toBe('v0.1.224')
  })
})
