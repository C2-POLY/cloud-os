import { useEffect, useState } from 'react'
import { Accounts } from './Accounts.tsx'
import { ApiError, api, type Account } from './api.ts'
import { CredentialsForm } from './CredentialsForm.tsx'
import { Health } from './Health.tsx'

type Screen =
  | { kind: 'loading' }
  | { kind: 'setup' }
  | { kind: 'login' }
  | { kind: 'home'; me: Account }
  | { kind: 'error'; message: string }

type SignedIn = { account: Account }

/** Picks the screen: the first-start wizard, the login, or the home page. */
export function App() {
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
    case 'loading':
      return <p className="muted">Chargement…</p>
    case 'error':
      return <p className="bad">Le cœur ne répond pas : {screen.message}</p>
    case 'setup':
      return (
        <section>
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
        </section>
      )
    case 'login':
      return (
        <section>
          <h2>Connexion</h2>
          <CredentialsForm
            submitLabel="Se connecter"
            onSubmit={async (values) => {
              const { account } = await api<SignedIn>('POST', '/api/login', values)
              setScreen({ kind: 'home', me: account })
            }}
          />
        </section>
      )
    case 'home':
      return (
        <section>
          <p className="signed-in">
            Connecté en tant que <strong>{screen.me.display_name}</strong>
            {screen.me.role === 'admin' && <span className="badge">admin</span>}
            <button
              type="button"
              className="link"
              onClick={async () => {
                await api('POST', '/api/logout', {})
                setScreen({ kind: 'login' })
              }}
            >
              Se déconnecter
            </button>
          </p>
          <Health />
          {screen.me.role === 'admin' && <Accounts me={screen.me} />}
        </section>
      )
  }
}
