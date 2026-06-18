import { describe, it, expect, beforeEach, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'

// vitest.config.ts aliases vue-i18n to its runtime-only build, which ships
// without the JIT message compiler. In that build `translate` cannot resolve
// any message — it logs `[intlify] The message format compilation is not
// supported in this build` and returns the key verbatim (confirmed via a
// minimal probe against both flat and nested messages). ThemeSwitcher calls
// `useI18n().t('theme.*')`, so we mock `vue-i18n` here to provide a working
// translator for the six theme keys. The real keys live in
// src/i18n/locales/{en,zh}.ts; production uses the JIT-enabled vue-i18n build
// (see vite.config.ts "启用 vue-i18n JIT 编译") where this mock is unnecessary.
vi.mock('vue-i18n', () => {
  const messages: Record<string, string> = {
    'theme.system': 'System',
    'theme.light': 'Light',
    'theme.midnight': 'Midnight',
    'theme.carbon': 'Carbon',
    'theme.oled': 'OLED Black',
    'theme.label': 'Theme'
  }
  const t = (key: string) => messages[key] ?? key
  return {
    useI18n: () => ({ t }),
    // Kept for parity with the brief; the plugin object is a no-op under the mock.
    createI18n: () => ({ global: { t } })
  }
})

import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import { setTheme, resolvedTheme, systemPrefersDark } from '@/composables/useTheme'

describe('ThemeSwitcher', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    document.documentElement.removeAttribute('data-theme')
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark') ? false : false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
  })

  const mountIt = () => mount(ThemeSwitcher)

  it('renders all five options when opened', async () => {
    const w = mountIt()
    await w.find('button').trigger('click')
    const labels = w.findAll('[data-test="theme-option"]').map((o) => o.text())
    expect(labels.length).toBe(5)
    expect(labels.some((l) => l.includes('Carbon'))).toBe(true)
  })

  it('marks the active option', async () => {
    setTheme('midnight')
    await flushPromises()
    const w = mountIt()
    await w.find('button').trigger('click')
    const active = w.findAll('[data-test="theme-option"]').filter((o) => o.attributes('aria-checked') === 'true')
    expect(active.length).toBe(1)
    expect(active[0].text()).toContain('Midnight')
  })

  it('switches theme on click', async () => {
    const w = mountIt()
    await w.find('button').trigger('click')
    await w.findAll('[data-test="theme-option"]')[3].trigger('click') // carbon
    expect(resolvedTheme.value).toBe('carbon')
    expect(document.documentElement.getAttribute('data-theme')).toBe('carbon')
  })

  it('marks only the System option active in system mode when OS prefers dark', async () => {
    // Regression: previously resolvedTheme was used for isActive, which made the
    // 'System' option AND the resolved dark variant (e.g. 'Midnight') both
    // report aria-checked="true" at the same time. System mode must be exclusive.
    setTheme('system')
    await flushPromises()
    // OS prefers dark: any matchMedia query containing 'dark' matches.
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
    // Touch systemPrefersDark() so the useTheme singleton picks up the new OS
    // signal (resolvedTheme is reactive on the systemDark mirror it refreshes).
    systemPrefersDark()

    const w = mountIt()
    await w.find('button').trigger('click')
    const checked = w
      .findAll('[data-test="theme-option"]')
      .filter((o) => o.attributes('aria-checked') === 'true')
    expect(checked.length).toBe(1)
    expect(checked[0].text()).toContain('System')
  })
})
