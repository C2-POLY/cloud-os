// The page's motion, loaded with the page: Lenis for the smooth scroll, the
// reveals, the typed query, the desktop mockup, and the scroll position
// turned into the state of the 3D box. three.js is not here: box3d.ts is
// imported only after the load event, and until then (or without WebGL)
// the SVG poster of the closed box stands in. With "reduce motion" there is
// no Lenis and no reveal, and the box snaps from one fixed view to the next.
import Lenis from 'lenis';
import type { Box, BoxState } from './box3d';

const root = document.documentElement;
root.classList.add('motion-ready');

const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
const wideMQ = matchMedia('(min-width: 960px)');
const darkMQ = matchMedia('(prefers-color-scheme: dark)');
const stage = document.querySelector<HTMLElement>('.stage')!;
const canvas = document.getElementById('box3d') as HTMLCanvasElement;
const callout = document.getElementById('callout') as HTMLElement;
const header = document.querySelector<HTMLElement>('.top')!;

/* ------------------------------------------------------------ theme */

const isDark = () => {
  const t = root.getAttribute('data-theme');
  return t ? t === 'dark' : darkMQ.matches;
};
let box: Box | null = null;
const onTheme = () => {
  if (box) {
    box.theme(isDark());
    box.dirty = true;
  }
};
darkMQ.addEventListener('change', onTheme);
new MutationObserver(onTheme).observe(root, { attributes: true, attributeFilter: ['data-theme'] });

/* ------------------------------------------------------------ smooth scroll and anchors */

let lenis: Lenis | null = null;
if (!reduce) {
  try {
    lenis = new Lenis({ lerp: 0.1, autoRaf: false });
  } catch {
    lenis = null;
  }
}
const bandPx = () => (wideMQ.matches ? 0 : stage.offsetHeight);
document.addEventListener('click', (e) => {
  const a = (e.target as Element).closest<HTMLAnchorElement>('a[href*="#"]');
  if (!a || a.pathname !== location.pathname || !a.hash) return;
  const el = document.getElementById(decodeURIComponent(a.hash.slice(1)));
  if (!el) return;
  e.preventDefault();
  const off = header.offsetHeight + (el.hasAttribute('data-band') && el.id !== 'main' ? bandPx() : 0);
  const top = el.id === 'main' ? 0 : el.getBoundingClientRect().top + scrollY - off;
  if (lenis) lenis.scrollTo(top, { duration: 1.5 });
  else scrollTo({ top, behavior: reduce ? 'auto' : 'smooth' });
  if (el.id === 'main') el.focus({ preventScroll: true });
});

/* ------------------------------------------------------------ reveals */

const reveal = (el: Element) => {
  el.classList.add('in');
  const pal = el.matches('.figure') ? el.querySelector<HTMLElement>('.pal') : null;
  if (pal) typeQuery(pal);
};
document
  .querySelectorAll('section')
  .forEach((s) => s.querySelectorAll<HTMLElement>('[data-in]').forEach((el, i) => el.style.setProperty('--i', String(Math.min(i, 6)))));
const ins = document.querySelectorAll('[data-in]');
if (!reduce && 'IntersectionObserver' in window) {
  const io = new IntersectionObserver(
    (entries) => {
      for (const en of entries)
        if (en.isIntersecting) {
          io.unobserve(en.target);
          reveal(en.target);
        }
    },
    { rootMargin: '0px 0px -6% 0px' },
  );
  ins.forEach((el) => io.observe(el));
} else ins.forEach((el) => el.classList.add('in'));

function typeQuery(pal: HTMLElement) {
  if (reduce) return;
  const q = pal.querySelector<HTMLElement>('[data-type]');
  if (!q) return;
  const full = q.dataset.type ?? '';
  q.textContent = '';
  pal.classList.add('typing');
  let i = 0;
  const step = () => {
    q.textContent = full.slice(0, ++i);
    if (i < full.length) setTimeout(step, 70 + Math.random() * 70);
    else setTimeout(() => pal.classList.remove('typing'), 280);
  };
  setTimeout(step, 600);
}

/* ------------------------------------------------------------ the desktop mockup */

const mockFrame = document.querySelector<HTMLElement>('.mock-frame')!;
const mock = document.querySelector<HTMLElement>('.mock')!;
const locale = mock.dataset.locale || root.lang;
const fmt = {
  time: new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit' }),
  day: new Intl.DateTimeFormat(locale, { weekday: 'long', day: 'numeric', month: 'long' }),
  short: new Intl.DateTimeFormat(locale, { weekday: 'short', day: 'numeric', month: 'short' }),
};
function tick() {
  const n = new Date();
  for (const el of document.querySelectorAll<HTMLElement>('[data-now]')) {
    const k = el.dataset.now;
    if (k === 'time') el.textContent = fmt.time.format(n);
    else if (k === 'day') {
      const d = fmt.day.format(n).replace(',', '');
      el.textContent = d.charAt(0).toUpperCase() + d.slice(1);
    } else el.textContent = fmt.short.format(n).replace(',', '') + ' ' + fmt.time.format(n);
  }
}
tick();
setInterval(tick, 15000);

function fitMock() {
  const w = mockFrame.clientWidth;
  const small = innerWidth < 700;
  mock.classList.toggle('s', small);
  const dw = small ? 360 : 1120;
  const dh = small ? 600 : 680;
  const s = w / dw;
  mock.style.transform = `scale(${s})`;
  mockFrame.style.height = `${Math.round(dh * s)}px`;
}
fitMock();
new ResizeObserver(fitMock).observe(mockFrame);

/* ------------------------------------------------------------ scroll → the state of the box */

// One state per section (data-k): how open the box is, how it is turned,
// which layer stands out, where it sits on screen and whether it shows.
type Key = { open: number; rot: number; elev: number; R: number; nx: number; vis: number; h: number[] };
type State = Record<(typeof FIELDS)[number], number>;
const L = (j: number) => [0, 1, 2, 3, 4].map((i) => (i === j ? 1 : 0));
const KD: Key[] = [
  { open: 0, rot: -0.78, elev: 0.46, R: 1.1, nx: 0.46, vis: 1, h: L(-1) },
  { open: 0.42, rot: -0.5, elev: 0.58, R: 1.42, nx: 0.46, vis: 1, h: L(-1) },
  { open: 1, rot: -0.62, elev: 0.52, R: 1.8, nx: 0.46, vis: 1, h: L(0) },
  { open: 1, rot: -0.36, elev: 0.52, R: 1.8, nx: 0.46, vis: 1, h: L(1) },
  { open: 1, rot: -0.1, elev: 0.5, R: 1.8, nx: 0.46, vis: 1, h: L(2) },
  { open: 1, rot: 0.16, elev: 0.5, R: 1.8, nx: 0.46, vis: 1, h: L(3) },
  { open: 1, rot: 0.44, elev: 0.48, R: 1.8, nx: 0.46, vis: 1, h: L(4) },
  { open: 0, rot: 0.9, elev: 0.46, R: 1.1, nx: 0.46, vis: 0, h: L(-1) },
  { open: 0, rot: -1.5, elev: 0.46, R: 1.1, nx: -0.48, vis: 0, h: L(-1) },
  { open: 0, rot: -0.82, elev: 0.42, R: 1.34, nx: -0.44, vis: 1, h: L(-1) },
];
// On a phone the box sits in the band, a little to the left while a label
// shows; further for the last layer, whose label (« 05 Sauvegarde ») is the
// longest and whose layer comes out furthest to the right.
const KM: Record<number, Partial<Pick<Key, 'nx' | 'R' | 'vis'>>> = {
  0: { nx: 0, R: 1.06 },
  1: { nx: 0, R: 1.35 },
  2: { nx: -0.14 },
  3: { nx: -0.14 },
  4: { nx: -0.14 },
  5: { nx: -0.14 },
  6: { nx: -0.26 },
  7: { vis: 0, nx: 0 },
  8: { vis: 0, nx: 0 },
  9: { vis: 0, nx: 0 },
};
const FIELDS = ['open', 'rot', 'elev', 'R', 'nx', 'vis', 'h0', 'h1', 'h2', 'h3', 'h4'] as const;
const keyAt = (i: number): State => {
  const k = KD[i];
  const m = wideMQ.matches ? {} : KM[i] || {};
  const o = { open: k.open, rot: k.rot, elev: k.elev, R: m.R ?? k.R, nx: m.nx ?? k.nx, vis: m.vis ?? k.vis } as State;
  k.h.forEach((v, j) => (o[`h${j}` as keyof State] = v));
  return o;
};

const sections = [...document.querySelectorAll<HTMLElement>('[data-k]')];
let tops: number[] = [];
const measure = () => {
  tops = sections.map((s) => s.getBoundingClientRect().top + scrollY);
};
measure();
new ResizeObserver(measure).observe(document.body);
addEventListener('load', measure);
wideMQ.addEventListener('change', () => {
  measure();
  fitMock();
  if (box) box.dirty = true;
});

const smooth = (a: number, b: number, x: number) => {
  const t = Math.min(1, Math.max(0, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
};

function target(): State {
  const vh = innerHeight;
  const wide = wideMQ.matches;
  const band = bandPx();
  const head = header.offsetHeight;
  const line = wide ? vh * 0.55 : head + band + (vh - head - band) * 0.3;
  const y = scrollY + line;
  if (reduce) {
    let i = 0;
    for (let k = 1; k < sections.length; k++) if (tops[k] <= y) i = k;
    return keyAt(i);
  }
  const w = (wide ? 0.24 : 0.16) * vh;
  const st = keyAt(0);
  for (let k = 1; k < sections.length; k++) {
    const b = smooth(tops[k] - w, tops[k] + w, y);
    if (b <= 0) break;
    const a = keyAt(k - 1),
      c = keyAt(k);
    for (const f of FIELDS) st[f] += (c[f] - a[f]) * b;
  }
  return st;
}

const cur = target();
const par = { x: 0, y: 0, tx: 0, ty: 0 };
if (!reduce) {
  addEventListener(
    'pointermove',
    (e) => {
      if (e.pointerType !== 'mouse') return;
      par.tx = (e.clientX / innerWidth - 0.5) * 0.22;
      par.ty = (e.clientY / innerHeight - 0.5) * 0.08;
    },
    { passive: true },
  );
}

const shown = { vis: -1, nx: -1 };
let last = performance.now();
let lastSig = '';
function loop(now: number) {
  const dt = Math.min(0.05, (now - last) / 1000);
  last = now;
  if (lenis) lenis.raf(now);
  const tg = target();
  if (reduce) Object.assign(cur, tg);
  else {
    const k = 1 - Math.exp(-dt * 5);
    for (const f of FIELDS) cur[f] += (tg[f] - cur[f]) * k;
    const kp = 1 - Math.exp(-dt * 3);
    par.x += (par.tx - par.x) * kp;
    par.y += (par.ty - par.y) * kp;
  }
  const vis = Math.round(cur.vis * 1000) / 1000;
  if (vis !== shown.vis) {
    stage.style.opacity = String(vis);
    stage.style.visibility = vis < 0.005 ? 'hidden' : 'visible';
    shown.vis = vis;
  }
  const nx = Math.round(cur.nx * 1000) / 1000;
  if (nx !== shown.nx) {
    stage.style.setProperty('--nx', String(nx));
    shown.nx = nx;
  }
  if (box && vis >= 0.005) {
    const t = now / 1000;
    const s: BoxState = {
      open: cur.open,
      R: cur.R,
      nx: cur.nx,
      rot: cur.rot + par.x + (reduce ? 0 : 0.07 * Math.sin(t * 0.32)),
      elev: cur.elev + par.y,
      h: [cur.h0, cur.h1, cur.h2, cur.h3, cur.h4],
    };
    if (reduce) {
      const sig = FIELDS.map((f) => cur[f].toFixed(3)).join();
      if (sig !== lastSig || box.dirty) {
        box.frame(s, t, reduce);
        lastSig = sig;
        box.dirty = false;
      }
    } else {
      box.frame(s, t, reduce);
      box.dirty = false;
    }
  }
  requestAnimationFrame(loop);
}
requestAnimationFrame(loop);

/* ------------------------------------------------------------ the box, in three.js, started late */

// three.js draws with WebGL 2 only: without it the poster stays, and the 3D
// chunk is not even downloaded.
function webgl2() {
  const probe = document.createElement('canvas').getContext('webgl2');
  probe?.getExtension('WEBGL_lose_context')?.loseContext();
  return !!probe;
}

function start() {
  if (!webgl2()) return;
  import('./box3d')
    .then(({ createBox }) => {
      const names = JSON.parse(stage.dataset.layers || '[]') as string[];
      return createBox({ canvas, callout, stage, names, wide: () => wideMQ.matches, dark: isDark() });
    })
    .then((b) => {
      if (!b) return;
      box = b;
      b.theme(isDark());
      b.dirty = true;
      new ResizeObserver(() => {
        b.resize();
        b.dirty = true;
      }).observe(canvas);
    })
    .catch(() => {});
}
const later = () => ('requestIdleCallback' in window ? requestIdleCallback(start, { timeout: 1200 }) : setTimeout(start, 200));
if (document.readyState === 'complete') setTimeout(start, 60);
else addEventListener('load', later, { once: true });
