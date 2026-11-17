import { useEffect, useState } from 'react'
import { api, messageOf, type Account } from './api.ts'
import { CredentialsForm } from './CredentialsForm.tsx'

/** The admin's list of accounts: create, change the role, disable or enable. */
export function Accounts({ me }: { me: Account }) {
  const [accounts, setAccounts] = useState<Account[]>([])
  const [error, setError] = useState<string | null>(null)
  const [formKey, setFormKey] = useState(0)

  const [version, setVersion] = useState(0)
  const reload = () => setVersion((v) => v + 1)

  useEffect(() => {
    let current = true
    api<Account[]>('GET', '/api/accounts')
      .then((list) => current && setAccounts(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [version])

  async function update(name: string, change: { role?: Account['role']; disabled?: boolean }) {
    setError(null)
    try {
      await api('PATCH', `/api/accounts/${encodeURIComponent(name)}`, change)
      reload()
    } catch (err) {
      setError(messageOf(err))
    }
  }

  return (
    <section className="accounts">
      <h2>Comptes</h2>
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <ul>
        {accounts.map((a) => (
          <li key={a.name} className={a.disabled_at ? 'muted' : undefined}>
            <span>
              <strong>{a.display_name}</strong> <span className="muted">{a.name}</span>
              {a.role === 'admin' && <span className="badge">admin</span>}
              {a.disabled_at && <span className="badge">désactivé</span>}
            </span>
            {a.name !== me.name && (
              <span className="actions">
                <button
                  type="button"
                  className="link"
                  onClick={() => update(a.name, { role: a.role === 'admin' ? 'member' : 'admin' })}
                >
                  {a.role === 'admin' ? 'Retirer admin' : 'Rendre admin'}
                </button>
                <button type="button" className="link" onClick={() => update(a.name, { disabled: !a.disabled_at })}>
                  {a.disabled_at ? 'Réactiver' : 'Désactiver'}
                </button>
              </span>
            )}
          </li>
        ))}
      </ul>
      <h3>Nouveau compte</h3>
      <CredentialsForm
        key={formKey}
        withDisplayName
        newPassword
        submitLabel="Créer le compte"
        onSubmit={async (values) => {
          await api('POST', '/api/accounts', { ...values, role: 'member' })
          setFormKey((k) => k + 1)
          reload()
        }}
      />
    </section>
  )
}
