export function formatBuildIdentity(version: string, forkID = ''): string {
  const normalizedVersion = version.trim()
  if (!normalizedVersion) return ''

  const normalizedForkID = forkID.trim()
  return `v${normalizedVersion}${normalizedForkID ? ` · ${normalizedForkID}` : ''}`
}
