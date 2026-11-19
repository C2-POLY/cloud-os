import { Check, Copy, KeyRound } from 'lucide-react'
import { useEffect, useState } from 'react'
import type { SMBPasswordCreated, SMBPasswordStatus } from '../../../shared/api-types.ts'
import { api, messageOf, type Account } from '../api.ts'
import { ConfirmDialog } from '../drive/Dialogs.tsx'
import { formatDate } from '../drive/files.ts'

/** The settings of the signed-in account, for every member. */
export function Settings({ me }: { me: Account }) {
  return (
    <div className="settings">
      <SMBAccess me={me} />
    </div>
  )
}

const smbPath = '/api/me/smb-password'

/**
 * « Accès SMB »: the account's own SMB password (#91). The box draws it and
 * keeps only its NT hash, so it is shown once, right after it is drawn.
 */
export function SMBAccess({ me }: { me: Account }) {
  const [status, setStatus] = useState<SMBPasswordStatus | null>(null)
  const [password, setPassword] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<'regenerate' | 'revoke' | null>(null)

  useEffect(() => {
    let current = true
    api<SMBPasswordStatus>('GET', smbPath)
      .then((st) => current && setStatus(st))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [])

  async function generate() {
    setError(null)
    const created = await api<SMBPasswordCreated>('POST', smbPath, {})
    const { password: pw, ...st } = created
    setStatus(st)
    setPassword(pw)
    setCopied(false)
  }

  async function revoke() {
    setError(null)
    await api('DELETE', smbPath, {})
    setStatus({ exists: false, created_at: null, last_used_at: null })
    setPassword(null)
  }

  return (
    <section className="settings-section" aria-labelledby="smb-access">
      <h2 id="smb-access">
        <KeyRound size={18} aria-hidden /> Accès SMB
      </h2>
      <p className="muted">
        Un mot de passe réservé au partage réseau SMB, pour monter votre drive comme un disque. Il n’ouvre ni l’interface web ni WebDAV, et
        le mot de passe de votre compte n’ouvre pas SMB.
      </p>

      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}

      {password && (
        <div className="smb-secret" role="status">
          <label>
            Mot de passe SMB
            <span className="smb-secret-row">
              <input readOnly value={password} onFocus={(e) => e.currentTarget.select()} spellCheck={false} />
              <button
                type="button"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(password)
                    setCopied(true)
                  } catch {
                    setError('Copie impossible : sélectionnez le mot de passe et copiez-le.')
                  }
                }}
              >
                {copied ? <Check size={16} aria-hidden /> : <Copy size={16} aria-hidden />} {copied ? 'Copié' : 'Copier'}
              </button>
            </span>
          </label>
          <p className="hint">
            Copiez-le maintenant : il ne sera plus affiché. La box n’en garde qu’une empreinte et ne peut pas le retrouver.
          </p>
          <button type="button" className="link" onClick={() => setPassword(null)}>
            C’est noté, masquer
          </button>
        </div>
      )}

      {status && (
        <dl className="smb-facts">
          <dt>Identifiant</dt>
          <dd>{me.name}</dd>
          <dt>Mot de passe</dt>
          <dd>{status.exists ? 'Défini' : 'Aucun'}</dd>
          {status.exists && status.created_at && (
            <>
              <dt>Créé le</dt>
              <dd>{formatDate(status.created_at)}</dd>
              <dt>Dernière utilisation</dt>
              <dd>{status.last_used_at ? formatDate(status.last_used_at) : 'Jamais'}</dd>
            </>
          )}
        </dl>
      )}

      {status && (
        <div className="settings-actions">
          {status.exists ? (
            <>
              <button type="button" onClick={() => setConfirm('regenerate')}>
                Régénérer
              </button>
              <button type="button" className="secondary" onClick={() => setConfirm('revoke')}>
                Révoquer
              </button>
            </>
          ) : (
            <button
              type="button"
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError(null)
                try {
                  await generate()
                } catch (err) {
                  setError(messageOf(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              Générer un mot de passe
            </button>
          )}
        </div>
      )}

      {confirm === 'regenerate' && (
        <ConfirmDialog
          title="Régénérer le mot de passe SMB ?"
          message="L’ancien mot de passe ne fonctionnera plus, et les connexions SMB ouvertes avec lui seront coupées."
          confirmLabel="Régénérer"
          onClose={() => setConfirm(null)}
          onConfirm={generate}
        />
      )}
      {confirm === 'revoke' && (
        <ConfirmDialog
          title="Révoquer le mot de passe SMB ?"
          message="Les connexions SMB ouvertes avec lui seront coupées. Pour vous reconnecter, il faudra en générer un nouveau."
          confirmLabel="Révoquer"
          onClose={() => setConfirm(null)}
          onConfirm={revoke}
        />
      )}
    </section>
  )
}
