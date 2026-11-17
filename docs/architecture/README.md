# Architecture

Comment Cloud OS est construit, domaine par domaine, avec ses schémas. Le besoin produit est dans le PRD (lien dans `CLAUDE.md`), les choix structurants et leurs raisons sont dans [`docs/decisions/`](../decisions/), et les règles et commandes du dépôt sont dans `CLAUDE.md`.

| Domaine | Fichier | Code | Issues |
|---|---|---|---|
| Vue d'ensemble : la pile, les services, la santé | [overview.md](overview.md) | `compose.yaml`, `core/cmd/core`, `worker/`, `web/` | #3 |
| Comptes, sessions et accès aux routes | [auth.md](auth.md) | `core/internal/auth`, `core/internal/httpapi` | #6 |
| Stockage des fichiers, dossiers, déplacements, API, envoi reprenable | [storage.md](storage.md) | `core/internal/storage`, `core/internal/httpapi/files.go`, `uploads.go` | #7, #9, #18 |
| Interface web : écrans, adresses, drive, file d'envoi | [web.md](web.md) | `web/src/` | #6, #11 |
| WebDAV : le drive dans le Finder et l'Explorateur | [webdav.md](webdav.md) | `core/internal/davfs`, `auth/basic.go` | #12 |
| File de tâches : travail de fond partagé par le cœur et les workers | [tasks.md](tasks.md) | `core/internal/tasks`, `worker/src/worker/tasks.py` | #8 |
| Partage par lien : jeton, expiration, mot de passe, page de l'invité | [share.md](share.md) | `core/internal/share`, `core/internal/httpapi/shares.go`, `web/src/share/` | #13 |
| Miniatures des images | [thumbnails.md](thumbnails.md) | `core/internal/thumbs`, `worker/src/worker/thumbs.py`, `web/src/drive/FileList.tsx` | #14 |

## Quand mettre un fichier à jour

Dans la PR qui change le comportement, jamais après. Le design validé d'une issue (sa section « Design » et son schéma Mermaid) est reporté dans le fichier de son domaine à la clôture du sujet. Un nouveau domaine a son propre fichier et sa ligne dans le tableau ci-dessus.

Un fichier décrit le code tel qu'il est, avec les vrais noms de fonctions, de tables et de routes. Un schéma faux est pire que pas de schéma : si le code s'en écarte, on corrige le schéma dans la même PR.
