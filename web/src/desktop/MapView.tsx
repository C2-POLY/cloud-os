import { MapPinOff } from 'lucide-react'
import type { GeoJSONSource, StyleSpecification } from 'maplibre-gl'
import { useEffect, useRef, useState } from 'react'
import { messageOf } from '../api.ts'
import { thumbURL } from '../drive/files.ts'
import { fetchMapInfo, fetchMapPoints, mapStyle, pointsGeoJSON, type MapInfo, type MapPoint } from './map.ts'
import { Viewer } from './Viewer.tsx'
import 'maplibre-gl/dist/maplibre-gl.css'
// MapLibre's web worker, as a file Vite serves and builds: its default
// address, relative to the library, breaks once Vite bundles the library.
import workerURL from 'maplibre-gl/dist/maplibre-gl-worker.mjs?url'

const accent = () => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim() || '#2f6fdf'
const darkScheme = () => window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false

let protocolAdded = false

/**
 * Photos › Carte: the geolocated photos on a map drawn from the box alone (a
 * PMTiles file, fonts and pictograms it serves), grouped by area as the zoom
 * goes; a click on a group lists its photos, a photo opens in the viewer.
 */
export function MapView() {
  const [info, setInfo] = useState<MapInfo | null>(null)
  const [points, setPoints] = useState<MapPoint[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [picked, setPicked] = useState<MapPoint[] | null>(null)
  const [viewing, setViewing] = useState<number | null>(null)
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    fetchMapInfo()
      .then((i) => {
        setInfo(i)
        if (i.tiles) return fetchMapPoints().then(setPoints)
      })
      .catch((err: unknown) => setError(messageOf(err)))
  }, [])

  useEffect(() => {
    const el = box.current
    if (!info?.tiles || !points || !el) return
    let map: { remove: () => void } | null = null
    let gone = false
    void (async () => {
      const [maplibregl, { Protocol }, basemaps] = await Promise.all([import('maplibre-gl'), import('pmtiles'), import('@protomaps/basemaps')])
      if (gone) return
      if (!protocolAdded) {
        maplibregl.setWorkerUrl(workerURL)
        maplibregl.addProtocol('pmtiles', new Protocol().tile)
        protocolAdded = true
      }
      const dark = darkScheme()
      const style = mapStyle(window.location.origin, dark, basemaps.layers('protomaps', basemaps.namedFlavor(dark ? 'dark' : 'light'), { lang: 'fr' }))
      const m = new maplibregl.Map({ container: el, style: style as StyleSpecification, center: [2.35, 48.85], zoom: 2, attributionControl: { compact: true } })
      map = m
      m.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right')
      m.on('load', () => {
        m.addSource('photos', { type: 'geojson', data: pointsGeoJSON(points), cluster: true, clusterRadius: 48, clusterMaxZoom: 16 })
        const color = accent()
        m.addLayer({ id: 'groups', type: 'circle', source: 'photos', filter: ['has', 'point_count'], paint: { 'circle-color': color, 'circle-opacity': 0.85, 'circle-stroke-color': '#fff', 'circle-stroke-width': 2, 'circle-radius': ['step', ['get', 'point_count'], 16, 10, 20, 100, 26] } })
        m.addLayer({ id: 'group-count', type: 'symbol', source: 'photos', filter: ['has', 'point_count'], layout: { 'text-field': ['get', 'point_count_abbreviated'], 'text-font': ['Noto Sans Medium'], 'text-size': 13 }, paint: { 'text-color': '#fff' } })
        m.addLayer({ id: 'photo', type: 'circle', source: 'photos', filter: ['!', ['has', 'point_count']], paint: { 'circle-color': color, 'circle-radius': 8, 'circle-stroke-color': '#fff', 'circle-stroke-width': 2 } })
        m.on('click', 'groups', (e) => {
          const f = e.features?.[0]
          const id = f?.properties?.cluster_id as number | undefined
          if (id === undefined) return
          const src = m.getSource('photos') as GeoJSONSource
          void src.getClusterLeaves(id, 500, 0).then((leaves) => setPicked(leaves.map((l) => points[(l.properties as { i: number }).i])))
        })
        m.on('click', 'photo', (e) => {
          const i = e.features?.[0]?.properties?.i as number | undefined
          if (i !== undefined) setPicked([points[i]])
        })
        for (const layer of ['groups', 'photo']) {
          m.on('mouseenter', layer, () => (m.getCanvas().style.cursor = 'pointer'))
          m.on('mouseleave', layer, () => (m.getCanvas().style.cursor = ''))
        }
        if (points.length > 0) {
          const lons = points.map((p) => p.lon)
          const lats = points.map((p) => p.lat)
          m.fitBounds([[Math.min(...lons), Math.min(...lats)], [Math.max(...lons), Math.max(...lats)]], { padding: 60, maxZoom: 13, duration: 0 })
        }
      })
    })().catch((err: unknown) => setError(messageOf(err)))
    return () => {
      gone = true
      map?.remove()
    }
  }, [info, points])

  if (error) return <p className="bad">{error}</p>
  if (!info) return null
  if (!info.tiles)
    return (
      <div className="map-missing">
        <MapPinOff size={28} aria-hidden />
        <h2>Aucune carte installée</h2>
        <p className="muted">
          Pour voir tes photos sur une carte, dépose un fichier de tuiles <code>.pmtiles</code> (un extrait de ta région, tiré d'OpenStreetMap par Protomaps) dans le dossier des cartes de la box. La carte s'affiche alors ici, sans rien demander à
          internet.
        </p>
      </div>
    )
  return (
    <div className="map-view">
      {points && points.length === 0 && <p className="muted">Aucune photo n'a encore de position.</p>}
      <div className="map-frame">
        <div ref={box} className="map-canvas" aria-label="Carte des photos" role="region" />
        {picked && (
          <aside className="map-panel" aria-label="Photos de ce lieu">
            <header>
              <span>
                {picked.length} photo{picked.length > 1 ? 's' : ''}
              </span>
              <button type="button" className="link" onClick={() => setPicked(null)}>
                Fermer
              </button>
            </header>
            <ul className="timeline-media">
              {picked.map((p, i) => (
                <li key={p.path}>
                  <button type="button" onClick={() => setViewing(i)} aria-label={p.name}>
                    <img src={thumbURL(p) ?? undefined} alt="" loading="lazy" />
                  </button>
                </li>
              ))}
            </ul>
          </aside>
        )}
      </div>
      {picked && viewing !== null && picked[viewing] && <Viewer items={picked} index={viewing} onIndex={setViewing} onClose={() => setViewing(null)} onNearEnd={() => {}} />}
    </div>
  )
}
