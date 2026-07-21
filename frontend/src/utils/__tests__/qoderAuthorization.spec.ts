import { describe, expect, it } from 'vitest'
import {
  canRefreshQoderCNAuthorization,
  getQoderCNAuthorizationKind,
  hasCompleteQoderCNAuthorization
} from '@/utils/qoderAuthorization'

describe('qoderAuthorization', () => {
  it('accepts a CN PAT without a refresh mode or refresh token', () => {
    const account = {
      credentials: { site: ' CN ', refresh_mode: 'cosy' },
      credentials_status: { has_pat: true }
    }

    expect(getQoderCNAuthorizationKind(account)).toBe('pat')
    expect(hasCompleteQoderCNAuthorization(account)).toBe(true)
    expect(canRefreshQoderCNAuthorization(account)).toBe(true)
  })

  it('accepts complete redacted Qoder CN device credentials', () => {
    const account = {
      credentials: {
        site: 'cn',
        refresh_mode: 'qodercn20',
        machine_id: 'machine-id',
        aid: 'account-id'
      },
      credentials_status: {
        has_security_oauth_token: true,
        has_refresh_token: true
      }
    }

    expect(getQoderCNAuthorizationKind(account)).toBe('device')
    expect(canRefreshQoderCNAuthorization(account)).toBe(true)
  })

  it.each([
    ['site', { refresh_mode: 'qodercn20', machine_id: 'machine-id', uid: 'user-id' }],
    ['refresh mode', { site: 'cn', refresh_mode: 'cosy', machine_id: 'machine-id', uid: 'user-id' }],
    ['machine id', { site: 'cn', refresh_mode: 'qodercn20', uid: 'user-id' }],
    ['uid or aid', { site: 'cn', refresh_mode: 'qodercn20', machine_id: 'machine-id' }]
  ])('rejects device credentials missing a valid %s', (_, credentials) => {
    expect(hasCompleteQoderCNAuthorization({
      credentials,
      credentials_status: {
        has_security_oauth_token: true,
        has_refresh_token: true
      }
    })).toBe(false)
  })

  it.each(['has_security_oauth_token', 'has_refresh_token'])(
    'rejects device credentials missing %s',
    (missingStatus) => {
      const credentialsStatus: Record<string, boolean> = {
        has_security_oauth_token: true,
        has_refresh_token: true
      }
      delete credentialsStatus[missingStatus]

      expect(hasCompleteQoderCNAuthorization({
        credentials: {
          site: 'cn',
          refresh_mode: 'qodercn20',
          machine_id: 'machine-id',
          uid: 'user-id'
        },
        credentials_status: credentialsStatus
      })).toBe(false)
    }
  )
})
