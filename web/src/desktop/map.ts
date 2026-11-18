import { api } from '../api.ts'
import type { TimelineEntry } from '../../../shared/api-types.ts'

/** Photos › Carte (#40). */
export const mapPath = '/photos/carte'

/** Whether the box has a map file (a .pmtiles in its maps folder). */
export interface MapInfo {
  tiles: boolean
  name?: string
}

/** A photo or video with where it was taken. */
export interface MapPoint extends TimelineEntry {
  lat: number
  lon: number
}

const zone = () => encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

export const fetchMapInfo = () => api<MapInfo>('GET', '/api/map')
export const fetchMapPoints = () => api<MapPoint[]>('GET', `/api/map/points?tz=${zone()}`)

/** The points as GeoJSON for MapLibre's clustering; each feature keeps its index in points. */
export function pointsGeoJSON(points: MapPoint[]) {
  return {
    type: 'FeatureCollection' as const,
    features: points.map((p, i) => ({
      type: 'Feature' as const,
      properties: { i },
      geometry: { type: 'Point' as const, coordinates: [p.lon, p.lat] },
    })),
  }
}

/** The style of the map: tiles, fonts and pictograms from the box alone. */
export function mapStyle(origin: string, dark: boolean, layers: unknown[]) {
  return {
    version: 8 as const,
    glyphs: `${origin}/map/fonts/{fontstack}/{range}.pbf`,
    sprite: `${origin}/map/sprites/${dark ? 'dark' : 'light'}`,
    sources: {
      protomaps: {
        type: 'vector' as const,
        url: `pmtiles://${origin}/api/map/tiles.pmtiles`,
        attribution: '<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noreferrer">© OpenStreetMap</a>',
      },
    },
    layers,
  }
}
