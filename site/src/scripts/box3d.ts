// The box in three.js: an aluminium base, five layers (Files, Photos, Search,
// Access, Backup) and a lid, drawn from the state page.ts gives each frame.
// Imported by page.ts only after the load event, so three.js never weighs on
// the first load, and built in short steps (yielding between them, shaders
// compiled in parallel) so it does not hold the page's main thread for long.
// Without WebGL, createBox resolves to null and the SVG poster of the closed
// box stays in place.
import * as T from 'three';

export type BoxState = { open: number; rot: number; elev: number; R: number; nx: number; h: number[] };
export type Box = {
  frame: (s: BoxState, t: number, still: boolean) => void;
  resize: () => void;
  theme: (dark: boolean) => void;
  dirty: boolean;
};
type Options = {
  canvas: HTMLCanvasElement;
  callout: HTMLElement;
  stage: HTMLElement;
  /** The layers' names, from the top, in the page's language. */
  names: string[];
  /** Whether the page is in its wide layout (the box beside the text). */
  wide: () => boolean;
  /** The theme on screen when the box is built. */
  dark: boolean;
};
type Layer = {
  name: string;
  color: string;
  darkColor?: string;
  n: string;
  group: T.Group;
  cap: T.MeshStandardMaterial;
  seam: T.MeshStandardMaterial;
  seamMesh: T.Mesh;
  details: T.Group;
  tints: { mat: T.MeshStandardMaterial; base: T.Color }[];
  capBase: T.Color;
  seamBase: T.Color;
  baseY: number;
  anim: ((t: number, h: number, still: boolean) => void) | null;
};

const smooth = (a: number, b: number, x: number) => {
  const t = Math.min(1, Math.max(0, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
};

// Lets the browser handle input and paint between two steps of the build.
const pause = () => new Promise<void>((r) => setTimeout(r, 0));

export async function createBox({ canvas, callout, stage, names, wide: isWide, dark: startDark }: Options): Promise<Box | null> {
  let renderer: T.WebGLRenderer;
  try {
    renderer = new T.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: 'low-power' });
  } catch {
    return null;
  }
  renderer.setClearColor(0x000000, 0);
  renderer.outputColorSpace = T.SRGBColorSpace;
  renderer.toneMapping = T.NeutralToneMapping;

  const scene = new T.Scene();
  const camera = new T.PerspectiveCamera(26, 1, 0.1, 80);

  const canvasTex = (w: number, h: number, draw: (g: CanvasRenderingContext2D, w: number, h: number) => void) => {
    const c = document.createElement('canvas');
    c.width = w;
    c.height = h;
    draw(c.getContext('2d')!, w, h);
    const t = new T.CanvasTexture(c);
    t.colorSpace = T.SRGBColorSpace;
    return t;
  };

  // A studio made of soft boxes, with a faint glow of the wallpaper's colours
  // near the horizon, so the aluminium picks up the product's light.
  const envFor = (dark: boolean) => {
    const t = canvasTex(512, 256, (g, w, h) => {
      const sky = g.createLinearGradient(0, 0, 0, h);
      if (dark) {
        sky.addColorStop(0, '#5c5c62');
        sky.addColorStop(0.46, '#323235');
        sky.addColorStop(0.54, '#1e1e20');
        sky.addColorStop(1, '#101011');
      } else {
        sky.addColorStop(0, '#ffffff');
        sky.addColorStop(0.46, '#f0f0ed');
        sky.addColorStop(0.54, '#d6d6d1');
        sky.addColorStop(1, '#b4b4ae');
      }
      g.fillStyle = sky;
      g.fillRect(0, 0, w, h);
      // Drawn three times across the seam, so the map wraps without a hard edge.
      const soft = (x: number, y: number, rx: number, ry: number, a: number, col: string) => {
        for (const dx of [-w, 0, w]) {
          g.save();
          g.translate(x + dx, y);
          g.scale(rx, ry);
          const r = g.createRadialGradient(0, 0, 0, 0, 0, 1);
          r.addColorStop(0, `rgba(${col},${a})`);
          r.addColorStop(1, `rgba(${col},0)`);
          g.fillStyle = r;
          g.beginPath();
          g.arc(0, 0, 1, 0, Math.PI * 2);
          g.fill();
          g.restore();
        }
      };
      const k = dark ? 0.6 : 1;
      soft(140, 70, 110, 46, 1 * k, '255,255,255');
      soft(370, 86, 70, 32, 0.85 * k, '255,255,255');
      soft(256, 12, 260, 22, 0.6 * k, '255,255,255');
      soft(70, 132, 100, 18, 0.32 * k, '255,154,98');
      soft(300, 134, 90, 16, 0.22 * k, '255,95,143');
      soft(460, 132, 90, 18, 0.28 * k, '122,92,255');
    });
    t.mapping = T.EquirectangularReflectionMapping;
    const pmrem = new T.PMREMGenerator(renderer);
    const rt = pmrem.fromEquirectangular(t);
    pmrem.dispose();
    t.dispose();
    return rt.texture;
  };
  // Only the theme on screen is lit at first; the other one is made the
  // first time the visitor switches to it.
  const envs: { light?: T.Texture; dark?: T.Texture } = {};
  const envOf = (d: boolean) => (d ? (envs.dark ??= envFor(true)) : (envs.light ??= envFor(false)));
  envOf(startDark);
  await pause();

  const key = new T.DirectionalLight(0xffffff, 1.2);
  key.position.set(-2.5, 5, 3.5);
  scene.add(key);
  const hemi = new T.HemisphereLight(0xffffff, 0xcfcfca, 0.35);
  scene.add(hemi);

  /* geometry */
  const W = 1.55,
    RAD = 0.36,
    BEV = 0.022,
    TH = 0.085,
    LID = 0.15,
    BASE = 0.095;
  const rr = (w: number, d: number, r: number) => {
    const s = new T.Shape(),
      x = w / 2 - r,
      y = d / 2 - r;
    s.moveTo(-x, -d / 2);
    s.lineTo(x, -d / 2);
    s.absarc(x, -y, r, -Math.PI / 2, 0, false);
    s.lineTo(w / 2, y);
    s.absarc(x, y, r, 0, Math.PI / 2, false);
    s.lineTo(-x, d / 2);
    s.absarc(-x, y, r, Math.PI / 2, Math.PI, false);
    s.lineTo(-w / 2, -y);
    s.absarc(-x, -y, r, Math.PI, Math.PI * 1.5, false);
    return s;
  };
  // A rounded slab lying flat, from y = 0 to y = th, its sides at ±w/2.
  const slab = (w: number, d: number, r: number, th: number, b = BEV, seg = 24) => {
    b = Math.min(b, th * 0.45);
    const g = new T.ExtrudeGeometry(rr(w - 2 * b, d - 2 * b, Math.max(r - b, 0.002)), {
      depth: Math.max(th - 2 * b, 0.001),
      bevelEnabled: true,
      bevelThickness: b,
      bevelSize: b,
      bevelSegments: 4,
      curveSegments: seg,
    });
    g.rotateX(-Math.PI / 2);
    g.translate(0, b, 0);
    return g;
  };
  // A thin plate standing up, facing the front.
  const plate = (w: number, h: number, r: number, depth: number) => {
    const g = new T.ExtrudeGeometry(rr(w, h, r), { depth, bevelEnabled: false, curveSegments: 8 });
    g.translate(0, h / 2, -depth / 2);
    return g;
  };
  const seamGeo = (() => {
    const o = 0.0035,
      i = 0.03;
    const s = rr(W + 2 * o, W + 2 * o, RAD + o);
    s.holes.push(rr(W - 2 * i, W - 2 * i, RAD - i));
    const g = new T.ExtrudeGeometry(s, { depth: 0.009, bevelEnabled: false, curveSegments: 32 });
    g.rotateX(-Math.PI / 2);
    return g;
  })();

  /* materials */
  const alu = new T.MeshStandardMaterial({ color: 0xdadce0, metalness: 0.72, roughness: 0.3 });
  const aluTop = new T.MeshStandardMaterial({ color: 0xdfe1e4, metalness: 0.66, roughness: 0.36 });
  const rubber = new T.MeshStandardMaterial({ color: 0x2a2a2c, roughness: 0.9 });
  const paper = new T.MeshStandardMaterial({ color: 0xf6f6f3, roughness: 0.7 });
  const neutral = new T.Color();

  const ledTex = canvasTex(8, 128, (g, w, h) => {
    const gr = g.createLinearGradient(0, 0, 0, h);
    gr.addColorStop(0, '#ff9a62');
    gr.addColorStop(0.5, '#7a5cff');
    gr.addColorStop(1, '#ff5f8f');
    g.fillStyle = gr;
    g.fillRect(0, 0, w, h);
  });
  const coreTex = canvasTex(128, 128, (g, w, h) => {
    const gr = g.createLinearGradient(0, 0, w, h);
    gr.addColorStop(0, '#ff9a62');
    gr.addColorStop(0.5, '#7a5cff');
    gr.addColorStop(1, '#ff5f8f');
    g.fillStyle = gr;
    g.beginPath();
    g.roundRect(4, 4, w - 8, h - 8, 22);
    g.fill();
  });
  const glowTex = canvasTex(128, 128, (g) => {
    const r = g.createRadialGradient(64, 64, 0, 64, 64, 64);
    r.addColorStop(0, 'rgba(255,255,255,1)');
    r.addColorStop(0.35, 'rgba(255,255,255,.35)');
    r.addColorStop(1, 'rgba(255,255,255,0)');
    g.fillStyle = r;
    g.fillRect(0, 0, 128, 128);
  });
  const shadowTex = canvasTex(256, 256, (g) => {
    const r = g.createRadialGradient(128, 128, 0, 128, 128, 128);
    r.addColorStop(0, 'rgba(0,0,0,.62)');
    r.addColorStop(0.32, 'rgba(0,0,0,.36)');
    r.addColorStop(0.62, 'rgba(0,0,0,.09)');
    r.addColorStop(1, 'rgba(0,0,0,0)');
    g.fillStyle = r;
    g.fillRect(0, 0, 256, 256);
  });

  await pause();

  /* the box */
  const rootG = new T.Group();
  const stack = new T.Group();
  rootG.add(stack);
  scene.add(rootG);

  const shadow = new T.Mesh(
    new T.PlaneGeometry(3.6, 3.6),
    new T.MeshBasicMaterial({ map: shadowTex, transparent: true, depthWrite: false, opacity: 0.55 }),
  );
  shadow.rotation.x = -Math.PI / 2;
  shadow.position.y = -0.03;
  shadow.renderOrder = -1;
  stack.add(shadow);

  const base = new T.Mesh(slab(W, W, RAD, BASE), alu);
  const foot = new T.Mesh(slab(W - 0.34, W - 0.34, RAD - 0.14, 0.022, 0.008), rubber);
  foot.position.y = -0.02;
  base.add(foot);
  stack.add(base);

  const lidPivot = new T.Group();
  const lid = new T.Mesh(slab(W, W, RAD, LID), [aluTop, alu]);
  lid.position.z = W / 2;
  lidPivot.add(lid);
  stack.add(lidPivot);

  const ledMat = new T.MeshBasicMaterial({ map: ledTex, toneMapped: false });
  const led = new T.Mesh(new T.CapsuleGeometry(0.011, 0.21, 4, 16), ledMat);
  led.rotation.z = Math.PI / 2;
  led.position.set(0, LID * 0.5, W / 2 - 0.004);
  lid.add(led);
  const glow = new T.Sprite(new T.SpriteMaterial({ map: glowTex, color: 0xff7f9a, transparent: true, depthWrite: false, opacity: 0.3 }));
  glow.scale.set(0.7, 0.22, 1);
  glow.position.set(0, LID * 0.5, W / 2 + 0.05);
  lid.add(glow);

  const defs: { name: string; color: string; darkColor?: string }[] = [
    { name: names[0] ?? 'Fichiers', color: '#2f6fdf' },
    { name: names[1] ?? 'Photos', color: '#e8862a' },
    { name: names[2] ?? 'Recherche', color: '#1d1b26', darkColor: '#c9c3ee' },
    { name: names[3] ?? 'Accès', color: '#13996b' },
    { name: names[4] ?? 'Sauvegarde', color: '#0e7490' },
  ];
  const slabG = slab(W, W, RAD, TH);
  const inner: Layer[] = defs.map((d, i) => {
    const group = new T.Group();
    const cap = new T.MeshStandardMaterial({ color: 0xf2f2ef, roughness: 0.82, metalness: 0 });
    const mesh = new T.Mesh(slabG, [cap, alu]);
    group.add(mesh);
    const seam = new T.MeshStandardMaterial({ color: d.color, emissive: d.color, emissiveIntensity: 0, roughness: 0.45 });
    const seamMesh = new T.Mesh(seamGeo, seam);
    seamMesh.position.y = TH - BEV - 0.013;
    group.add(seamMesh);
    const details = new T.Group();
    details.position.y = TH;
    group.add(details);
    stack.add(group);
    return {
      ...d,
      n: String(i + 1).padStart(2, '0'),
      group,
      cap,
      seam,
      seamMesh,
      details,
      tints: [],
      capBase: new T.Color(),
      seamBase: new T.Color(),
      baseY: 0,
      anim: null,
    };
  });
  const tinted = (layer: Layer, color: string, opts: T.MeshStandardMaterialParameters = {}) => {
    const m = new T.MeshStandardMaterial({ color, roughness: 0.55, metalness: 0, ...opts });
    layer.tints.push({ mat: m, base: new T.Color(color) });
    return m;
  };

  await pause();

  // 1 · Files: a card index, one card lifted.
  {
    const Ly = inner[0];
    const card = plate(0.62, 0.19, 0.03, 0.008);
    const blue = tinted(Ly, '#2f6fdf', { roughness: 0.5 });
    const cards: T.Mesh[] = [];
    for (let i = 0; i < 6; i++) {
      const m = new T.Mesh(card, i === 3 ? blue : paper);
      m.position.set(0, 0.004, -0.44 + i * 0.165);
      m.rotation.x = -0.2;
      Ly.details.add(m);
      cards.push(m);
    }
    const tray = new T.Mesh(slab(0.74, 1.08, 0.06, 0.03, 0.008, 8), tinted(Ly, '#c9d6f2', { roughness: 0.8 }));
    tray.position.y = -0.02;
    Ly.details.add(tray);
    Ly.anim = (t, h, still) => {
      cards[3].position.y = 0.004 + h * (0.06 + (still ? 0 : 0.012 * Math.sin(t * 1.3)));
    };
  }
  // 2 · Photos: a contact sheet, one print lifted.
  {
    const Ly = inner[1];
    const tile = slab(0.27, 0.27, 0.045, 0.022, 0.006, 6);
    const cols = ['#f3b37a', '#e8862a', '#ff5f8f', '#8fb8d8', '#ffd7a8', '#7a5cff', '#9fcfae', '#c9a3e8', '#ff9a62'];
    let lifted: T.Mesh | null = null;
    cols.forEach((c, i) => {
      const m = new T.Mesh(tile, tinted(Ly, c));
      m.position.set(((i % 3) - 1) * 0.33, 0, (Math.floor(i / 3) - 1) * 0.33);
      Ly.details.add(m);
      if (i === 4) lifted = m;
    });
    const print = lifted as unknown as T.Mesh;
    Ly.anim = (t, h, still) => {
      print.position.y = h * (0.07 + (still ? 0 : 0.012 * Math.sin(t * 1.1)));
      print.rotation.y = h * 0.18;
    };
  }
  // 3 · Search: the chip that runs the AI, its core lit with the wallpaper's colours.
  {
    const Ly = inner[2];
    const ink = tinted(Ly, '#24222e', { roughness: 0.4, metalness: 0.2 });
    const chip = new T.Mesh(slab(0.56, 0.56, 0.07, 0.05, 0.01, 8), ink);
    Ly.details.add(chip);
    const pinG = new T.BoxGeometry(0.035, 0.014, 0.06);
    for (let s = 0; s < 4; s++) {
      for (let i = 0; i < 5; i++) {
        const p = new T.Mesh(pinG, alu);
        const off = (i - 2) * 0.09;
        const a = (s * Math.PI) / 2;
        p.position.set(Math.cos(a) * 0.3 + -Math.sin(a) * off, 0.012, Math.sin(a) * 0.3 + Math.cos(a) * off);
        p.rotation.y = -a + Math.PI / 2;
        Ly.details.add(p);
      }
    }
    const coreMat = new T.MeshBasicMaterial({ map: coreTex, transparent: true, toneMapped: false });
    const core = new T.Mesh(new T.PlaneGeometry(0.28, 0.28), coreMat);
    core.rotation.x = -Math.PI / 2;
    core.position.y = 0.052;
    Ly.details.add(core);
    Ly.anim = (t, h, still) => {
      coreMat.color.setScalar(0.55 + 0.45 * h * (still ? 1 : 0.8 + 0.2 * Math.sin(t * 2)));
    };
  }
  // 4 · Access: rings of a signal around the box.
  {
    const Ly = inner[3];
    const green = tinted(Ly, '#13996b', { emissive: '#0b5c40', emissiveIntensity: 0.3 });
    const dot = new T.Mesh(new T.SphereGeometry(0.05, 24, 16), green);
    dot.position.y = 0.05;
    Ly.details.add(dot);
    const rings = [0.17, 0.3, 0.43].map((r) => {
      const m = new T.Mesh(new T.TorusGeometry(r, 0.012, 10, 72), green);
      m.rotation.x = Math.PI / 2;
      m.position.y = 0.02;
      Ly.details.add(m);
      return m;
    });
    Ly.anim = (t, h, still) => {
      rings.forEach((m, i) => {
        m.position.y = 0.02 + h * (still ? 0.012 * (2 - i) : 0.03 * Math.max(0, Math.sin(t * 1.6 - i * 0.8)));
      });
    };
  }
  // 5 · Backup: an encrypted USB disk, cabled to the side.
  {
    const Ly = inner[4];
    const teal = tinted(Ly, '#0e7490', { roughness: 0.45 });
    const disk = new T.Mesh(slab(0.46, 0.32, 0.08, 0.075, 0.014, 10), tinted(Ly, '#2c4c55', { roughness: 0.5, metalness: 0.3 }));
    disk.position.set(-0.08, 0, 0);
    Ly.details.add(disk);
    const lampMat = new T.MeshBasicMaterial({ color: 0x5eead4, toneMapped: false });
    const lamp = new T.Mesh(new T.SphereGeometry(0.014, 12, 8), lampMat);
    lamp.position.set(-0.24, 0.04, 0.162);
    Ly.details.add(lamp);
    const body = new T.Mesh(plate(0.13, 0.09, 0.022, 0.035), teal);
    body.position.set(-0.08, 0.075, 0.02);
    Ly.details.add(body);
    const shackle = new T.Mesh(new T.TorusGeometry(0.04, 0.011, 8, 24, Math.PI), teal);
    shackle.position.set(-0.08, 0.165, 0.02);
    Ly.details.add(shackle);
    const curve = new T.CatmullRomCurve3([
      new T.Vector3(0.15, 0.035, 0),
      new T.Vector3(0.42, 0.03, 0.06),
      new T.Vector3(0.62, 0.05, 0.2),
      new T.Vector3(0.79, 0.04, 0.26),
    ]);
    const cable = new T.Mesh(new T.TubeGeometry(curve, 32, 0.013, 8), tinted(Ly, '#3a3d42', { roughness: 0.7 }));
    Ly.details.add(cable);
    Ly.anim = (t, _h, still) => {
      lampMat.color.setScalar(still ? 1 : 0.55 + 0.45 * (0.5 + 0.5 * Math.sin(t * 2.4)));
    };
  }

  /* state */
  let dark = false;
  const size = { w: 1, h: 1 };
  const v = new T.Vector3(),
    right = new T.Vector3(),
    pa = new T.Vector3(),
    pb = new T.Vector3();
  const lerp = (a: number, b: number, t: number) => a + (b - a) * t;
  const calloutName = callout.querySelector('span')!;
  const calloutTag = callout.querySelector('b')!;

  function theme(d: boolean) {
    dark = d;
    scene.environment = envOf(d);
    scene.environmentIntensity = d ? 0.95 : 1;
    renderer.toneMappingExposure = 1;
    alu.color.set(d ? '#a4a7ad' : '#e3e5e8');
    aluTop.color.set(d ? '#a9acb2' : '#e8eaed');
    paper.color.set(d ? '#dedcd6' : '#f6f6f3');
    neutral.set(d ? '#3d3d3b' : '#cfcfcb');
    for (const L of inner) {
      L.capBase.set(d ? '#2b2b29' : '#f2f2ef').lerp(new T.Color(d && L.darkColor ? L.darkColor : L.color), d ? 0.1 : 0.06);
      L.seamBase.set(d && L.darkColor ? L.darkColor : L.color);
    }
    shadow.material.opacity = d ? 0.9 : 0.5;
    key.intensity = d ? 0.85 : 1.45;
    hemi.intensity = d ? 0.2 : 0.35;
    glow.material.color.set(d ? 0xff6f8f : 0xff8f9a);
  }

  function resize() {
    const w = canvas.clientWidth,
      h = canvas.clientHeight;
    if (!w || !h) return;
    const dpr = Math.min(devicePixelRatio || 1, w * h > 1.1e6 ? 1.5 : 2);
    renderer.setPixelRatio(dpr);
    renderer.setSize(w, h, false);
    size.w = w;
    size.h = h;
  }
  resize();

  const showCallout = (c: { x: number; y: number; left: boolean; color: string; o: number } | null) => {
    if (!c) {
      callout.style.opacity = '0';
      return;
    }
    callout.classList.toggle('left', c.left);
    callout.style.setProperty('--co', c.color);
    callout.style.opacity = c.o.toFixed(3);
    callout.style.transform = c.left
      ? `translate(calc(${c.x}px - 100%), ${c.y}px) translateY(-50%)`
      : `translate(${c.x}px, ${c.y}px) translateY(-50%)`;
  };

  let first = true;
  function frame(s: BoxState, t: number, still: boolean) {
    const o = s.open;
    const gIn = lerp(0.012, 0.3, o),
      gLid = lerp(0.012, 0.54, o);
    let y = BASE;
    for (let i = 4; i >= 0; i--) {
      y += gIn;
      inner[i].baseY = y;
      y += TH;
    }
    y += gLid;
    const lidY = y;
    y += LID;
    stack.position.y = -y / 2;
    lidPivot.position.set(0, lidY, -W / 2);
    lidPivot.rotation.x = -0.2 * o;

    const anyH = Math.min(
      1,
      s.h.reduce((a, b) => a + b, 0),
    );
    const ds = smooth(0.34, 0.88, o);
    inner.forEach((L, i) => {
      const h = s.h[i];
      L.group.position.set(0, L.baseY + 0.03 * h * o, 0.5 * h * o);
      const dim = anyH * (1 - h);
      L.cap.color.copy(L.capBase).lerp(neutral, dim * 0.3);
      // Closed, the box is plain aluminium: the coloured seams come with the opening.
      L.seamMesh.visible = o > 0.02;
      L.seam.color
        .copy(alu.color)
        .lerp(L.seamBase, smooth(0.02, 0.45, o))
        .lerp(neutral, dim * 0.7);
      L.seam.emissive.copy(L.seam.color);
      L.seam.emissiveIntensity = o * (0.1 + 0.55 * h);
      for (const m of L.tints) m.mat.color.copy(m.base).lerp(neutral, dim * 0.6);
      L.details.scale.setScalar(Math.max(ds, 1e-4));
      L.details.visible = ds > 0.002;
      if (L.anim) L.anim(t, h, still);
    });

    rootG.rotation.y = s.rot;
    rootG.position.y = still ? 0 : 0.012 * Math.sin(t * 0.8);
    const br = still ? 0.9 : 0.7 + 0.3 * (0.5 + 0.5 * Math.sin(t * 1.25));
    ledMat.color.setScalar(br * 1.1);
    glow.material.opacity = (dark ? 0.55 : 0.3) * br;

    // Frame the box in its part of the screen, then slide the view so it sits there.
    const wide = isWide();
    const aspect = size.w / size.h;
    const tanH = Math.tan((camera.fov * Math.PI) / 360);
    const regionW = wide ? 0.4 : 0.84,
      regionH = wide ? 0.78 : 0.88;
    const D = Math.max(s.R / (tanH * regionH), s.R / (tanH * aspect * regionW));
    camera.aspect = aspect;
    camera.position.set(0, D * Math.sin(s.elev), D * Math.cos(s.elev));
    camera.lookAt(0, 0, 0);
    camera.setViewOffset(size.w, size.h, (-s.nx * size.w) / 2, 0, size.w, size.h);
    camera.updateProjectionMatrix();
    renderer.render(scene, camera);

    // The exploded-view label: a leader line from the layer that stands out.
    let best = -1,
      bh = 0;
    s.h.forEach((h, i) => {
      if (h > bh) {
        bh = h;
        best = i;
      }
    });
    const co = Math.min(1, bh * smooth(0.6, 1, o));
    if (best >= 0 && co > 0.02) {
      const Ly = inner[best];
      Ly.group.getWorldPosition(v);
      v.y += TH * 0.5;
      right.setFromMatrixColumn(camera.matrixWorld, 0);
      pa.copy(v).addScaledVector(right, W * 0.62).project(camera);
      pb.copy(v).addScaledVector(right, -W * 0.62).project(camera);
      const xr = ((pa.x + 1) / 2) * size.w,
        yr = ((1 - pa.y) / 2) * size.h;
      const xl = ((pb.x + 1) / 2) * size.w,
        yl = ((1 - pb.y) / 2) * size.h;
      calloutName.textContent = wide ? Ly.n : `${Ly.n} ${Ly.name}`;
      calloutTag.textContent = Ly.name;
      const need = callout.offsetWidth + 10;
      const left = xr + need > size.w - 10 && xl - need > (wide ? size.w * 0.5 : 10);
      showCallout({ x: left ? xl : xr, y: left ? yl : yr, left, color: dark && Ly.darkColor ? Ly.darkColor : Ly.color, o: co });
    } else showCallout(null);

    if (first) {
      first = false;
      stage.classList.add('ready');
    }
  }

  // Compile every material's shaders off the main thread where the browser
  // can (KHR_parallel_shader_compile), so the first frame does not stall.
  theme(startDark);
  await pause();
  try {
    await renderer.compileAsync(scene, camera);
  } catch {}
  return { frame, resize, theme, dirty: true };
}
