import { afterEach, expect, it, vi } from 'vitest'
import { enter } from './animate.ts'

afterEach(() => {
  vi.unstubAllGlobals()
  delete (Element.prototype as { animate?: unknown }).animate
})

// A browser whose frame clock does not run (a background tab, a paused
// preview): the animation never ends on its own, only when finish() is called.
it('never leaves an entering element invisible when animations stall', async () => {
  vi.stubGlobal('matchMedia', (media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} }))
  const finished: string[] = []
  Object.defineProperty(Element.prototype, 'animate', {
    configurable: true,
    value(this: HTMLElement) {
      this.style.opacity = '0' // what a stalled first frame shows
      const animation = {
        onfinish: null as null | (() => void),
        playState: 'running',
        playbackRate: 1,
        startTime: 0,
        currentTime: 0,
        cancel() {},
        pause() {},
        play() {},
        commitStyles() {},
        finish: () => {
          animation.playState = 'finished'
          finished.push('finish')
          animation.onfinish?.()
        },
      }
      return animation
    },
  })
  const el = document.createElement('section')
  document.body.append(el)
  enter(el, { scale: 0.97 })
  expect(el.style.opacity).toBe('0')
  // One animation per property (opacity, transform), each completed.
  await vi.waitFor(() => expect(finished.length).toBeGreaterThan(0), { timeout: 2000 })
  await vi.waitFor(() => expect(el.style.opacity).toBe(''))
})
