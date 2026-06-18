import { describe, it, expect } from 'vitest'
import { getChartColors } from '@/utils/chartTheme'

describe('chartTheme', () => {
  it('returns colors for each resolved theme', () => {
    for (const t of ['light', 'midnight', 'carbon', 'oled'] as const) {
      const c = getChartColors(t)
      expect(c.grid).toMatch(/^#/)
      expect(c.text).toMatch(/^#/)
      expect(c.tooltipBg).toMatch(/^#/)
    }
  })

  it('oled uses pure black tooltip background', () => {
    expect(getChartColors('oled').tooltipBg).toBe('#000000')
  })

  it('carbon uses neutral (non-blue) grid', () => {
    expect(getChartColors('carbon').grid).toBe('#2A2D33')
  })
})
