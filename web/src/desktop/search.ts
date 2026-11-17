import { parentOf, type Entry } from '../drive/files.ts'

/** The groups of the results, in the order they are shown. */
export const groups = ['Dossiers', 'Photos et vidéos', 'Documents'] as const
export type Group = (typeof groups)[number]

export function groupOf(e: Entry): Group {
  if (e.kind === 'dir') return 'Dossiers'
  if (e.mime.startsWith('image/') || e.mime.startsWith('video/')) return 'Photos et vidéos'
  return 'Documents'
}

/** The folder that shows an entry in Fichiers: itself for a folder, its parent for a file. */
export function folderOf(e: Entry): string {
  return e.kind === 'dir' ? e.path : parentOf(e.path)
}
