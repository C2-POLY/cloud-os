import { Cloud, LogOut, Monitor, Search } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { failedLabel, summaryLabel, useAnalysisSummary } from '../analysis/analysis.ts'
import { Accounts } from '../Accounts.tsx'
import { api, type Account, type Health as HealthReport } from '../api.ts'
import { Drive } from '../drive/Drive.tsx'
import { TrashPage } from '../drive/TrashPage.tsx'
import { Health } from '../Health.tsx'
import { driveURL, folderFromURL, navigate, usePath } from '../router.ts'
import { SharesPage } from '../share/SharesPage.tsx'
import { Settings } from '../settings/Settings.tsx'
import { DocumentsPage } from '../documents/Documents.tsx'
import { categoryFromURL } from '../documents/documents.ts'
import { appInfo, apps, timelinePath, type AppId } from './apps.ts'
import { Timeline } from './Timeline.tsx'
import { Palette } from './Palette.tsx'
import { Widgets } from './Widgets.tsx'
import { Window } from './Window.tsx'
import { useGeometries, useWindows, type Win } from './windows.ts'
import { AlbumPage, AlbumsPage } from './Albums.tsx'
import { albumFromURL, albumsPath, photosPath } from './albums.ts'
import { mapPath } from './map.ts'
import { MapView } from './MapView.tsx'

/** The signed-in box: a living wallpaper, a menu bar, the app windows and the dock. */
export function Desktop({ me, onSignedOut }: { me: Account; onSignedOut: () => void }) {
  const path = usePath()
  const allowed = (id: AppId) => !appInfo(id).adminOnly || me.role === 'admin'
  const { windows, front, show, close } = useWindows(path, allowed)
  const places = useGeometries()
  const [asking, setAsking] = useState(false)

  // ⌘K (Ctrl+K elsewhere) opens or closes Demander from anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setAsking((a) => !a)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="desktop">
      <div className="wallpaper" aria-hidden />
      <MenuBar title={front ? appInfo(front).title : 'Bureau'} me={me} onSignedOut={onSignedOut} onAsk={() => setAsking(true)} />

      <main className="desk" aria-label="Bureau" onPointerDown={(e) => e.target === e.currentTarget && navigate('/')}>
        <Clock />
        <Widgets />
      </main>

      {windows.map((w, i) => (
        <Window
          key={w.id}
          app={appInfo(w.id)}
          front={w.id === front}
          z={10 + i}
          geometry={places.get(w.id, apps.findIndex((a) => a.id === w.id))}
          onGeometry={(g) => places.set(w.id, g)}
          onFocus={() => show(w.id)}
          onClose={() => close(w.id)}
        >
          <AppContent win={w} me={me} />
        </Window>
      ))}

      <nav className="dock" aria-label="Applications">
        <DockButton label="Bureau" color="rgba(255,255,255,.9)" dark active={front === null} onClick={() => navigate('/')}>
          <Monitor size={24} aria-hidden />
        </DockButton>
        {apps.filter((a) => allowed(a.id)).map((a) => (
          <DockButton
            key={a.id}
            label={a.title}
            color={a.color}
            active={front === a.id}
            open={windows.some((w) => w.id === a.id)}
            onClick={() => show(a.id)}
          >
            <a.icon size={24} aria-hidden />
          </DockButton>
        ))}
        <span className="dock-sep" aria-hidden />
        <DockButton label="Demander" color="#1d1b26" active={asking} onClick={() => setAsking(true)}>
          <Search size={24} aria-hidden />
        </DockButton>
      </nav>

      {asking && <Palette onClose={() => setAsking(false)} />}
    </div>
  )
}

/** What a window shows: the app at the address the window keeps. */
function AppContent({ win, me }: { win: Win; me: Account }) {
  switch (win.id) {
    case 'files':
      return (
        <>
          <FilesViews path={win.path} />
          {win.path === timelinePath ? <Timeline /> : <Drive folder={folderFromURL(win.path) ?? ''} />}
        </>
      )
    case 'photos': {
      const album = albumFromURL(win.path)
      return (
        <>
          <PhotosViews path={win.path} />
          {album !== null ? <AlbumPage id={album} /> : win.path === albumsPath ? <AlbumsPage /> : win.path === mapPath ? <MapView /> : <Timeline photos />}
        </>
      )
    }
    case 'documents':
      return <DocumentsPage category={categoryFromURL(win.path) ?? undefined} />
    case 'shares':
      return <SharesPage />
    case 'trash':
      return <TrashPage />
    case 'settings':
      return <Settings me={me} />
    case 'accounts':
      return (
        <>
          <Health />
          <Accounts me={me} />
        </>
      )
  }
}

/** Photos' three views: every photo by day, the albums, the map. */
function PhotosViews({ path }: { path: string }) {
  const onMap = path === mapPath
  const onAlbums = path !== photosPath && !onMap
  const link = (to: string, label: string, active: boolean) => (
    <a
      href={to}
      className={active ? 'seg on' : 'seg'}
      aria-current={active ? 'page' : undefined}
      onClick={(e) => {
        e.preventDefault()
        navigate(to)
      }}
    >
      {label}
    </a>
  )
  return (
    <nav className="files-views" aria-label="Vue de Photos">
      {link(photosPath, 'Photos', !onAlbums && !onMap)}
      {link(albumsPath, 'Albums', onAlbums)}
      {link(mapPath, 'Carte', onMap)}
    </nav>
  )
}

/** Fichiers' two views: its folders, or everything on one timeline. */
function FilesViews({ path }: { path: string }) {
  const onTimeline = path === timelinePath
  const link = (to: string, label: string, active: boolean) => (
    <a
      href={to}
      className={active ? 'seg on' : 'seg'}
      aria-current={active ? 'page' : undefined}
      onClick={(e) => {
        e.preventDefault()
        navigate(to)
      }}
    >
      {label}
    </a>
  )
  return (
    <nav className="files-views" aria-label="Vue de Fichiers">
      {link(driveURL(''), 'Dossiers', !onTimeline)}
      {link(timelinePath, 'Chronologie', onTimeline)}
    </nav>
  )
}

function DockButton({
  label,
  color,
  dark,
  active,
  open,
  disabled,
  onClick,
  children,
}: {
  label: string
  color: string
  dark?: boolean
  active?: boolean
  open?: boolean
  disabled?: boolean
  onClick?: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      className={['dock-button', dark && 'dark', open && 'open'].filter(Boolean).join(' ')}
      style={{ background: color }}
      aria-label={label}
      title={label}
      aria-current={active ? 'page' : undefined}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  )
}

function useNow(everyMs: number): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const t = window.setInterval(() => setNow(new Date()), everyMs)
    return () => window.clearInterval(t)
  }, [everyMs])
  return now
}

const time = new Intl.DateTimeFormat('fr-FR', { hour: '2-digit', minute: '2-digit' })
const day = new Intl.DateTimeFormat('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' })
const shortDay = new Intl.DateTimeFormat('fr-FR', { weekday: 'short', day: 'numeric', month: 'short' })

function Clock() {
  const now = useNow(15_000)
  const date = day.format(now)
  return (
    <div className="clock">
      <time className="clock-time" dateTime={now.toISOString()}>
        {time.format(now)}
      </time>
      <div className="clock-date">{date.charAt(0).toUpperCase() + date.slice(1)}</div>
    </div>
  )
}

/** "Sauvegarde à jour" while the core answers its health check. */
function useBackupStatus(): string | null {
  const [status, setStatus] = useState<string | null>(null)
  useEffect(() => {
    let current = true
    const read = () =>
      api<HealthReport>('GET', '/api/health')
        .then((h) => current && setStatus(h.db === 'ok' ? 'Sauvegarde à jour' : null))
        .catch(() => current && setStatus(null))
    void read()
    const t = window.setInterval(read, 20_000)
    return () => {
      current = false
      window.clearInterval(t)
    }
  }, [])
  return status
}

/** Where the analysis of the account's files stands, from /api/analysis; it updates by itself. */
function AnalysisStatus() {
  const summary = useAnalysisSummary()
  if (!summary) return null
  const failed = failedLabel(summary)
  return (
    <span className="menubar-analysis" role="status">
      <span className={summary.analysing > 0 ? 'menubar-busy' : undefined}>{summaryLabel(summary)}</span>
      {failed && (
        <span className="menubar-failed" title="Ouvrez le fichier pour voir pourquoi.">
          {failed}
        </span>
      )}
    </span>
  )
}

function MenuBar({ title, me, onSignedOut, onAsk }: { title: string; me: Account; onSignedOut: () => void; onAsk: () => void }) {
  const now = useNow(15_000)
  const status = useBackupStatus()
  return (
    <header className="menubar">
      <Cloud size={16} aria-label="Cloud OS" />
      <strong className="menubar-app">{title}</strong>
      <span className="menubar-fill" />
      <button type="button" className="menubar-ask" onClick={onAsk}>
        <Search size={13} aria-hidden /> Demander <kbd>⌘K</kbd>
      </button>
      <span className="menubar-fill" />
      {status && <span className="menubar-status">{status}</span>}
      <AnalysisStatus />
      <span className="menubar-clock">
        {shortDay.format(now)} {time.format(now)}
      </span>
      <span className="menubar-who">{me.display_name}</span>
      <button
        type="button"
        className="menubar-button"
        aria-label="Se déconnecter"
        title="Se déconnecter"
        onClick={async () => {
          await api('POST', '/api/logout', {})
          onSignedOut()
        }}
      >
        <LogOut size={15} aria-hidden />
      </button>
    </header>
  )
}
