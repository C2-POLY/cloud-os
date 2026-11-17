import { Cloud, HardDrive, LogOut, Share2, Trash2, Users } from 'lucide-react'
import { useEffect } from 'react'
import { Accounts } from './Accounts.tsx'
import { api, type Account } from './api.ts'
import { Drive } from './drive/Drive.tsx'
import { TrashPage } from './drive/TrashPage.tsx'
import { Health } from './Health.tsx'
import { SharesPage } from './share/SharesPage.tsx'
import { driveURL, folderFromURL, navigate, usePath } from './router.ts'

/** The signed-in app: a top bar and the page the URL names. */
export function Shell({ me, onSignedOut }: { me: Account; onSignedOut: () => void }) {
  const path = usePath()
  const folder = folderFromURL(path)
  const onAccounts = path === '/comptes' && me.role === 'admin'
  const onTrash = path === '/corbeille'
  const onShares = path === '/partages'

  // Any other address opens the drive's root.
  useEffect(() => {
    if (folder === null && !onAccounts && !onTrash && !onShares) navigate(driveURL(''), true)
  }, [folder, onAccounts, onTrash, onShares])

  function link(to: string, label: string, Icon: typeof HardDrive, active: boolean) {
    return (
      <a
        href={to}
        className={active ? 'tab active' : 'tab'}
        aria-current={active ? 'page' : undefined}
        aria-label={label}
        title={label}
        onClick={(e) => {
          e.preventDefault()
          navigate(to)
        }}
      >
        <Icon size={16} aria-hidden /> <span className="tab-label">{label}</span>
      </a>
    )
  }

  return (
    <div className="shell">
      <header className="topbar">
        <span className="brand">
          <Cloud size={20} aria-hidden /> <span className="brand-name">Cloud OS</span>
        </span>
        <nav className="tabs">
          {link(driveURL(''), 'Drive', HardDrive, folder !== null)}
          {link('/partages', 'Partages', Share2, onShares)}
          {link('/corbeille', 'Corbeille', Trash2, onTrash)}
          {me.role === 'admin' && link('/comptes', 'Comptes', Users, onAccounts)}
        </nav>
        <span className="who">
          <span className="who-name">{me.display_name}</span>
          <button
            type="button"
            className="icon-button"
            aria-label="Se déconnecter"
            title="Se déconnecter"
            onClick={async () => {
              await api('POST', '/api/logout', {})
              onSignedOut()
            }}
          >
            <LogOut size={16} aria-hidden />
          </button>
        </span>
      </header>
      <main className="page">
        {folder !== null && <Drive folder={folder} />}
        {onTrash && <TrashPage />}
        {onShares && <SharesPage />}
        {onAccounts && (
          <>
            <h1>Comptes</h1>
            <Health />
            <Accounts me={me} />
          </>
        )}
      </main>
    </div>
  )
}
