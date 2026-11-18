import { Check, File, FileImage, FileText, FileVideo } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { messageOf } from '../api.ts'
import { parentOf, thumbURL } from '../drive/files.ts'
import { driveURL, navigate } from '../router.ts'
import { byDay, byMonth, dayTitle, fetchMonths, fillMonths, fetchTimeline, formatDuration, monthLabel, startOfMonthAfter, type MonthCount, type TimelineCursor, type TimelineEntry } from './timeline.ts'
import { Viewer } from './Viewer.tsx'
import { addToAlbum, fetchAlbumItems, removeFromAlbum } from './albums.ts'
import { AlbumPicker } from './Albums.tsx'

const dayFormat = new Intl.DateTimeFormat('fr-FR', { day: 'numeric', month: 'short', year: 'numeric' })
const day = (at: string) => {
  const [y, m, d] = at.slice(0, 10).split('-').map(Number)
  return dayFormat.format(new Date(y, m - 1, d))
}

/**
 * A timeline, newest first, a page at a time; the strip of months jumps.
 * Fichiers' Chronologie (every file; a click opens its folder) or, with
 * `photos`, the Photos app (images and videos by day; a click opens them big),
 * or with `album`, one album of it. In Photos, photos can be selected to add
 * them to an album, or to take them out of the album shown.
 */
export function Timeline({ photos: photosApp = false, album, onChange }: { photos?: boolean; album?: number; onChange?: () => void }) {
  const photos = photosApp || album !== undefined
  const filter = photos ? 'media' : undefined
  const [selecting, setSelecting] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [picking, setPicking] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [viewing, setViewing] = useState<number | null>(null)
  const [months, setMonths] = useState<MonthCount[] | null>(null)
  const [items, setItems] = useState<TimelineEntry[]>([])
  const [next, setNext] = useState<TimelineCursor | null>(null)
  const [done, setDone] = useState(false)
  const [loading, setLoading] = useState(true) // the first page is on its way
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  const sentinel = useRef<HTMLDivElement>(null)

  // Asks for a page; reset starts the list over at the cursor (a jump) or at
  // the top. An answer to an older jump arriving late is dropped.
  const request = useCallback((cursor: TimelineCursor | null, reset: boolean) => {
    const gen = reset ? ++generation.current : generation.current
    ;(album !== undefined ? fetchAlbumItems(album, cursor, 60) : fetchTimeline(cursor, 60, filter))
      .then((page) => {
        if (gen !== generation.current) return
        setItems((prev) => (reset ? page.items : [...prev, ...page.items]))
        setNext(page.next)
        setDone(page.next === null)
        setError(null)
      })
      .catch((err: unknown) => gen === generation.current && setError(messageOf(err)))
      .finally(() => gen === generation.current && setLoading(false))
  }, [filter, album])

  const load = useCallback(
    (cursor: TimelineCursor | null, reset: boolean) => {
      setLoading(true)
      request(cursor, reset)
    },
    [request],
  )

  useEffect(() => {
    if (album === undefined)
      fetchMonths(filter)
        .then((m) => setMonths(fillMonths(m)))
        .catch(() => setMonths([]))
    request(null, true)
  }, [request, filter, album])

  // The next page loads when the end of the list comes into view.
  useEffect(() => {
    const el = sentinel.current
    if (!el || done || loading || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => entries.some((e) => e.isIntersecting) && next && load(next, false))
    io.observe(el)
    return () => io.disconnect()
  }, [next, done, loading, load])

  const groups = byMonth(items)
  const max = Math.max(1, ...(months ?? []).map((m) => m.count))
  const open = (e: TimelineEntry) => navigate(driveURL(parentOf(e.path)))
  // Stable, so that the viewer does not read its metadata again on each render.
  const loadMore = useCallback(() => {
    if (next && !loading) load(next, false)
  }, [next, loading, load])
  const show = (e: TimelineEntry) => setViewing(items.indexOf(e))
  const toggle = (e: TimelineEntry) =>
    setSelected((prev) => {
      const s = new Set(prev)
      if (s.has(e.path)) s.delete(e.path)
      else s.add(e.path)
      return s
    })
  const stopSelecting = () => {
    setSelecting(false)
    setSelected(new Set())
  }
  const addSelected = async (id: number, name: string) => {
    await addToAlbum(id, [...selected])
    setPicking(false)
    setNotice(`${selected.size} ajoutée${selected.size > 1 ? 's' : ''} à « ${name} ».`)
    stopSelecting()
  }
  const removeSelected = async () => {
    if (album === undefined) return
    try {
      await removeFromAlbum(album, [...selected])
      const n = selected.size
      setNotice(n > 1 ? `${n} retirées de l'album. Elles restent dans Photos.` : "1 retirée de l'album. Elle reste dans Photos.")
      stopSelecting()
      load(null, true)
      onChange?.()
    } catch (err) {
      setError(messageOf(err))
    }
  }

  return (
    <div className={photos ? 'timeline photos' : 'timeline'}>
      {months && months.length > 0 && (
        <nav className="strip" aria-label="Aller à un mois">
          {months.map((m) => (
            <button
              key={m.month}
              type="button"
              className={m.count ? 'strip-bar' : 'strip-bar empty'}
              disabled={m.count === 0}
              style={{ height: m.count ? `${Math.max(10, (m.count / max) * 100)}%` : '3px' }}
              title={`${monthLabel(m.month)} : ${m.count} fichier${m.count > 1 ? 's' : ''}`}
              aria-label={`${monthLabel(m.month)}, ${m.count} fichier${m.count > 1 ? 's' : ''}`}
              onClick={() => load(startOfMonthAfter(m.month), true)}
            />
          ))}
        </nav>
      )}
      {months && months.length > 0 && (
        <div className="strip-years" aria-hidden>
          <span>{months[0].month.slice(0, 4)}</span>
          <span>{months.at(-1)!.month.slice(0, 4)}</span>
        </div>
      )}

      {photos && items.length > 0 && (
        <div className="toolbar photos-select" role="toolbar" aria-label="Sélection">
          {!selecting ? (
            <button type="button" className="secondary" onClick={() => setSelecting(true)}>
              Sélectionner
            </button>
          ) : (
            <>
              <span className="muted">
                {selected.size} sélectionnée{selected.size > 1 ? 's' : ''}
              </span>
              {album === undefined ? (
                <button type="button" disabled={selected.size === 0} onClick={() => setPicking(true)}>
                  Ajouter à un album…
                </button>
              ) : (
                <button type="button" disabled={selected.size === 0} onClick={() => void removeSelected()}>
                  Retirer de l'album
                </button>
              )}
              <button type="button" className="secondary" onClick={stopSelecting}>
                Annuler
              </button>
            </>
          )}
        </div>
      )}
      {notice && (
        <p className="muted" role="status">
          {notice}
        </p>
      )}
      {error && <p className="bad">{error}</p>}
      {!error && !loading && items.length === 0 && (
        <p className="muted">{album !== undefined ? 'Cet album est vide. Ajoute des photos depuis Photos, avec « Sélectionner ».' : photos ? "Aucune photo pour l'instant." : "Aucun fichier pour l'instant."}</p>
      )}

      {photos &&
        groups.map((g) => (
          <section key={g.month} className="timeline-month" aria-labelledby={`m-${g.month}`}>
            <h2 id={`m-${g.month}`}>{monthLabel(g.month)}</h2>
            {byDay(g.media).map((d) => (
              <div key={d.day} className="photos-day">
                <h3>{dayTitle(d.day)}</h3>
                <ul className="timeline-media">
                  {d.items.map((e) => (
                    <li key={e.path}>
                      <button
                        type="button"
                        className={selected.has(e.path) ? 'selected' : undefined}
                        aria-pressed={selecting ? selected.has(e.path) : undefined}
                        onClick={() => (selecting ? toggle(e) : show(e))}
                        aria-label={`${e.name}, ${day(e.at)}`}
                      >
                        <MediaVisual entry={e} />
                        {selecting && <span className="timeline-media-check">{selected.has(e.path) && <Check size={14} aria-hidden />}</span>}
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </section>
        ))}

      {!photos && groups.map((g) => (
        <section key={g.month} className="timeline-month" aria-labelledby={`m-${g.month}`}>
          <h2 id={`m-${g.month}`}>{monthLabel(g.month)}</h2>
          {g.media.length > 0 && (
            <ul className="timeline-media">
              {g.media.map((e) => (
                <li key={e.path}>
                  <button type="button" onClick={() => open(e)} title={`${e.name} · ${day(e.at)}`} aria-label={`${e.name}, ${day(e.at)}`}>
                    <MediaVisual entry={e} />
                  </button>
                </li>
              ))}
            </ul>
          )}
          {g.docs.length > 0 && (
            <ul className="timeline-docs">
              {g.docs.map((e) => (
                <li key={e.path}>
                  <button type="button" onClick={() => open(e)}>
                    <DocIcon mime={e.mime} />
                    <span className="timeline-doc-name">{e.name}</span>
                    <span className="muted">{day(e.at)}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      ))}

      <div ref={sentinel} className="timeline-end">
        {loading && <span className="muted">Chargement…</span>}
        {!loading && next && (
          <button type="button" className="secondary" onClick={() => load(next, false)}>
            Plus ancien
          </button>
        )}
      </div>

      {picking && <AlbumPicker count={selected.size} onPick={addSelected} onClose={() => setPicking(false)} />}

      {viewing !== null && viewing >= 0 && items[viewing] && (
        <Viewer
          items={items}
          index={viewing}
          onIndex={setViewing}
          onClose={() => setViewing(null)}
          onNearEnd={loadMore}
        />
      )}
    </div>
  )
}

function MediaVisual({ entry }: { entry: TimelineEntry }) {
  const thumb = thumbURL(entry)
  const [broken, setBroken] = useState(false)
  const video = entry.mime.startsWith('video/')
  return (
    <>
      {thumb && !broken ? (
        <img src={thumb} alt="" loading="lazy" onError={() => setBroken(true)} />
      ) : (
        <span className="timeline-media-icon">{video ? <FileVideo size={22} aria-hidden /> : <FileImage size={22} aria-hidden />}</span>
      )}
      {video && entry.duration_ms != null && <span className="timeline-media-duration">{formatDuration(entry.duration_ms)}</span>}
    </>
  )
}

function DocIcon({ mime }: { mime: string }) {
  const Icon = mime.startsWith('text/') || mime === 'application/pdf' ? FileText : File
  return <Icon size={18} aria-hidden />
}
