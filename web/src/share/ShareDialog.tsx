import { Check, Copy } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { messageOf } from '../api.ts'
import { DismissButton, Modal } from '../drive/Dialogs.tsx'
import type { Entry } from '../drive/files.ts'
import { createShare, expiryOf, shareURL } from './shares.ts'

const choices = [
  { value: '1', label: '1 jour' },
  { value: '7', label: '7 jours' },
  { value: '30', label: '30 jours' },
  { value: 'date', label: 'Jusqu’au…' },
]

/** Makes a link to a file or folder, then shows it once with a Copy button. */
export function ShareDialog({ entry, onClose }: { entry: Entry; onClose: () => void }) {
  const [choice, setChoice] = useState('7')
  const [date, setDate] = useState('')
  const [protect, setProtect] = useState(false)
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [url, setURL] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    const expires = expiryOf(choice, date)
    if (!expires || expires.getTime() <= Date.now()) {
      setError('Choisissez une date d’expiration dans le futur.')
      return
    }
    if (protect && password.length < 6) {
      setError('Le mot de passe du lien doit avoir au moins 6 caractères.')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const { token } = await createShare(entry.path, expires, protect ? password : '')
      setURL(shareURL(token))
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  const title = `Partager « ${entry.name} »`
  if (url) {
    return (
      <Modal title={title} onClose={onClose}>
        <div className="form share-result">
          <label>
            Lien
            <input readOnly value={url} onFocus={(e) => e.currentTarget.select()} aria-label="Lien de partage" />
          </label>
          <p className="muted hint">Copiez-le maintenant : pour des raisons de sécurité, il ne sera plus affiché.</p>
        </div>
        <div className="dialog-buttons">
          <DismissButton>
            Fermer
          </DismissButton>
          <button
            type="button"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(url)
                setCopied(true)
              } catch {
                setError('Copie impossible : sélectionnez le lien et copiez-le.')
              }
            }}
          >
            {copied ? <Check size={16} aria-hidden /> : <Copy size={16} aria-hidden />} {copied ? 'Copié' : 'Copier le lien'}
          </button>
        </div>
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
      </Modal>
    )
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <p className="muted hint-text">
          Toute personne qui a le lien pourra voir et télécharger {entry.kind === 'dir' ? 'ce dossier et son contenu' : 'ce fichier'}, sans
          compte et sans pouvoir rien modifier.
        </p>
        <fieldset className="choices">
          <legend>Expire dans</legend>
          {choices.map((c) => (
            <label key={c.value} className="choice">
              <input type="radio" name="expiry" value={c.value} checked={choice === c.value} onChange={() => setChoice(c.value)} />
              {c.label}
            </label>
          ))}
        </fieldset>
        {choice === 'date' && (
          <label>
            Date d’expiration
            <input type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
          </label>
        )}
        <label className="choice">
          <input type="checkbox" checked={protect} onChange={(e) => setProtect(e.target.checked)} />
          Protéger par un mot de passe
        </label>
        {protect && (
          <label>
            Mot de passe du lien
            <input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={6} />
          </label>
        )}
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-buttons">
          <DismissButton>
            Annuler
          </DismissButton>
          <button type="submit" disabled={busy}>
            Créer le lien
          </button>
        </div>
      </form>
    </Modal>
  )
}
