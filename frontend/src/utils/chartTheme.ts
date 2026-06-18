import type { ResolvedTheme } from '@/composables/useTheme'

export interface ChartColors {
  grid: string
  text: string
  tooltipBg: string
  tooltipTitle: string
  tooltipBody: string
}

const CHART_COLORS: Record<ResolvedTheme, ChartColors> = {
  light: {
    grid: '#f3f4f6',
    text: '#6b7280',
    tooltipBg: '#ffffff',
    tooltipTitle: '#111827',
    tooltipBody: '#4b5563'
  },
  midnight: {
    grid: '#2C365A',
    text: '#93A6CC',
    tooltipBg: '#0E1626',
    tooltipTitle: '#FFFFFF',
    tooltipBody: '#D5E5FB'
  },
  carbon: {
    grid: '#2A2D33',
    text: '#9CA3AF',
    tooltipBg: '#0E0F12',
    tooltipTitle: '#FAFAFA',
    tooltipBody: '#C9CDD4'
  },
  oled: {
    grid: '#262626',
    text: '#A3A3A3',
    tooltipBg: '#000000',
    tooltipTitle: '#FFFFFF',
    tooltipBody: '#D4D4D4'
  }
}

export function getChartColors(theme: ResolvedTheme): ChartColors {
  return CHART_COLORS[theme]
}
