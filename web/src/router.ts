import { useSyncExternalStore } from 'react'

// A tiny router over the History API: the page is the URL, Back and Forward
// work, and the core serves index.html for every path it does not know.

const listeners = new Set<() => void>()

function subscribe(listener: () => void) {
  listeners.add(listener)
  window.addEventListener('popstate', listener)
  return () => {
    listeners.delete(listener)
    window.removeEventListener('popstate', listener)
  }
}

/** The current pathname, re-rendering on navigation. */
export function usePath(): string {
  return useSyncExternalStore(subscribe, () => window.location.pathname)
}

/** Goes to path, adding a history entry (or replacing the current one). */
export function navigate(path: string, replace = false) {
  if (path === window.location.pathname) return
  if (replace) window.history.replaceState(null, '', path)
  else window.history.pushState(null, '', path)
  listeners.forEach((l) => l())
}

/** The drive URL of a folder path ("" is the root). */
export function driveURL(folder: string): string {
  return '/drive' + (folder ? '/' + folder.split('/').map(encodeURIComponent).join('/') : '')
}

/** The folder path shown by a drive URL, or null when the URL is not the drive's. */
export function folderFromURL(pathname: string): string | null {
  if (pathname === '/drive' || pathname === '/drive/') return ''
  if (!pathname.startsWith('/drive/')) return null
  try {
    return pathname
      .slice('/drive/'.length)
      .split('/')
      .filter((s) => s !== '')
      .map(decodeURIComponent)
      .join('/')
  } catch {
    return null
  }
}
