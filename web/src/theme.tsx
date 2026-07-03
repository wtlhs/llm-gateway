import { createContext, useContext, useState, useEffect, type ReactNode } from 'react'
import { ConfigProvider, theme as antdTheme } from 'antd'

type ThemeMode = 'light' | 'dark'

interface ThemeCtx {
  mode: ThemeMode
  toggle: () => void
}

const Ctx = createContext<ThemeCtx>({ mode: 'light', toggle: () => {} })

export function useTheme() {
  return useContext(Ctx)
}

const STORAGE_KEY = 'platform_theme'

export function ThemeProvider({ children, locale }: { children: ReactNode; locale?: any }) {
  const [mode, setMode] = useState<ThemeMode>(() => {
    const saved = localStorage.getItem(STORAGE_KEY) as ThemeMode | null
    if (saved) return saved
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  })

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', mode)
    localStorage.setItem(STORAGE_KEY, mode)
  }, [mode])

  const toggle = () => setMode(m => (m === 'light' ? 'dark' : 'light'))

  const config = {
    algorithm: mode === 'dark' ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
    token: mode === 'dark' ? {
      colorPrimary: '#F4F4F5',
      colorBgContainer: 'transparent',
      colorBorder: '#26262A',
      colorBgElevated: '#131314',
      borderRadius: 6,
      fontSize: 14,
      colorText: '#F4F4F5',
      colorTextSecondary: '#A1A1A6',
      fontFamily: "-apple-system, BlinkMacSystemFont, 'Inter', 'PingFang SC', sans-serif",
    } : {
      colorPrimary: '#0D0D0D',
      colorBgContainer: '#FFFFFF',
      colorBorder: '#ECECEE',
      borderRadius: 6,
      fontSize: 14,
      fontFamily: "-apple-system, BlinkMacSystemFont, 'Inter', 'PingFang SC', sans-serif",
    },
  }

  return (
    <Ctx.Provider value={{ mode, toggle }}>
      <ConfigProvider theme={config} locale={locale}>
        {children}
      </ConfigProvider>
    </Ctx.Provider>
  )
}
