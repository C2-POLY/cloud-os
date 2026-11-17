import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DismissButton, Modal } from '../drive/Dialogs.tsx'
import { FileList } from '../drive/FileList.tsx'
import type { Entry } from '../drive/files.ts'
import { animatedRows } from './animate.ts'
import { prefersReducedMotion } from './reducedMotion.ts'
import { SmoothScroll } from './SmoothScroll.tsx'

/** Answers the reduced-motion media query as the system would. */
function systemMotion(reduce: boolean) {
  vi.stubGlobal('matchMedia', (media: string) => ({
    matches: reduce && media.includes('prefers-reduced-motion: reduce'),
    media,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

/**
 * A stand-in for the Web Animations API, which jsdom lacks: it records the
 * animated elements and finishes each animation after its duration.
 */
function webAnimations() {
  const animated: Element[] = []
  Object.defineProperty(Element.prototype, 'animate', {
    configurable: true,
    value(this: Element, _keyframes: unknown, timing: { duration?: number; delay?: number } = {}) {
      animated.push(this)
      const animation = {
        onfinish: null as null | (() => void),
        playState: 'running',
        playbackRate: 1,
        startTime: 0,
        currentTime: 0,
        cancel() {},
        pause() {},
        play() {},
        finish() {},
        commitStyles() {},
      }
      setTimeout(() => {
        animation.playState = 'finished'
        animation.onfinish?.()
      }, (timing.duration ?? 0) + (timing.delay ?? 0))
      return animation
    },
  })
  return animated
}

const files = (n: number, prefix = 'f'): Entry[] =>
  Array.from({ length: n }, (_, i) => ({
    path: `${prefix}${String(i).padStart(4, '0')}.txt`,
    name: `${prefix}${String(i).padStart(4, '0')}.txt`,
    kind: 'file',
    size: 1,
    mime: 'text/plain',
    modified_at: '2026-11-17T10:00:00Z',
  }))

const noop = () => {}
const list = (entries: Entry[], folder = 'Docs') => (
  <FileList folder={folder} entries={entries} onRename={noop} onMove={noop} onTrash={noop} onVersions={noop} onShare={noop} />
)
const styledRows = () => Array.from(document.querySelectorAll('tbody tr')).filter((r) => r.getAttribute('style'))

afterEach(() => {
  vi.unstubAllGlobals()
  delete (Element.prototype as { animate?: unknown }).animate
  document.documentElement.className = ''
})

describe('reduced motion', () => {
  it('follows the system setting, and counts a missing matchMedia as reduced', () => {
    expect(prefersReducedMotion()).toBe(true) // jsdom has no matchMedia
    systemMotion(true)
    expect(prefersReducedMotion()).toBe(true)
    systemMotion(false)
    expect(prefersReducedMotion()).toBe(false)
  })

  it('keeps the native scrolling: Lenis is not mounted', () => {
    systemMotion(true)
    render(
      <SmoothScroll>
        <p>page</p>
      </SmoothScroll>,
    )
    expect(screen.getByText('page')).toBeInTheDocument()
    expect(document.documentElement).not.toHaveClass('lenis')
  })

  it('shows the rows of a listing as they are, without inline styles', async () => {
    systemMotion(true)
    render(list(files(50)))
    await act(async () => {})
    expect(document.querySelectorAll('tbody tr')).toHaveLength(50)
    expect(styledRows()).toHaveLength(0)
  })

  it('closes a dialog at once on Escape', async () => {
    systemMotion(true)
    const onClose = vi.fn()
    render(<Modal title="Essai" onClose={onClose}>contenu</Modal>)
    expect(screen.getByRole('dialog').getAttribute('style')).toBeNull()
    fireEvent.keyDown(window, { key: 'Escape' })
    await act(async () => {})
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('with motion', () => {
  it('mounts Lenis on the page', () => {
    systemMotion(false)
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    )
    render(
      <SmoothScroll>
        <p>page</p>
      </SmoothScroll>,
    )
    expect(document.documentElement).toHaveClass('lenis')
  })

  it('animates only the first rows of a long listing, and only when the folder changes', async () => {
    systemMotion(false)
    const animated = webAnimations()
    const rows = () => [...new Set(animated.filter((el) => el.tagName === 'TR'))] // one animation per property
    const { rerender } = render(list(files(500)))
    expect(rows()).toHaveLength(animatedRows)
    expect(rows()).toEqual(Array.from(document.querySelectorAll('tbody tr')).slice(0, animatedRows))

    rerender(list(files(500).slice(1))) // same folder reloaded: nothing moves
    expect(rows()).toHaveLength(animatedRows)

    rerender(list(files(10, 'g'), 'Autre')) // another folder: its rows come in
    expect(rows()).toHaveLength(animatedRows + 10)
    await waitFor(() => expect(styledRows()).toHaveLength(0)) // no inline style left behind
  })

  it('closes a dialog after its animation, once, whatever closes it', async () => {
    systemMotion(false)
    webAnimations()
    const onClose = vi.fn()
    render(
      <Modal title="Essai" onClose={onClose}>
        <DismissButton>Annuler</DismissButton>
      </Modal>,
    )
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Annuler' }))
    await act(async () => {})
    expect(onClose).not.toHaveBeenCalled() // still fading out
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    await new Promise((r) => setTimeout(r, 300))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes a dialog even when the browser holds its animation back (background tab)', async () => {
    systemMotion(false)
    Object.defineProperty(Element.prototype, 'animate', {
      configurable: true,
      value: () => ({ onfinish: null, playState: 'running', playbackRate: 1, startTime: 0, currentTime: 0, cancel() {}, pause() {}, play() {} }),
    })
    const onClose = vi.fn()
    render(<Modal title="Essai" onClose={onClose}>contenu</Modal>)
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1), { timeout: 1000 })
  })
})
