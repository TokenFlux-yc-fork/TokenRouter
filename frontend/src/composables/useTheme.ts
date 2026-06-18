import { ref, computed } from 'vue'

export type DarkTheme = 'midnight' | 'carbon' | 'oled'
export type ThemeMode = 'system' | 'light' | DarkTheme
export type ResolvedTheme = 'light' | DarkTheme

type ViewTransition = { ready: Promise<void> }
type ViewTransitionDocument = { startViewTransition?: (cb: () => void) => ViewTransition }

const DARK_THEMES: readonly DarkTheme[] = ['midnight', 'carbon', 'oled']
const themeStorageKey = 'theme' // 'system'|'light'|'midnight'|'carbon'|'oled'
const darkVariantStorageKey = 'darkTheme' // which dark variant the user prefers
const themeRippleDuration = 640

function isDarkTheme(v: string): v is DarkTheme {
  return (DARK_THEMES as readonly string[]).includes(v)
}

function loadStoredTheme(): ThemeMode {
  const saved = localStorage.getItem(themeStorageKey)
  // Backward compatibility: legacy boolean-era values.
  if (saved === 'dark') return 'midnight'
  if (saved === 'light') return 'light'
  if (saved && isDarkTheme(saved)) return saved
  return 'system'
}

function loadStoredDarkVariant(): DarkTheme {
  const saved = localStorage.getItem(darkVariantStorageKey)
  if (saved && isDarkTheme(saved)) return saved
  return 'midnight' // default dark variant (continues brand)
}

const theme = ref<ThemeMode>(loadStoredTheme())
const darkVariant = ref<DarkTheme>(loadStoredDarkVariant())

// Reactive mirror of the OS color-scheme preference. Vue's computed caching
// means resolvedTheme would not otherwise notice when the OS preference flips
// (window.matchMedia().matches is not a tracked dependency), so we route the
// OS signal through this ref. It is refreshed on every systemPrefersDark() call
// and on the matchMedia 'change' event registered at the bottom of this module.
const systemDark = ref(false)

/** Read the live OS preference, update the reactive mirror, return the value. */
function refreshSystemDark(): boolean {
  const matches = typeof window !== 'undefined'
    && window.matchMedia('(prefers-color-scheme: dark)').matches
  systemDark.value = matches
  return matches
}

export function systemPrefersDark(): boolean {
  return refreshSystemDark()
}

export const resolvedTheme = computed<ResolvedTheme>(() => {
  if (theme.value === 'system') {
    return systemDark.value ? darkVariant.value : 'light'
  }
  if (theme.value === 'light') return 'light'
  return theme.value // a DarkTheme
})

export const isDark = computed(() => resolvedTheme.value !== 'light')

const THEME_CLASSES: readonly string[] = DARK_THEMES.map((t) => `theme-${t}`)

function applyResolved(resolved: ResolvedTheme) {
  const html = document.documentElement
  // CSS token blocks (Task 1) are CLASS-based (.theme-midnight/.theme-carbon/.theme-oled),
  // so the class drives the palette. data-theme is kept for semantics only.
  html.classList.remove(...THEME_CLASSES)
  if (resolved === 'light') {
    html.classList.remove('dark')
    html.setAttribute('data-theme', 'light')
  } else {
    html.classList.add('dark', `theme-${resolved}`)
    html.setAttribute('data-theme', resolved)
  }
}

function persist(next: ThemeMode, variant: DarkTheme) {
  theme.value = next
  darkVariant.value = variant
  localStorage.setItem(themeStorageKey, next)
  if (next !== 'light' && next !== 'system') {
    // remember the chosen dark variant for the "system → dark" case
    localStorage.setItem(darkVariantStorageKey, next)
  } else {
    localStorage.setItem(darkVariantStorageKey, variant)
  }
  refreshSystemDark()
  applyResolved(resolvedTheme.value)
}

function supportsAnimatedTheme(event?: MouseEvent): event is MouseEvent {
  const doc = document as unknown as ViewTransitionDocument
  return Boolean(
    event && doc.startViewTransition && !window.matchMedia('(prefers-reduced-motion: reduce)').matches
  )
}

function rippleRadius(x: number, y: number): number {
  return Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y))
}

function animateRipple(transition: ViewTransition, x: number, y: number) {
  void transition.ready
    .then(() => {
      const end = rippleRadius(x, y)
      document.documentElement.animate(
        {
          clipPath: [
            `circle(0px at ${x}px ${y}px)`,
            `circle(${end}px at ${x}px ${y}px)`
          ]
        },
        {
          duration: themeRippleDuration,
          easing: 'cubic-bezier(0.65, 0, 0.35, 1)',
          pseudoElement: '::view-transition-new(root)'
        } as KeyframeAnimationOptions & { pseudoElement: string }
      )
    })
    .catch(() => undefined)
}

export function initTheme() {
  // Applied before mount (called from main.ts) to avoid a flash of the wrong theme.
  refreshSystemDark()
  applyResolved(resolvedTheme.value)
}

export function setTheme(next: ThemeMode, event?: MouseEvent) {
  if (next === theme.value && next !== 'system') return
  const variant: DarkTheme = isDarkTheme(next) ? next : darkVariant.value

  if (!supportsAnimatedTheme(event)) {
    persist(next, variant)
    return
  }
  const { clientX, clientY } = event
  const doc = document as unknown as ViewTransitionDocument
  const transition = doc.startViewTransition?.(() => persist(next, variant))
  if (transition) animateRipple(transition, clientX, clientY)
  else persist(next, variant)
}

export function toggleTheme(event?: MouseEvent) {
  // Quick two-state toggle used by legacy sun/moon buttons (public pages).
  setTheme(isDark.value ? 'light' : darkVariant.value, event)
}

export function useTheme() {
  // Lazy sync: if localStorage now holds a value the module singleton didn't
  // see at import time (legacy 'dark' migration written after load, a cross-tab
  // edit, or HMR), pick it up idempotently so callers observe the migrated state.
  const stored = loadStoredTheme()
  if (stored !== theme.value) {
    theme.value = stored
    refreshSystemDark()
    applyResolved(resolvedTheme.value)
  }
  return {
    theme,
    darkVariant,
    resolvedTheme,
    isDark,
    setTheme,
    toggleTheme,
    DARK_THEMES
  }
}

// Keep the resolved theme in sync when the OS preference changes (system mode).
if (typeof window !== 'undefined') {
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    refreshSystemDark()
    if (theme.value === 'system') applyResolved(resolvedTheme.value)
  })
}
