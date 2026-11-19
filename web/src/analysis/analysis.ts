import { useEffect, useRef, useState } from 'react'
import { api } from '../api.ts'
import type { AnalysisKind, AnalysisState, AnalysisStep, AnalysisSummary, FileAnalysis } from '../../../shared/api-types.ts'

export type { AnalysisKind, AnalysisState, AnalysisStep, AnalysisSummary, FileAnalysis }

/** How often the state is read: fast while something is pending, slowly otherwise. */
export const POLL_BUSY_MS = 2_500
export const POLL_IDLE_MS = 15_000

export function fetchAnalysisSummary(): Promise<AnalysisSummary> {
  return api<AnalysisSummary>('GET', '/api/analysis')
}

export function fetchFileAnalysis(path: string): Promise<FileAnalysis> {
  return api<FileAnalysis>('GET', `/api/analysis/file?path=${encodeURIComponent(path)}`)
}

/**
 * Reads `read` now, then again after `delay(value)` for as long as the caller
 * is mounted; null stops it. A tab in the background skips the reads and keeps
 * its timer. A failed read keeps the last value and tries again slowly.
 */
function usePolled<T>(read: () => Promise<T>, delay: (value: T) => number | null, key: string, refreshOn?: string): T | null {
  const [value, setValue] = useState<{ key: string; value: T } | null>(null)
  const latest = useRef({ read, delay })
  useEffect(() => {
    latest.current = { read, delay }
  })
  useEffect(() => {
    let current = true
    let timer: number | undefined
    const tick = async () => {
      let next = POLL_IDLE_MS as number | null
      if (!document.hidden) {
        try {
          const v = await latest.current.read()
          if (!current) return
          setValue({ key, value: v })
          next = latest.current.delay(v)
        } catch {
          // keep what is shown
        }
      }
      if (current && next !== null) timer = window.setTimeout(() => void tick(), next)
    }
    void tick()
    // Something happened that starts work (a file arrived): read now, not at the next slow tick.
    const refresh = () => {
      window.clearTimeout(timer)
      void tick()
    }
    if (refreshOn) window.addEventListener(refreshOn, refresh)
    return () => {
      current = false
      window.clearTimeout(timer)
      if (refreshOn) window.removeEventListener(refreshOn, refresh)
    }
  }, [key, refreshOn])
  return value?.key === key ? value.value : null
}

/** Event the app sends when a file has just been stored, so the menu bar does not wait for its slow tick. */
export const FILE_STORED = 'cloudos:file-stored'

/** How many of the account's files are being analysed, or failed; null until known. */
export function useAnalysisSummary(): AnalysisSummary | null {
  return usePolled(fetchAnalysisSummary, (s) => (s.analysing > 0 ? POLL_BUSY_MS : POLL_IDLE_MS), 'summary', FILE_STORED)
}

/** Where the analysis of one file stands, kept up to date; null until known (or when the core cannot say). */
export function useFileAnalysis(path: string): FileAnalysis | null {
  return usePolled(
    () => fetchFileAnalysis(path),
    (a) => (a.state === 'pending' ? POLL_BUSY_MS : a.state === 'none' ? null : POLL_IDLE_MS),
    path,
  )
}

/** The text of the menu bar: "Analyse de 3 fichiers…", or "Analyse à jour". */
export function summaryLabel(s: AnalysisSummary): string {
  if (s.analysing === 0) return 'Analyse à jour'
  return s.analysing === 1 ? 'Analyse d’un fichier…' : `Analyse de ${s.analysing} fichiers…`
}

/** The failures, for the menu bar; '' when there is none. */
export function failedLabel(s: AnalysisSummary): string {
  if (s.failed === 0) return ''
  return s.failed === 1 ? '1 fichier non analysé' : `${s.failed} fichiers non analysés`
}

/** What each step does, in the words of the user. */
export const stepLabels: Record<AnalysisKind, string> = {
  thumb: 'Miniature',
  meta: 'Date, lieu et appareil',
  text: 'Texte lu',
  ocr: 'Texte reconnu (OCR)',
  photovec: 'Contenu de l’image indexé',
  textemb: 'Sens du texte indexé',
  doccat: 'Catégorie du document',
}

const stateLabels: Record<AnalysisState, string> = {
  waiting: 'en attente',
  running: 'en cours',
  done: 'fait',
  failed: 'échec',
  skipped: 'sans objet',
  idle: 'pas lancé',
}

/** One word for a step's state; a step tried again says so. */
export function stateLabel(step: AnalysisStep): string {
  if (step.state === 'waiting' && step.attempts) return `nouvel essai à venir (${step.attempts}/${step.max_attempts ?? '?'})`
  return stateLabels[step.state]
}

/** The line above the steps: where the whole analysis stands. */
export function overallLabel(a: FileAnalysis): string {
  switch (a.state) {
    case 'pending':
      return 'Analyse en cours…'
    case 'failed':
      return 'L’analyse a échoué : ce fichier ne sera pas retrouvé par son contenu.'
    case 'ready':
      return 'Analyse terminée'
    case 'none':
      return 'Ce type de fichier n’est pas analysé.'
  }
}
