import { useEffect, useState, type ReactNode } from 'react'
import { ApiError, api, type Account } from './api.ts'
import { CredentialsForm } from './CredentialsForm.tsx'
import { SmoothScroll } from './motion/SmoothScroll.tsx'
import { SharePage } from './share/SharePage.tsx'
import { tokenFromURL } from './share/shares.ts'
import { Shell } from './Shell.tsx'

type Screen =
  | { kind: 'loading' }
  | { kind: 'setup' }
  | { kind: 'login' }
  | { kind: 'home'; me: Account }
  | { kind: 'error'; message: string }

type SignedIn = { account: Account }

/** Picks the screen: a guest's share link, the first-start wizard, the login, or the home page. */
export function App() {
  // A share link opens without an account, before any session check.
  const token = tokenFromURL(window.location.pathname)
  return <SmoothScroll>{token ? <SharePage token={token} /> : <SignedInApp />}</SmoothScroll>
}

/** The box for its accounts: the wizard, the login, then the shell. */
function SignedInApp() {
  const [screen, setScreen] = useState<Screen>({ kind: 'loading' })

  useEffect(() => {
    let current = true
    const show = (next: Screen) => current && setScreen(next)
    api<{ needed: boolean }>('GET', '/api/setup')
      .then(({ needed }) =>
        needed ? show({ kind: 'setup' }) : api<Account>('GET', '/api/me').then((me) => show({ kind: 'home', me })),
      )
      .catch((err: unknown) =>
        show(
          err instanceof ApiError && err.status === 401
            ? { kind: 'login' }
            : { kind: 'error', message: String(err) },
        ),
      )
    return () => {
      current = false
    }
  }, [])

  switch (screen.kind) {
    case 'home':
      return <Shell me={screen.me} onSignedOut={() => setScreen({ kind: 'login' })} />
    case 'loading':
      return (
        <Narrow>
          <p className="muted">Chargement…</p>
        </Narrow>
      )
    case 'error':
      return (
        <Narrow>
          <p className="bad">Le cœur ne répond pas : {screen.message}</p>
        </Narrow>
      )
    case 'setup':
      return (
        <Narrow>
          <h2>Bienvenue</h2>
          <p className="muted">Créez le compte administrateur de la box.</p>
          <CredentialsForm
            withDisplayName
            submitLabel="Créer le compte administrateur"
            onSubmit={async (values) => {
              const { account } = await api<SignedIn>('POST', '/api/setup', values)
              setScreen({ kind: 'home', me: account })
            }}
          />
        </Narrow>
      )
    case 'login':
      return (
        <Narrow>
          <h2>Connexion</h2>
          <CredentialsForm
            submitLabel="Se connecter"
            onSubmit={async (values) => {
              const { account } = await api<SignedIn>('POST', '/api/login', values)
              setScreen({ kind: 'home', me: account })
            }}
          />
        </Narrow>
      )
  }
}

/** The narrow column of the screens before signing in. */
function Narrow({ children }: { children: ReactNode }) {
  return (
    <main className="narrow">
      <h1>Cloud OS</h1>
      <section>{children}</section>
    </main>
  )
}
