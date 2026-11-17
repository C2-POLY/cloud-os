import { useEffect, useState } from 'react'
import { navigate } from '../router.ts'
import { appInfo, appOf, type AppId } from './apps.ts'

/** An open window: its app and the address it shows. */
export type Win = { id: AppId; path: string }

/** Where a window sits on the desktop, in CSS pixels. */
export type Geometry = { x: number; y: number; w: number; h: number; max: boolean }

/**
 * The open windows, back to front, with the URL as the source of truth for the
 * front one: going to an app's address opens it (or brings it forward) at that
 * address, so links, Back and Forward work as before. The other windows keep
 * the address they last showed.
 */
export function useWindows(path: string, allowed: (id: AppId) => boolean) {
  const [open, setOpen] = useState<Win[]>([])
  const front = appOf(path)
  const frontAllowed = front !== null && allowed(front)

  const windows: Win[] = frontAllowed ? [...open.filter((w) => w.id !== front), { id: front, path }] : open

  // A new address for the front window is stored while rendering (React's
  // pattern for state that follows a prop), not in an effect after the paint.
  const last = open.at(-1)
  if (frontAllowed && (last?.id !== front || last.path !== path)) setOpen(windows)

  // Any other address, or an app this account cannot open, is the desktop.
  useEffect(() => {
    if (path !== '/' && !frontAllowed) navigate('/', true)
  }, [path, frontAllowed])

  return {
    windows,
    front: frontAllowed ? front : null,
    /** Brings a window forward, or opens its app at its home. */
    show(id: AppId) {
      navigate(windows.find((w) => w.id === id)?.path ?? appInfo(id).home)
    },
    close(id: AppId) {
      const rest = windows.filter((w) => w.id !== id)
      setOpen(rest)
      if (id === front) navigate(rest.at(-1)?.path ?? '/')
    },
  }
}

const storageKey = 'cloudos.windows.v1'

function readGeometries(): Partial<Record<AppId, Geometry>> {
  try {
    const raw = window.localStorage.getItem(storageKey)
    return raw ? (JSON.parse(raw) as Partial<Record<AppId, Geometry>>) : {}
  } catch {
    return {}
  }
}

/** The windows' places, kept in this browser only; a missing store is not an error. */
export function useGeometries() {
  const [all, setAll] = useState(readGeometries)
  return {
    get(id: AppId, index: number): Geometry {
      return all[id] ?? defaultGeometry(index)
    },
    set(id: AppId, g: Geometry) {
      const next = { ...all, [id]: g }
      setAll(next)
      try {
        window.localStorage.setItem(storageKey, JSON.stringify(next))
      } catch {
        // Private window or blocked storage: the place lasts until the page closes.
      }
    },
  }
}

/** A new window: large, below the menu bar, each one a little lower and to the right. */
export function defaultGeometry(index: number): Geometry {
  const vw = window.innerWidth || 1280
  const vh = window.innerHeight || 800
  const w = Math.min(1040, vw - 160)
  const h = Math.min(700, vh - 190)
  return { x: Math.max(16, (vw - w) / 2 - 120 + index * 36), y: 64 + index * 28, w, h, max: false }
}

/** Keeps a moved window's title bar reachable: never above the menu bar or off the screen. */
export function clampPosition(x: number, y: number, w: number): { x: number; y: number } {
  const vw = window.innerWidth || 1280
  const vh = window.innerHeight || 800
  return { x: Math.min(Math.max(x, 80 - w), vw - 80), y: Math.min(Math.max(y, 40), vh - 120) }
}
