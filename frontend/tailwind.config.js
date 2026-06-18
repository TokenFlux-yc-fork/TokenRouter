/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{vue,js,ts,jsx,tsx}'],
  darkMode: 'class',
  // Theme hook classes are referenced dynamically at runtime (Task 2 applies
  // them via `useTheme`), not as literal tokens in any scanned file. Without
  // safelisting, Tailwind's content scanner would purge the `.theme-*` token
  // blocks defined in src/style.css.
  safelist: ['.theme-midnight', '.theme-carbon', '.theme-oled'],
  theme: {
    extend: {
      colors: {
        // 主色调 - Blue Archive 蓝白主题
        primary: {
          50: '#F6FCFF',
          100: '#DDF4FC',
          200: '#BFEAFF',
          300: '#8BDDF8',
          400: '#4DD4F6',
          500: '#00D2FF',
          600: '#12A7E8',
          700: '#0B8FD8',
          800: '#176F9E',
          900: '#2D4F68',
          950: '#071A2A'
        },
        // 辅助色 - 冰白到品牌深蓝
        accent: {
          50: '#FFFFFF',
          100: '#EAF8FE',
          200: '#CDEFFD',
          300: '#9BDEFA',
          400: '#5BCDF2',
          500: '#1598D8',
          600: '#0B8FD8',
          700: '#176F9E',
          800: '#2D4F68',
          900: '#21465E',
          950: '#071A2A'
        },
        // 深色模式背景 - 参考 Blue Archive GDDark Firefox 主题
        // Palette now reads CSS variables so all `dark-xxx` utilities follow the
        // active theme. `:root` (style.css) holds the default dark values used
        // today; `.theme-midnight` / `.theme-carbon` / `.theme-oled` override them.
        // Colors are stored as RGB triplets (e.g. `53 64 108`) and wrapped with
        // `rgb(var(--x) / <alpha-value>)` so Tailwind v3 opacity modifiers
        // (e.g. `dark:border-dark-600/70`) keep working unchanged.
        dark: {
          50: 'rgb(var(--dark-50) / <alpha-value>)',
          100: 'rgb(var(--dark-100) / <alpha-value>)',
          200: 'rgb(var(--dark-200) / <alpha-value>)',
          300: 'rgb(var(--dark-300) / <alpha-value>)',
          400: 'rgb(var(--dark-400) / <alpha-value>)',
          500: 'rgb(var(--dark-500) / <alpha-value>)',
          600: 'rgb(var(--dark-600) / <alpha-value>)',
          700: 'rgb(var(--dark-700) / <alpha-value>)',
          800: 'rgb(var(--dark-800) / <alpha-value>)',
          900: 'rgb(var(--dark-900) / <alpha-value>)',
          950: 'rgb(var(--dark-950) / <alpha-value>)'
        }
      },
      fontFamily: {
        sans: [
          'system-ui',
          '-apple-system',
          'BlinkMacSystemFont',
          'Segoe UI',
          'Roboto',
          'Helvetica Neue',
          'Arial',
          'PingFang SC',
          'Hiragino Sans GB',
          'Microsoft YaHei',
          'sans-serif'
        ],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco', 'Consolas', 'monospace']
      },
      boxShadow: {
        glass: '0 8px 32px rgba(0, 0, 0, 0.08)',
        'glass-sm': '0 4px 16px rgba(0, 0, 0, 0.06)',
        glow: '0 0 20px rgba(0, 210, 255, 0.28)',
        'glow-lg': '0 0 40px rgba(18, 167, 232, 0.35)',
        card: '0 1px 3px rgba(0, 0, 0, 0.04), 0 1px 2px rgba(0, 0, 0, 0.06)',
        'card-hover': '0 10px 40px rgba(0, 0, 0, 0.08)',
        'inner-glow': 'inset 0 1px 0 rgba(255, 255, 255, 0.1)'
      },
      backgroundImage: {
        'gradient-radial': 'radial-gradient(var(--tw-gradient-stops))',
        'gradient-primary': 'linear-gradient(135deg, #00D2FF 0%, #0B8FD8 100%)',
        'gradient-dark': 'linear-gradient(135deg, #293059 0%, #10182C 100%)',
        'gradient-glass':
          'linear-gradient(135deg, rgba(255,255,255,0.1) 0%, rgba(255,255,255,0.05) 100%)',
        'mesh-gradient':
          'radial-gradient(at 40% 20%, rgba(0, 210, 255, 0.14) 0px, transparent 50%), radial-gradient(at 80% 0%, rgba(139, 221, 248, 0.12) 0px, transparent 50%), radial-gradient(at 0% 50%, rgba(18, 167, 232, 0.1) 0px, transparent 50%)'
      },
      animation: {
        'fade-in': 'fadeIn 0.3s ease-out',
        'slide-up': 'slideUp 0.3s ease-out',
        'slide-down': 'slideDown 0.3s ease-out',
        'slide-in-right': 'slideInRight 0.3s ease-out',
        'scale-in': 'scaleIn 0.2s ease-out',
        'pulse-slow': 'pulse 3s cubic-bezier(0.4, 0, 0.6, 1) infinite',
        shimmer: 'shimmer 2s linear infinite',
        glow: 'glow 2s ease-in-out infinite alternate'
      },
      keyframes: {
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' }
        },
        slideUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideDown: {
          '0%': { opacity: '0', transform: 'translateY(-10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' }
        },
        slideInRight: {
          '0%': { opacity: '0', transform: 'translateX(20px)' },
          '100%': { opacity: '1', transform: 'translateX(0)' }
        },
        scaleIn: {
          '0%': { opacity: '0', transform: 'scale(0.95)' },
          '100%': { opacity: '1', transform: 'scale(1)' }
        },
        shimmer: {
          '0%': { backgroundPosition: '-200% 0' },
          '100%': { backgroundPosition: '200% 0' }
        },
        glow: {
          '0%': { boxShadow: '0 0 20px rgba(0, 210, 255, 0.28)' },
          '100%': { boxShadow: '0 0 30px rgba(18, 167, 232, 0.4)' }
        }
      },
      backdropBlur: {
        xs: '2px'
      },
      borderRadius: {
        '4xl': '2rem'
      }
    }
  },
  plugins: []
}
