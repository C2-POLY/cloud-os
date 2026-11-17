import { animate } from 'motion/mini'
import { prefersReducedMotion } from './reducedMotion.ts'

const enterSeconds = 0.2

/** How many rows of a listing come in animated; the rest are shown as they are. */
export const animatedRows = 30

function elements(targets: Element | null | (Element | null)[]): HTMLElement[] {
  return (Array.isArray(targets) ? targets : [targets]).filter((t): t is HTMLElement => t instanceof HTMLElement)
}

/**
 * Fades elements in from a few pixels below (or from a slightly smaller size),
 * one after the other when `step` (seconds) is set. It runs on the browser's Web
 * Animations API, changes only opacity and transform, and leaves no inline style
 * behind. Nothing happens with reduced motion.
 */
export function enter(targets: Element | null | (Element | null)[], { y = 0, scale = 1, step = 0 }: { y?: number; scale?: number; step?: number } = {}): void {
  const list = elements(targets)
  if (list.length === 0 || prefersReducedMotion()) return
  const keyframes: Record<string, (string | number)[]> = { opacity: [0, 1] }
  if (y || scale !== 1) keyframes.transform = [`translateY(${y}px) scale(${scale})`, 'none']
  list.forEach((el, i) => {
    const delay = i * step
    const controls = animate(el, keyframes, { duration: enterSeconds, ease: 'easeOut', delay })
    void controls.then(() => {
      el.style.removeProperty('opacity')
      el.style.removeProperty('transform')
    })
    // A browser that holds back animations (a background tab, a paused frame
    // clock) must not leave the element invisible: it is completed shortly
    // after its own end.
    setTimeout(() => controls.complete(), (enterSeconds + delay) * 1000 + 300)
  })
}

const leaveSeconds = 0.14

/**
 * Fades elements out (shrinking to `scale` when set), then resolves; the final
 * style stays, so nothing flashes before the caller unmounts them. With reduced
 * motion it resolves at once. A browser that holds back the animations of a
 * background tab cannot hold the caller: it resolves at the latest shortly after
 * the animation's own duration.
 */
export function leave(targets: Element | null | (Element | null)[], { scale = 1 }: { scale?: number } = {}): Promise<void> {
  const list = elements(targets)
  if (list.length === 0 || prefersReducedMotion()) return Promise.resolve()
  const keyframes: Record<string, string | number> = { opacity: 0 }
  if (scale !== 1) keyframes.transform = `scale(${scale})`
  const done = Promise.all(list.map((el) => animate(el, keyframes, { duration: leaveSeconds, ease: 'easeIn' }).then(() => {})))
  const deadline = new Promise((resolve) => setTimeout(resolve, leaveSeconds * 1000 + 100))
  return Promise.race([done, deadline]).then(() => {})
}
