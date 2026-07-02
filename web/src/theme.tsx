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
      colorPrimary: '#7B73FF',
      colorBgContainer: 'transparent',
      colorBorder: 'rgba(255,255,255,0.08)',
      colorBgElevated: '#131316',
      borderRadius: 10,
      fontSize: 14,
      colorText: '#EDEDED',
      colorTextSecondary: '#999',
    } : {
      colorPrimary: '#635BFF',
      colorBgContainer: '#FFFFFF',
      colorBorder: 'rgba(0,0,0,0.06)',
      borderRadius: 10,
      fontSize: 14,
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
