import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  useTheme,
  setTheme,
  toggleTheme,
  initTheme,
  resolvedTheme,
  isDark,
  systemPrefersDark
} from '@/composables/useTheme'

describe('useTheme', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    document.documentElement.removeAttribute('data-theme')
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark') ? false : false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
  })

  it('defaults to system and resolves to light when OS is light', () => {
    initTheme()
    expect(resolvedTheme.value).toBe('light')
    expect(isDark.value).toBe(false)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('resolves system→dark (midnight) when OS prefers dark', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
    initTheme()
    expect(resolvedTheme.value).toBe('midnight')
    expect(isDark.value).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.getAttribute('data-theme')).toBe('midnight')
  })

  it('migrates legacy "dark" stored value to midnight', () => {
    localStorage.setItem('theme', 'dark')
    const { theme } = useTheme()
    expect(theme.value).toBe('midnight')
  })

  it('setTheme(carbon) applies carbon and persists', () => {
    setTheme('carbon')
    expect(resolvedTheme.value).toBe('carbon')
    expect(document.documentElement.getAttribute('data-theme')).toBe('carbon')
    expect(localStorage.getItem('theme')).toBe('carbon')
    expect(localStorage.getItem('darkTheme')).toBe('carbon')
  })

  it('setTheme(light) clears .dark and sets data-theme=light', () => {
    setTheme('midnight')
    setTheme('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('setTheme(system) follows OS preference', () => {
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
    setTheme('system')
    expect(resolvedTheme.value).toBe('midnight')
  })

  it('remembers the last dark variant for system→dark', () => {
    setTheme('oled')            // pick oled
    setTheme('system')          // switch to system
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: q.includes('dark'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn()
    }))
    expect(systemPrefersDark()).toBe(true)
    expect(resolvedTheme.value).toBe('oled')
  })

  it('toggleTheme flips light ↔ the current dark variant', () => {
    // Establish a known starting state independent of prior tests:
    // light mode + darkVariant reset to midnight.
    setTheme('midnight')
    setTheme('light')
    // isDark=false → toggle goes to darkVariant (midnight).
    toggleTheme()
    expect(resolvedTheme.value).toBe('midnight')
    expect(isDark.value).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.getAttribute('data-theme')).toBe('midnight')
    // isDark=true → toggle returns to light.
    toggleTheme()
    expect(resolvedTheme.value).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('re-applies when the OS preference changes while in system mode', async () => {
    let osDark = false
    let changeListener: (() => void) | undefined
    // Re-import on a clean module graph so the matchMedia 'change' listener
    // is registered against a stub whose addEventListener captures the callback
    // (the setup.ts polyfill's addEventListener is a no-op, so the listener
    // registered at the top-level import above was never actually captured).
    vi.resetModules()
    vi.stubGlobal('matchMedia', (q: string) => ({
      matches: osDark && q.includes('dark'),
      media: q,
      onchange: null,
      addEventListener: (_e: string, cb: () => void) => { changeListener = cb },
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false
    }))
    const mod = await import('@/composables/useTheme')
    mod.setTheme('system')
    expect(mod.resolvedTheme.value).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')

    // OS preference flips to dark → listener must refresh + re-apply.
    osDark = true
    changeListener!()
    expect(mod.resolvedTheme.value).toBe('midnight')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.getAttribute('data-theme')).toBe('midnight')
  })
})
