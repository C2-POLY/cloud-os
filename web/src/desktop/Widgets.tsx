import { File, FileImage, FileText, FileVideo } from 'lucide-react'
import { useEffect, useState, type MouseEvent } from 'react'
import { thumbURL, type Entry } from '../drive/files.ts'
import { navigate } from '../router.ts'
import { dayLabel, describe, fetchActivity, fetchRecent, recentHref, type ActivityGroup } from './widgets.ts'

const refreshMs = 30_000

// Dated when loaded, so that "Aujourd'hui" and "Hier" follow the clock without
// reading it while rendering.
const loadActivity = () => fetchActivity().then((groups) => ({ groups, at: new Date() }))

/** Calls load now, every 30 s, and when the tab comes back into view. */
function useRefreshing<T>(load: () => Promise<T>): T | null {
  const [value, setValue] = useState<T | null>(null)
  useEffect(() => {
    let current = true
    const read = () => {
      load()
        .then((v) => current && setValue(v))
        .catch(() => {}) // a widget keeps its last value; the core's health says the rest
    }
    read()
    const timer = window.setInterval(read, refreshMs)
    const onVisible = () => document.visibilityState === 'visible' && read()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      current = false
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [load])
  return value
}

function go(href: string) {
  return (e: MouseEvent) => {
    e.preventDefault()
    navigate(href)
  }
}

/** The desktop's widgets: Reprendre and Ce qui s'est passé. */
export function Widgets() {
  const recent = useRefreshing(fetchRecent)
  const activity = useRefreshing(loadActivity)
  return (
    <div className="widgets">
      <section className="widget" aria-labelledby="w-recent">
        <h2 id="w-recent">Reprendre</h2>
        {recent && recent.length === 0 && <p className="widget-empty">Les fichiers que tu ajoutes ou modifies apparaîtront ici.</p>}
        <ul className="recent">
          {(recent ?? []).map((e) => (
            <li key={e.path}>
              <a href={recentHref(e)} onClick={go(recentHref(e))} title={e.path}>
                <RecentVisual entry={e} />
                <span className="recent-name">{e.name}</span>
              </a>
            </li>
          ))}
        </ul>
      </section>
      <section className="widget" aria-labelledby="w-activity">
        <h2 id="w-activity">Ce qui s'est passé</h2>
        {activity && activity.groups.length === 0 && <p className="widget-empty">Rien depuis une semaine.</p>}
        {activity && <ActivityList groups={activity.groups} now={activity.at} />}
      </section>
    </div>
  )
}

function ActivityList({ groups, now }: { groups: ActivityGroup[]; now: Date }) {
  return (
    <ol className="activity">
      {groups.map((g, i) => {
        const { text, href } = describe(g)
        const newDay = i === 0 || groups[i - 1].day !== g.day
        return (
          <li key={`${g.day} ${g.kind} ${g.place}`}>
            {newDay && <div className="activity-day">{dayLabel(g.day, now)}</div>}
            <a href={href} onClick={go(href)} className={`activity-item ${g.kind}`}>
              {text}
            </a>
          </li>
        )
      })}
    </ol>
  )
}

function RecentVisual({ entry }: { entry: Entry }) {
  const thumb = thumbURL(entry)
  const [broken, setBroken] = useState(false)
  if (thumb && !broken) return <img className="recent-visual" src={thumb} alt="" loading="lazy" onError={() => setBroken(true)} />
  const Icon = entry.mime.startsWith('image/') ? FileImage : entry.mime.startsWith('video/') ? FileVideo : entry.mime.startsWith('text/') || entry.mime === 'application/pdf' ? FileText : File
  return (
    <span className="recent-visual">
      <Icon size={26} aria-hidden />
    </span>
  )
}
