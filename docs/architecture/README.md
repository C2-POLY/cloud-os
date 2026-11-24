# Architecture

Comment Cloud OS est construit, domaine par domaine, avec ses schémas. Le besoin produit est dans le PRD (lien dans `CLAUDE.md`), les choix structurants et leurs raisons sont dans [`docs/decisions/`](../decisions/), et les règles et commandes du dépôt sont dans `CLAUDE.md`.

| Domaine | Fichier | Code | Issues |
|---|---|---|---|
| Vue d'ensemble : la pile, les services, la santé | [overview.md](overview.md) | `compose.yaml`, `core/cmd/core`, `worker/`, `web/` | #3 |
| La box en production : une surcouche Compose, `scripts/box`, `box.env`, les images en arm64 et amd64 ; l'installation sur une machine neuve et le démarrage, une coupure pendant un envoi, la mesure de la configuration requise ; l'accès en HTTPS et hors de la maison par Tailscale, les liens de partage publics par Funnel | [box.md](box.md) | `compose.prod.yaml`, `scripts/box`, `scripts/install`, `box/systemd/`, `scripts/test-install.sh`, `scripts/test-crash.sh`, `scripts/measure`, `Makefile`, `.github/workflows/images.yml`, `box/tailscale/`, `core/internal/edge` | #99, #100, #103 |
| La sauvegarde : chaque nuit, chiffrée (restic) sur un disque USB, une photo cohérente sans arrêter la box, vérifiée chaque semaine, restauration complète | [backup.md](backup.md) | `core/internal/backup`, `core/internal/storage/freeze.go`, `backup/`, `core/migrations/0028_backup.sql`, `scripts/box` | #101 |
| L'état de la box pour l'administrateur : disques, services, sauvegarde, fichiers SMB gardés, et les alertes | [status.md](status.md) | `core/internal/boxstatus`, `core/internal/httpapi/status.go`, `web/src/status/` | #102 |
| Comptes, sessions, accès aux routes et mot de passe SMB | [auth.md](auth.md) | `core/internal/auth`, `core/internal/httpapi`, `web/src/settings` | #6, #91 |
| Stockage des fichiers, dossiers, déplacements, API, envoi reprenable | [storage.md](storage.md) | `core/internal/storage`, `core/internal/httpapi/files.go`, `uploads.go` | #7, #9, #18 |
| Interface web : écrans, adresses, drive, file d'envoi | [web.md](web.md) | `web/src/` | #6, #11 |
| WebDAV : le drive dans le Finder et l'Explorateur | [webdav.md](webdav.md) | `core/internal/davfs`, `auth/basic.go` | #12 |
| SMB : le drive en partage réseau, écrit par `storage`, dans un service à part | [smb.md](smb.md) | `core/internal/smbfs`, `core/third_party/smbsrv`, `core/cmd/core/main.go` (`core smb`), `compose.yaml` | #90, #95, #97, #92, #15 |
| File de tâches : travail de fond partagé par le cœur et les workers | [tasks.md](tasks.md) | `core/internal/tasks`, `worker/src/worker/tasks.py` | #8, #75 |
| Partage par lien : jeton, expiration, mot de passe, page de l'invité | [share.md](share.md) | `core/internal/share`, `core/internal/httpapi/shares.go`, `web/src/share/` | #13 |
| Miniatures des images | [thumbnails.md](thumbnails.md) | `core/internal/thumbs`, `worker/src/worker/thumbs.py`, `web/src/drive/FileList.tsx` | #14 |
| Métadonnées des photos : date de prise de vue, GPS, appareil | [metadata.md](metadata.md) | `core/internal/meta`, `worker/src/worker/meta.py` | #34 |
| Texte des documents : PDF, Office, OpenDocument, textes ; OCR des images et des scans ; classement par catégorie | [documents.md](documents.md) | `core/internal/doctext`, `core/internal/ocr`, `core/internal/doccat`, `worker/src/worker/doctext.py`, `worker/src/worker/ocr.py`, `worker/src/worker/doccat.py` | #67, #68, #73 |
| Recherche par nom et dans le texte des documents, mots tapés en partie ou avec une faute ; Demander, une seule liste des quatre recherches | [search.md](search.md) | `core/internal/storage/search.go`, `words.go`, `core/internal/search`, `core/migrations/0018_document_search.sql`, `0022_search_words.sql`, `web/src/desktop/Palette.tsx` | #48, #69, #72, #85 |
| Recherche des documents par le sens : vecteurs de leur texte, API | [search-ai.md](search-ai.md) | `core/internal/textemb`, `worker/src/worker/textemb.py`, `core/migrations/0021_text_vectors.sql` | #70 |
| Albums de photos : références, corbeille, API | [albums.md](albums.md) | `core/internal/albums`, `core/internal/httpapi/albums.go`, `web/src/desktop/Albums.tsx` | #39 |
| Carte des photos : tuiles PMTiles locales, points géolocalisés | [map.md](map.md) | `core/internal/httpapi/map.go`, `core/internal/storage/geo.go`, `web/src/desktop/MapView.tsx` | #40 |
| App mobile (Expo) : connexion, fichiers, photos | [mobile.md](mobile.md) | `mobile/`, `shared/api-types.ts` | #37 |
| Modèles d'IA locaux : texte, photos, OCR, dans l'image du worker | [models.md](models.md) | `worker/src/worker/models.py`, `worker/models/fetch.sh`, `worker/bench/` | #66 |
| Vecteurs et recherche par le sens : photos, registre des modèles, requêtes encodées par le worker | [embeddings.md](embeddings.md) | `core/internal/photovec`, `core/internal/embed`, `worker/src/worker/photovec.py`, `embedserver.py` | #71 |
| Le site public : la box qui s'ouvre au défilement, en anglais et en français, publié sur GitHub Pages | [site.md](site.md) | `site/`, `.github/workflows/site.yml` | #113 |

## Quand mettre un fichier à jour

Dans la PR qui change le comportement, jamais après. Le design validé d'une issue (sa section « Design » et son schéma Mermaid) est reporté dans le fichier de son domaine à la clôture du sujet. Un nouveau domaine a son propre fichier et sa ligne dans le tableau ci-dessus.

Un fichier décrit le code tel qu'il est, avec les vrais noms de fonctions, de tables et de routes. Un schéma faux est pire que pas de schéma : si le code s'en écarte, on corrige le schéma dans la même PR.
