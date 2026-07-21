import type { Account } from '@/types'

type QoderAuthorizationAccount = Pick<Account, 'credentials' | 'credentials_status'>

const nonEmptyString = (value: unknown): boolean =>
  typeof value === 'string' && value.trim() !== ''

const hasCredential = (account: QoderAuthorizationAccount, key: string): boolean =>
  account.credentials_status?.[`has_${key}`] === true ||
  nonEmptyString(account.credentials?.[key])

const normalizedCredential = (account: QoderAuthorizationAccount, key: string): string => {
  const value = account.credentials?.[key]
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

export type QoderAuthorizationKind = 'pat' | 'device'

export function getQoderCNAuthorizationKind(
  account: QoderAuthorizationAccount
): QoderAuthorizationKind | null {
  if (normalizedCredential(account, 'site') !== 'cn') return null
  if (hasCredential(account, 'pat')) return 'pat'
  if (normalizedCredential(account, 'refresh_mode') !== 'qodercn20') return null

  const hasIdentity =
    nonEmptyString(account.credentials?.uid) || nonEmptyString(account.credentials?.aid)
  if (
    hasCredential(account, 'security_oauth_token') &&
    hasCredential(account, 'refresh_token') &&
    nonEmptyString(account.credentials?.machine_id) &&
    hasIdentity
  ) {
    return 'device'
  }
  return null
}

export const hasCompleteQoderCNAuthorization = (
  account: QoderAuthorizationAccount
): boolean => getQoderCNAuthorizationKind(account) !== null

export const canRefreshQoderCNAuthorization = (
  account: QoderAuthorizationAccount
): boolean => getQoderCNAuthorizationKind(account) !== null
