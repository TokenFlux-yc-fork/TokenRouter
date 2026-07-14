import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const routerPath = resolve(dirname(fileURLToPath(import.meta.url)), '../index.ts')
const routerSource = readFileSync(routerPath, 'utf8')

describe('OpenAI OAuth capacity admin route', () => {
  it('registers an admin-only lazy-loaded route with localized metadata', () => {
    const routeMatch = routerSource.match(
      /\{\s*path: '\/admin\/openai-oauth-capacity',[\s\S]*?descriptionKey: 'admin\.openaiOAuthCapacity\.description'\s*\}/
    )

    expect(routeMatch).not.toBeNull()
    expect(routeMatch?.[0]).toContain("name: 'AdminOpenAIOAuthCapacity'")
    expect(routeMatch?.[0]).toContain("import('@/views/admin/OpenAIOAuthCapacityView.vue')")
    expect(routeMatch?.[0]).toContain('requiresAuth: true')
    expect(routeMatch?.[0]).toContain('requiresAdmin: true')
    expect(routeMatch?.[0]).toContain("titleKey: 'admin.openaiOAuthCapacity.title'")
  })
})
