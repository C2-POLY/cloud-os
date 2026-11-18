# Miniatures

Les images et les vidéos du drive ont une miniature, faite en tâche de fond par les workers et stockée en base. Les originaux ne sont jamais modifiés.
Issue : #14, HEIC : #30, vidéos : #41.

## Parcours

```mermaid
sequenceDiagram
    participant W as navigateur
    participant C as cœur (Go)
    participant DB as Postgres
    participant K as worker (Python)
    W->>C: PUT /api/files/content, tus, WebDAV, import
    C->>C: storage écrit le fichier, puis AfterStore
    C->>DB: thumbs.Enqueue : tâche thumb {sha256}, dedupe thumb:<sha256>
    K->>DB: Claim
    K->>DB: un fichier vivant avec ce sha256 (owner, path)
    K->>K: lit /data en lecture seule, vérifie le sha256
    K->>K: vidéo : une image avec ffmpeg (video.py) ; HEIF ou HEIC : libheif (heif.py) ; sinon Pillow
    K->>DB: INSERT thumbnails (WebP 256 px)
    K->>DB: Complete
    W->>C: GET /api/files/thumb?path=…&v=<sha256>
    C->>DB: thumbs.Get(sha256)
    C-->>W: image/webp, ou 404 tant qu'elle n'existe pas
```

## Mise en file

- **Quand :** `storage.Store.AfterStore` est appelé après chaque fichier enregistré : envoi, envoi reprenable, WebDAV, import, remplacement, version restaurée. Le cœur y branche `thumbs.Enqueue` (et `meta.Enqueue`, voir [metadata.md](metadata.md)).
- **Formats :** une image JPEG, PNG, WebP, HEIF ou HEIC (`image/heic`, `image/heif`), ou une vidéo MP4 ou QuickTime (`video/mp4`, `video/quicktime`, extensions `.mp4`, `.m4v`, `.mov`), reconnue par son type MIME ou par ses premiers octets (un fichier sans extension compte). `storage.SniffMIME` reconnaît un HEIF par sa boîte `ftyp` (marques `heic`, `heix`, `heim`, `heis`, `mif1`, `msf1`), que `http.DetectContentType` ignore, et une vidéo par la marque principale de cette boîte (`qt  ` pour QuickTime ; `isom`, `iso2`, `mp41`, `mp42`, `avc1`, `M4V `… pour MP4, que `http.DetectContentType` ne reconnaît que si une marque commence par `mp4`) ; `storage` connaît les extensions `.heic`, `.heif`, `.mp4`, `.m4v` et `.mov` quel que soit le `mime.types` du système.
- **Doublons :** rien n'est mis en file si le contenu a déjà sa miniature, ou si une tâche est déjà en attente pour lui (`dedupe_key`).
- **Priorités :** `PriorityNew` pour un envoi, `PriorityBackfill` pour un import de bibliothèque, qui ne doit pas retarder les nouveaux fichiers.
- **Rattrapage :** `core thumbs backfill` met en file, à priorité de rattrapage, chaque contenu vivant sans miniature : fonds stocké avant #14 (et les vidéos stockées avant #41), fichiers adoptés par l'import, tâche perdue (la mise en file est faite au mieux, sans faire échouer l'écriture). Une image illisible y est retentée une fois.

## Génération (worker)

`worker/src/worker/thumbs.py`, avec Pillow (licence HPND, permissive) pour JPEG, PNG et WebP, et `worker/src/worker/heif.py` pour HEIF et HEIC :
- **Lecture :** le worker cherche un fichier vivant avec cette empreinte, le lit depuis `/data`, monté **en lecture seule**, et vérifie son SHA-256 avant de s'en servir (`worker/src/worker/content.py`, commun avec les [métadonnées](metadata.md)).
- **Rendu :** l'orientation EXIF est appliquée, puis la miniature WebP fait 256 px sur le grand côté (qualité 80), en gardant la transparence.
- **HEIF et HEIC :** `heif.py` appelle la `libheif` du système avec `ctypes` (paquets Debian `libheif1` et `libheif-plugin-libde265`, décodeurs seulement, voir [l'ADR 002](../decisions/002-lgpl.md)). Il décode l'image principale en RGB ou RGBA ; `libheif` applique elle-même la rotation et le miroir du fichier (`irot`, `imir`), donc l'orientation EXIF n'est pas réappliquée. La taille annoncée est vérifiée avant le décodage (plus de 100 mégapixels : refusée). Une erreur de `libheif` est définitive ; une `libheif` absente (image mal construite) est une erreur passagère, retentée.
- **Vidéos :** `video.py` lance le `ffmpeg` LGPL du projet ([l'ADR 002](../decisions/002-lgpl.md)) sur le fichier stocké, sans le charger en mémoire : une image à 1 s (après un éventuel noir de début), ou la première pour une vidéo plus courte, sortie en PNG dans un tuyau puis réduite comme une photo. `ffmpeg` applique la rotation de la vidéo, la miniature est donc droite. Chaque lancement est limité à 30 s et, sous Linux, à 2 Gio de mémoire. Une vidéo illisible, ou trop longue à lire, est une erreur définitive ; un `ffmpeg` absent est une erreur passagère, retentée.
- **Erreurs définitives (`Permanent`, la tâche passe en `failed` au premier essai, la file continue) :** image illisible (HEIC tronqué ou abîmé compris), format non couvert, plus de 100 mégapixels, fichier de plus de 200 Mo.
- **Erreurs passagères (nouvel essai avec le délai de la file) :** le fichier a bougé ou changé pendant la lecture.
- **Rien à faire (`done`) :** aucun fichier vivant n'a plus ce contenu.

## Stockage

Table `thumbnails (sha256, size, mime, width, height, bytes)`, clé `(sha256, size)` : deux fichiers identiques partagent leur miniature, et une nouvelle version a la sienne. Environ 15 Ko par image : 100 000 photos font à peu près 1,5 Go dans Postgres.

Pas de clé étrangère vers `files` : toutes les 30 s, avec `Reap`, le cœur fait `thumbs.Prune`, qui supprime les miniatures dont aucune ligne de `files` (vivante ou en corbeille) n'a l'empreinte. Une image restaurée de la corbeille retrouve donc la sienne.

## API et web

- **API :** `GET /api/files/thumb?path=` exige une session et ne sert que l'espace du compte connecté. Elle renvoie 404 pour un dossier, un fichier absent, ou une image sans miniature (encore). Elle envoie `ETag` (l'empreinte), `Cache-Control: private, max-age=86400` et répond 304 à la revalidation.
- **Web :** le drive, la Chronologie et Photos affichent la miniature à la place de l'icône (`thumbURL` dans `web/src/drive/files.ts`, vidéos comprises, avec leur durée en bas à droite), chargée paresseusement, avec `?v=` tiré de l'empreinte pour qu'une nouvelle version ne soit jamais servie depuis le cache. Si la miniature n'existe pas encore, l'icône reste.
