import { useSyncExternalStore } from 'react'

const query = '(prefers-reduced-motion: reduce)'

/**
 * Whether the person asked the system for less motion. Without matchMedia
 * (tests, very old browsers) it answers yes: no animation is the safe default.
 */
export function prefersReducedMotion(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return true
  return window.matchMedia(query).matches
}

function subscribe(onChange: () => void): () => void {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {}
  const list = window.matchMedia(query)
  list.addEventListener('change', onChange)
  return () => list.removeEventListener('change', onChange)
}

/** prefersReducedMotion as a hook, updated when the system setting changes. */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, prefersReducedMotion, () => true)
}
