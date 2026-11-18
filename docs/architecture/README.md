# Architecture

Comment Cloud OS est construit, domaine par domaine, avec ses schémas. Le besoin produit est dans le PRD (lien dans `CLAUDE.md`), les choix structurants et leurs raisons sont dans [`docs/decisions/`](../decisions/), et les règles et commandes du dépôt sont dans `CLAUDE.md`.

| Domaine | Fichier | Code | Issues |
|---|---|---|---|
| Vue d'ensemble : la pile, les services, la santé | [overview.md](overview.md) | `compose.yaml`, `core/cmd/core`, `worker/`, `web/` | #3 |
| Comptes, sessions et accès aux routes | [auth.md](auth.md) | `core/internal/auth`, `core/internal/httpapi` | #6 |
| Stockage des fichiers, dossiers, déplacements, API, envoi reprenable | [storage.md](storage.md) | `core/internal/storage`, `core/internal/httpapi/files.go`, `uploads.go` | #7, #9, #18 |
| Interface web : écrans, adresses, drive, file d'envoi | [web.md](web.md) | `web/src/` | #6, #11 |
| WebDAV : le drive dans le Finder et l'Explorateur | [webdav.md](webdav.md) | `core/internal/davfs`, `auth/basic.go` | #12 |
| File de tâches : travail de fond partagé par le cœur et les workers | [tasks.md](tasks.md) | `core/internal/tasks`, `worker/src/worker/tasks.py` | #8, #75 |
| Partage par lien : jeton, expiration, mot de passe, page de l'invité | [share.md](share.md) | `core/internal/share`, `core/internal/httpapi/shares.go`, `web/src/share/` | #13 |
| Miniatures des images | [thumbnails.md](thumbnails.md) | `core/internal/thumbs`, `worker/src/worker/thumbs.py`, `web/src/drive/FileList.tsx` | #14 |
| Métadonnées des photos : date de prise de vue, GPS, appareil | [metadata.md](metadata.md) | `core/internal/meta`, `worker/src/worker/meta.py` | #34 |
| Texte des documents : PDF, Office, OpenDocument, textes ; OCR des images et des scans | [documents.md](documents.md) | `core/internal/doctext`, `core/internal/ocr`, `worker/src/worker/doctext.py`, `worker/src/worker/ocr.py` | #67, #68 |
| Recherche par nom et dans le texte des documents, palette Demander | [search.md](search.md) | `core/internal/storage/search.go`, `core/migrations/0018_document_search.sql`, `web/src/desktop/Palette.tsx` | #48, #69 |
| Albums de photos : références, corbeille, API | [albums.md](albums.md) | `core/internal/albums`, `core/internal/httpapi/albums.go`, `web/src/desktop/Albums.tsx` | #39 |
| Carte des photos : tuiles PMTiles locales, points géolocalisés | [map.md](map.md) | `core/internal/httpapi/map.go`, `core/internal/storage/geo.go`, `web/src/desktop/MapView.tsx` | #40 |
| App mobile (Expo) : connexion, fichiers, photos | [mobile.md](mobile.md) | `mobile/`, `shared/api-types.ts` | #37 |
| Modèles d'IA locaux : texte, photos, OCR, dans l'image du worker | [models.md](models.md) | `worker/src/worker/models.py`, `worker/models/fetch.sh`, `worker/bench/` | #66 |

## Quand mettre un fichier à jour

Dans la PR qui change le comportement, jamais après. Le design validé d'une issue (sa section « Design » et son schéma Mermaid) est reporté dans le fichier de son domaine à la clôture du sujet. Un nouveau domaine a son propre fichier et sa ligne dans le tableau ci-dessus.

Un fichier décrit le code tel qu'il est, avec les vrais noms de fonctions, de tables et de routes. Un schéma faux est pire que pas de schéma : si le code s'en écarte, on corrige le schéma dans la même PR.
