import { AlertTriangle, Check, Circle, Clock, Loader, Minus, type LucideIcon } from 'lucide-react'
import { overallLabel, stateLabel, stepLabels, useFileAnalysis, type AnalysisState } from './analysis.ts'

const icons: Record<AnalysisState, LucideIcon> = {
  waiting: Clock,
  running: Loader,
  done: Check,
  failed: AlertTriangle,
  skipped: Minus,
  idle: Circle,
}

/**
 * Where the analysis of a file stands, step by step: what the workers do for
 * its type (text read, OCR, meaning indexed; for a photo, thumbnail, date and
 * place, what it shows), each waiting, running, done, or failed with the
 * reason. It updates itself while the file is open. `tone="dark"` is for the
 * photo viewer's dark panel.
 */
export function AnalysisSteps({ path, tone }: { path: string; tone?: 'dark' }) {
  const analysis = useFileAnalysis(path)
  if (!analysis || analysis.state === 'none') return null
  return (
    <section className={tone === 'dark' ? 'analysis dark' : 'analysis'} aria-label="Analyse du fichier">
      <p className={`analysis-title ${analysis.state}`} role={analysis.state === 'failed' ? 'alert' : 'status'}>
        {overallLabel(analysis)}
      </p>
      <ul className="analysis-steps">
        {analysis.steps.map((step) => {
          const Icon = icons[step.state]
          return (
            <li key={step.kind} className={`analysis-step ${step.state}`}>
              <Icon size={14} aria-hidden />
              <span className="analysis-step-name">{stepLabels[step.kind]}</span>
              <span className="analysis-step-state">{stateLabel(step)}</span>
              {step.error && (
                <span className="analysis-step-error">
                  {step.state === 'failed' ? 'Raison : ' : 'Dernière erreur : '}
                  {step.error}
                </span>
              )}
            </li>
          )
        })}
      </ul>
    </section>
  )
}
