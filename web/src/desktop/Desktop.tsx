import { Cloud, LogOut, Monitor, Search } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { Accounts } from '../Accounts.tsx'
import { api, type Account, type Health as HealthReport } from '../api.ts'
import { Drive } from '../drive/Drive.tsx'
import { TrashPage } from '../drive/TrashPage.tsx'
import { Health } from '../Health.tsx'
import { folderFromURL, navigate, usePath } from '../router.ts'
import { SharesPage } from '../share/SharesPage.tsx'
import { appInfo, apps, type AppId } from './apps.ts'
import { Window } from './Window.tsx'
import { useGeometries, useWindows, type Win } from './windows.ts'

/** The signed-in box: a living wallpaper, a menu bar, the app windows and the dock. */
export function Desktop({ me, onSignedOut }: { me: Account; onSignedOut: () => void }) {
  const path = usePath()
  const allowed = (id: AppId) => !appInfo(id).adminOnly || me.role === 'admin'
  const { windows, front, show, close } = useWindows(path, allowed)
  const places = useGeometries()

  return (
    <div className="desktop">
      <div className="wallpaper" aria-hidden />
      <MenuBar title={front ? appInfo(front).title : 'Bureau'} me={me} onSignedOut={onSignedOut} />

      <main className="desk" aria-label="Bureau" onPointerDown={(e) => e.target === e.currentTarget && navigate('/')}>
        <Clock />
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
        <DockButton label="Demander (bientôt)" color="#1d1b26" disabled>
          <Search size={24} aria-hidden />
        </DockButton>
      </nav>
    </div>
  )
}

/** What a window shows: the app at the address the window keeps. */
function AppContent({ win, me }: { win: Win; me: Account }) {
  switch (win.id) {
    case 'files':
      return <Drive folder={folderFromURL(win.path) ?? ''} />
    case 'shares':
      return <SharesPage />
    case 'trash':
      return <TrashPage />
    case 'accounts':
      return (
        <>
          <Health />
          <Accounts me={me} />
        </>
      )
  }
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

/** "Sauvegarde à jour" or how many tasks the box is working on, from /api/health. */
function useTaskStatus(): string | null {
  const [status, setStatus] = useState<string | null>(null)
  useEffect(() => {
    let current = true
    const read = () =>
      api<HealthReport>('GET', '/api/health')
        .then((h) => {
          if (!current || !h.tasks) return
          const busy = h.tasks.queued + h.tasks.running
          setStatus(busy === 0 ? 'Sauvegarde à jour' : `${busy} tâche${busy > 1 ? 's' : ''} en cours`)
        })
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

function MenuBar({ title, me, onSignedOut }: { title: string; me: Account; onSignedOut: () => void }) {
  const now = useNow(15_000)
  const status = useTaskStatus()
  return (
    <header className="menubar">
      <Cloud size={16} aria-label="Cloud OS" />
      <strong className="menubar-app">{title}</strong>
      <span className="menubar-fill" />
      {status && <span className="menubar-status">{status}</span>}
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
