import { File, FileImage, FileText, FileVideo } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { messageOf } from '../api.ts'
import { parentOf, thumbURL } from '../drive/files.ts'
import { driveURL, navigate } from '../router.ts'
import { byMonth, fetchMonths, fillMonths, fetchTimeline, monthLabel, startOfMonthAfter, type MonthCount, type TimelineCursor, type TimelineEntry } from './timeline.ts'

const dayFormat = new Intl.DateTimeFormat('fr-FR', { day: 'numeric', month: 'short', year: 'numeric' })
const day = (at: string) => {
  const [y, m, d] = at.slice(0, 10).split('-').map(Number)
  return dayFormat.format(new Date(y, m - 1, d))
}

/**
 * Fichiers' Chronologie: every file at its date, photos at the date they were
 * taken, newest first, a page at a time; the strip of months jumps.
 */
export function Timeline() {
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
    fetchTimeline(cursor)
      .then((page) => {
        if (gen !== generation.current) return
        setItems((prev) => (reset ? page.items : [...prev, ...page.items]))
        setNext(page.next)
        setDone(page.next === null)
        setError(null)
      })
      .catch((err: unknown) => gen === generation.current && setError(messageOf(err)))
      .finally(() => gen === generation.current && setLoading(false))
  }, [])

  const load = useCallback(
    (cursor: TimelineCursor | null, reset: boolean) => {
      setLoading(true)
      request(cursor, reset)
    },
    [request],
  )

  useEffect(() => {
    fetchMonths()
      .then((m) => setMonths(fillMonths(m)))
      .catch(() => setMonths([]))
    request(null, true)
  }, [request])

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

  return (
    <div className="timeline">
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

      {error && <p className="bad">{error}</p>}
      {!error && !loading && items.length === 0 && <p className="muted">Aucun fichier pour l'instant.</p>}

      {groups.map((g) => (
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
    </div>
  )
}

function MediaVisual({ entry }: { entry: TimelineEntry }) {
  const thumb = thumbURL(entry)
  const [broken, setBroken] = useState(false)
  if (thumb && !broken) return <img src={thumb} alt="" loading="lazy" onError={() => setBroken(true)} />
  return <span className="timeline-media-icon">{entry.mime.startsWith('video/') ? <FileVideo size={22} aria-hidden /> : <FileImage size={22} aria-hidden />}</span>
}

function DocIcon({ mime }: { mime: string }) {
  const Icon = mime.startsWith('text/') || mime === 'application/pdf' ? FileText : File
  return <Icon size={18} aria-hidden />
}
