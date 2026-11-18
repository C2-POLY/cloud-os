import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { appOf } from './apps.ts'
import { mapStyle, pointsGeoJSON, type MapPoint } from './map.ts'
import { MapView } from './MapView.tsx'

type Handler = (e: { features?: { properties: Record<string, unknown> }[] }) => void
const maps: { options: { style: unknown }; handlers: Map<string, Handler> }[] = []

vi.mock('maplibre-gl', () => {
  class FakeMap {
    handlers = new Map<string, Handler>()
    options: { style: unknown }
    constructor(options: { style: unknown }) {
      this.options = options
      maps.push(this)
      queueMicrotask(() => this.handlers.get('load')?.({}))
    }
    on(event: string, layerOrHandler: string | Handler, handler?: Handler) {
      if (typeof layerOrHandler === 'function') this.handlers.set(event, layerOrHandler)
      else if (handler) this.handlers.set(`${event}:${layerOrHandler}`, handler)
    }
    addControl() {}
    addSource() {}
    addLayer() {}
    fitBounds() {}
    remove() {}
    getCanvas() {
      return { style: {} }
    }
    getSource() {
      return { getClusterLeaves: async () => [{ properties: { i: 0 } }, { properties: { i: 1 } }] }
    }
  }
  return { Map: FakeMap, NavigationControl: class {}, addProtocol: vi.fn(), setWorkerUrl: vi.fn() }
})
vi.mock('pmtiles', () => ({ Protocol: class { tile = vi.fn() } }))
vi.mock('@protomaps/basemaps', () => ({ layers: () => [{ id: 'background', type: 'background' }], namedFlavor: (n: string) => ({ n }) }))
vi.mock('maplibre-gl/dist/maplibre-gl.css', () => ({}))
vi.mock('maplibre-gl/dist/maplibre-gl-worker.mjs?url', () => ({ default: '/worker.mjs' }))

const point = (path: string, lat: number, lon: number): MapPoint => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 2048,
  mime: 'image/jpeg',
  sha256: 'ab'.repeat(32),
  modified_at: '2026-11-17T10:00:00Z',
  at: '2024-07-14T18:03:22',
  taken: true,
  lat,
  lon,
})
const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

afterEach(() => {
  vi.unstubAllGlobals()
  maps.length = 0
})

describe('map helpers', () => {
  it('belongs to Photos and keeps each point’s index', () => {
    expect(appOf('/photos/carte')).toBe('photos')
    const g = pointsGeoJSON([point('a.jpg', 48.85, 2.29)])
    expect(g.features[0]).toEqual({ type: 'Feature', properties: { i: 0 }, geometry: { type: 'Point', coordinates: [2.29, 48.85] } })
  })

  it('draws from the box alone: tiles, fonts and pictograms', () => {
    const style = mapStyle('http://box:8480', true, [])
    expect(style.glyphs).toBe('http://box:8480/map/fonts/{fontstack}/{range}.pbf')
    expect(style.sprite).toBe('http://box:8480/map/sprites/dark')
    expect(style.sources.protomaps.url).toBe('pmtiles://http://box:8480/api/map/tiles.pmtiles')
    const { attribution: _shown, ...source } = style.sources.protomaps
    const urls = JSON.stringify({ ...style, sources: { protomaps: source } }).match(/https?:\/\/[^/"]+/g) ?? []
    expect(urls.every((u) => u === 'http://box:8480')).toBe(true)
  })
})

describe('MapView', () => {
  it('says how to install a map when the box has none', async () => {
    mockApi({ 'GET /api/map': { status: 200, body: { tiles: false } } })
    render(<MapView />)
    expect(await screen.findByRole('heading', { name: 'Aucune carte installée' })).toBeInTheDocument()
    expect(maps).toHaveLength(0)
  })

  it('shows the map on the local style and lists the photos of a clicked group', async () => {
    const points = [point('Photos/tour.jpg', 48.8584, 2.2945), point('Photos/pont.jpg', 48.8606, 2.3376)]
    mockApi({
      'GET /api/map': { status: 200, body: { tiles: true, name: 'paris.pmtiles' } },
      [`GET /api/map/points?tz=${tz}`]: { status: 200, body: points },
    })
    render(<MapView />)
    await waitFor(() => expect(maps).toHaveLength(1))
    const style = maps[0].options.style as ReturnType<typeof mapStyle>
    expect(style.sources.protomaps.url).toBe(`pmtiles://${window.location.origin}/api/map/tiles.pmtiles`)
    await waitFor(() => expect(maps[0].handlers.has('click:groups')).toBe(true))
    await act(async () => maps[0].handlers.get('click:groups')!({ features: [{ properties: { cluster_id: 7 } }] }))
    const panel = await screen.findByRole('complementary', { name: 'Photos de ce lieu' })
    expect(panel).toHaveTextContent('2 photos')
    fireEvent.click(screen.getByRole('button', { name: 'pont.jpg' }))
    expect(screen.getByRole('dialog', { name: 'pont.jpg' })).toBeInTheDocument()
  })
})
