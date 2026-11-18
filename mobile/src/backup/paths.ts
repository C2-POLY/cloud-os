/** The phone's folder name, safe as one path segment: "iPhone d'Enzo". */
export function folderName(name: string | null | undefined): string {
  const safe = [...(name ?? '')].map((ch) => (ch === '/' || ch === '\\' || ch.charCodeAt(0) < 32 ? '-' : ch)).join('')
  const clean = safe.replace(/^\.+/, '').trim()
  return clean.slice(0, 80) || 'Téléphone'
}

const pad = (n: number) => String(n).padStart(2, '0')

/** Where an element of the camera roll goes: Photos/<phone>/YYYY/MM/<name>, by its local shooting date. */
export function destination(phone: string, filename: string, takenMs: number): string {
  const d = new Date(takenMs)
  return `Photos/${folderName(phone)}/${d.getFullYear()}/${pad(d.getMonth() + 1)}/${folderName(filename)}`
}

/** The n-th name to try when the first one is taken: "IMG_0001 (2).HEIC". */
export function variant(path: string, n: number): string {
  if (n <= 1) return path
  const slash = path.lastIndexOf('/')
  const dot = path.lastIndexOf('.')
  return dot > slash + 1 ? `${path.slice(0, dot)} (${n})${path.slice(dot)}` : `${path} (${n})`
}
