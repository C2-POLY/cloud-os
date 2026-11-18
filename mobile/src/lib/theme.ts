import { useColorScheme } from 'react-native'

// The desktop's palette (web/src/index.css), light and dark.
const light = { bg: '#f7f7f5', fg: '#1d1d1b', muted: '#6b6b66', line: '#e2e2dd', surface: '#ffffff', accent: '#2f6fdf', folder: '#c98a17', bad: '#b3261e' }
const dark = { bg: '#161615', fg: '#ececea', muted: '#9a9a94', line: '#2c2c2a', surface: '#1f1f1d', accent: '#79a6f2', folder: '#e0b04a', bad: '#f2786f' }

export type Theme = typeof light

export function useTheme(): Theme {
  return useColorScheme() === 'dark' ? dark : light
}
