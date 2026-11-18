# Métadonnées des photos

Chaque image et chaque vidéo du drive a ses métadonnées en base : date de prise de vue, lieu, appareil, dimensions et, pour une vidéo, durée. Elles sont lues dans l'EXIF d'une photo, ou par `ffprobe` dans le conteneur d'une vidéo, par les workers, en tâche de fond. Les originaux ne sont jamais modifiés. Elles servent la Chronologie de Fichiers (#47, `GET /api/timeline`, voir [web.md](web.md)), l'app Photos et sa visionneuse (#35) et la carte (#40).
Issue : #34, HEIC : #30, vidéos : #41.

## Parcours

```mermaid
sequenceDiagram
    participant C as cœur (Go)
    participant DB as Postgres
    participant K as worker (Python)
    C->>C: storage écrit le fichier, puis AfterStore
    C->>DB: meta.Enqueue : tâche meta {sha256}, dedupe meta:<sha256>, sauf si media_meta l'a déjà
    K->>DB: Claim (kind meta)
    K->>DB: un fichier vivant avec ce sha256 (owner, path)
    alt aucun fichier vivant
        K->>DB: Complete (rien à faire)
    else
        K->>K: lit /data en lecture seule, vérifie le sha256
        K->>K: vidéo : ffprobe (video.py) ; HEIF ou HEIC : heif.probe (taille et bloc Exif) ; sinon Image.open (en-tête) et getexif()
        alt image illisible ou format non couvert
            K->>DB: GiveUp (Permanent)
        else
            K->>DB: INSERT media_meta ON CONFLICT DO NOTHING (champs absents à NULL)
            K->>DB: Complete
        end
    end
    C->>DB: toutes les 30 s, meta.Prune : lignes dont aucun fichier n'a l'empreinte
    Note over C,DB: GET /api/files/meta?path= : Lookup du compte, puis meta.Get(sha256), 404 tant qu'absente
```

## Mise en file

- **Quand :** le crochet `AfterStore` du cœur (`queueBackgroundWork` dans `core/cmd/core/main.go`) met en file la miniature et les métadonnées de chaque fichier enregistré : envoi, envoi reprenable, WebDAV, import, remplacement, version restaurée.
- **Formats :** les mêmes que les miniatures (`thumbs.IsMedia`) : JPEG, PNG, WebP, HEIF, HEIC, MP4 et QuickTime, reconnus par leur type MIME ou leurs premiers octets.
- **Doublons :** rien n'est mis en file si le contenu a déjà sa ligne, ou si une tâche est en attente pour lui (`dedupe_key`).
- **Priorités :** `PriorityNew` pour un envoi, `PriorityBackfill` pour un import.
- **Rattrapage :** `core meta backfill` met en file, à priorité de rattrapage, chaque contenu vivant sans métadonnées.

## Lecture (worker)

`worker/src/worker/meta.py`, avec Pillow. Le fichier est lu par `worker/src/worker/content.py`, commun avec les miniatures : un fichier vivant avec cette empreinte, lu depuis `/data` **en lecture seule**, vérifié par son SHA-256. Les pixels ne sont pas décodés : Pillow ne lit que l'en-tête et l'EXIF. Pour un HEIF ou un HEIC, `heif.probe` (voir [thumbnails.md](thumbnails.md)) lit avec `libheif` la taille de l'image principale et son bloc `Exif`, que Pillow analyse ensuite comme celui d'un JPEG.

| Champ | Source EXIF |
|---|---|
| `taken_at` | `DateTimeOriginal`, sinon `DateTimeDigitized`, sinon `DateTime` ; avec les fractions de seconde (`SubSecTime…`). L'heure locale de la prise de vue, gardée sans fuseau |
| `taken_offset` | Le décalage de la date retenue (`OffsetTimeOriginal`, `…Digitized`, `OffsetTime`), en minutes |
| `latitude`, `longitude` | IFD GPS, en degrés, signés par `GPSLatitudeRef` (`S`) et `GPSLongitudeRef` (`W`) |
| `make`, `model`, `lens` | `Make`, `Model`, `LensModel` |
| `width`, `height` | La taille de l'image, inversée pour les orientations 5 à 8 : la taille affichée |
| `orientation` | `Orientation`, de 1 à 8. Toujours 1 pour un HEIF ou un HEIC : `libheif` rend des pixels déjà redressés, à la taille affichée |

### Vidéos

`worker/src/worker/video.py` lance `ffprobe` (le binaire LGPL du projet, [l'ADR 002](../decisions/002-lgpl.md)) sur le fichier stocké et lit son JSON :

| Champ | Source |
|---|---|
| `taken_at`, `taken_offset` | `com.apple.quicktime.creationdate` (iPhone : l'heure locale avec son fuseau), sinon `creation_time` (UTC, écrit par Android et la plupart des appareils), gardé en UTC avec un décalage 0. 1904-01-01, l'origine des dates QuickTime qu'écrivent les appareils sans horloge, compte comme absent |
| `latitude`, `longitude` | `com.apple.quicktime.location.ISO6709`, sinon `location` (ISO 6709, `+48.8584+002.2945/`) |
| `make`, `model` | `com.apple.quicktime.make`, `com.apple.quicktime.model` |
| `width`, `height` | La taille du premier flux vidéo, inversée quand sa rotation est de 90° ou 270° : la taille affichée |
| `duration_ms` | La durée du conteneur, sinon celle du flux vidéo, en millisecondes. NULL pour une photo |

`orientation` reste NULL : `ffmpeg` redresse lui-même l'image. Un fichier sans flux vidéo, illisible, ou que `ffprobe` ne lit pas en 30 s, est une erreur définitive.

- **Valeurs aberrantes, ignorées et non fatales :** une date impossible (`0000:00:00`, 30 février, hors 1900–2200), un décalage au-delà de ±18 h, un GPS hors bornes ou à 0,0 (ce qu'écrivent des appareils sans position), une orientation hors 1–8. Les textes perdent leurs octets nuls et sont coupés à 200 caractères.
- **Image sans EXIF ou à l'EXIF abîmé :** elle a quand même sa ligne, avec ses dimensions, le reste à NULL. Elle n'est pas retentée.
- **Erreurs définitives (`Permanent`) :** image illisible (en-tête HEIC abîmé compris ; un HEIC tronqué dans ses données garde ses métadonnées, comme un JPEG), format non couvert, plus de 100 mégapixels, fichier de plus de 200 Mo.
- **Erreurs passagères :** le fichier a bougé ou changé pendant la lecture.

## Stockage

Table `media_meta`, clé `sha256` : deux fichiers identiques partagent leur ligne, une nouvelle version a la sienne. Chaque champ est nullable ; des contraintes `CHECK` refusent un GPS ou un décalage hors bornes, une latitude sans longitude, un décalage sans date.

Pas de clé étrangère vers `files` : toutes les 30 s, avec `Reap` et `thumbs.Prune`, le cœur fait `meta.Prune`, qui supprime les lignes dont aucun fichier (vivant ou en corbeille) n'a l'empreinte.

## API

`GET /api/files/meta?path=` exige une session et ne sert que l'espace du compte connecté. 404 pour un dossier, un fichier absent ou une image pas encore lue. Sinon :

```json
{"taken_at": "2024-07-14T18:03:22", "taken_offset": "+02:00", "latitude": 48.8584, "longitude": 2.2945,
 "make": "Apple", "model": "iPhone 15", "lens": "iPhone 15 back camera 6.86mm f/1.78",
 "width": 3024, "height": 4032, "orientation": 6, "duration_ms": null}
```

`taken_at` est l'heure locale de la prise de vue, sans fuseau ; un champ absent du fichier vaut `null`. `duration_ms` est la durée d'une vidéo, aussi renvoyée par `GET /api/timeline` sur chaque vidéo (omise pour une photo).
