import { ChevronRight, HardDrive } from 'lucide-react'
import { driveURL, navigate } from '../router.ts'

/** "Mon drive › Photos › 2024", each part a link to its folder. */
export function Breadcrumb({ folder }: { folder: string }) {
  const parts = folder ? folder.split('/') : []
  return (
    <nav className="breadcrumb" aria-label="Fil d'Ariane">
      <a
        href={driveURL('')}
        onClick={(e) => {
          e.preventDefault()
          navigate(driveURL(''))
        }}
        aria-current={parts.length === 0 ? 'page' : undefined}
      >
        <HardDrive size={16} aria-hidden /> Mon drive
      </a>
      {parts.map((name, i) => {
        const path = parts.slice(0, i + 1).join('/')
        const last = i === parts.length - 1
        return (
          <span key={path} className="crumb">
            <ChevronRight size={14} aria-hidden className="muted" />
            <a
              href={driveURL(path)}
              onClick={(e) => {
                e.preventDefault()
                navigate(driveURL(path))
              }}
              aria-current={last ? 'page' : undefined}
            >
              {name}
            </a>
          </span>
        )
      })}
    </nav>
  )
}
