import { useState, type FormEvent } from 'react'
import { messageOf } from './api.ts'

export interface Credentials {
  name: string
  display_name?: string
  password: string
}

/** The name and password form shared by the wizard, the login and account creation. */
export function CredentialsForm({
  onSubmit,
  submitLabel,
  withDisplayName = false,
  newPassword = false,
}: {
  onSubmit: (values: Credentials) => Promise<void>
  submitLabel: string
  withDisplayName?: boolean
  newPassword?: boolean
}) {
  const [name, setName] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await onSubmit({ name, password, ...(withDisplayName ? { display_name: displayName } : {}) })
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  const creates = withDisplayName || newPassword
  return (
    <form className="form" onSubmit={submit}>
      <label>
        Identifiant
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          pattern="[a-z0-9][a-z0-9_\-]{0,63}"
          title="Minuscules, chiffres, - ou _"
          required
        />
      </label>
      {withDisplayName && (
        <label>
          Nom affiché
          <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} autoComplete="name" />
        </label>
      )}
      <label>
        Mot de passe
        <input
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete={creates ? 'new-password' : 'current-password'}
          minLength={creates ? 10 : undefined}
          required
        />
      </label>
      {creates && <p className="muted hint">10 caractères au moins.</p>}
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <button type="submit" disabled={busy}>
        {submitLabel}
      </button>
    </form>
  )
}
