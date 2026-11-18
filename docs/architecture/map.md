# Carte des photos

La vue Carte de Photos (`/photos/carte`) place les photos et vidéos géolocalisées sur une carte. Tout vient de la box : les tuiles, le style, les polices et les pictogrammes. Aucune requête ne sort de la maison.
Issue : #40.

## Parcours

```mermaid
sequenceDiagram
    participant W as Navigateur (MapLibre)
    participant C as Cœur
    participant F as /maps (lecture seule)
    participant DB as Postgres
    W->>C: GET /api/map
    C->>F: un fichier .pmtiles ?
    C-->>W: {tiles: true, name}
    W->>C: GET /api/map/points
    C->>DB: fichiers vivants du compte avec latitude et longitude (media_meta)
    C-->>W: points
    loop en se déplaçant sur la carte
        W->>C: GET /api/map/tiles.pmtiles (Range: bytes=…)
        C->>F: lit la plage
        C-->>W: 206 Partial Content
    end
    Note over W: MapLibre regroupe les points selon le zoom ; un clic sur un groupe montre ses photos
```

## Les tuiles

- **Un fichier PMTiles** (tuiles vectorielles OpenStreetMap, schéma Protomaps) dans le dossier des cartes : `CLOUDOS_MAPS_DIR` sur l'hôte (par défaut `./.data/maps`), monté en lecture seule sur `/maps` dans le cœur (`MAPS_DIR`). S'il y en a plusieurs, le premier par nom est servi.
- **Le servir :** `GET /api/map/tiles.pmtiles` (session exigée) envoie le fichier par plages HTTP (`http.ServeContent`, 206) : le navigateur ne lit que les morceaux des tuiles affichées. `GET /api/map` dit s'il y a un fichier.
- **Sans fichier**, la vue explique comment en installer un au lieu d'afficher une carte vide.
- **Obtenir un fichier :** le monde entier fait plus de 100 Go ; on extrait sa région d'un build quotidien de Protomaps avec l'outil `pmtiles` (BSD-3), une fois, depuis un ordinateur relié à internet :

```bash
go install github.com/protomaps/go-pmtiles@v1.28.0
go-pmtiles extract https://build.protomaps.com/<AAAAMMJJ>.pmtiles .data/maps/region.pmtiles --bbox=<ouest>,<sud>,<est>,<nord> --maxzoom=14
```

Paris intra-muros jusqu'au zoom 14 fait environ 12 Mo, la France entière quelques Go.

## Le style, les polices et les pictogrammes

- `maplibre-gl` (BSD-3) dessine la carte ; `pmtiles` (BSD-3) lit le fichier par plages ; `@protomaps/basemaps` (BSD-3) donne les couches du style, en clair ou en sombre selon le système, avec les noms en français. MapLibre est chargé à la demande, seulement quand on ouvre la carte.
- `mapStyle` (`web/src/desktop/map.ts`) pointe vers la box seulement : les glyphes (`/map/fonts/{fontstack}/{range}.pbf`), les pictogrammes (`/map/sprites/light` ou `dark`) et les tuiles (`pmtiles://…/api/map/tiles.pmtiles`). Un test vérifie qu'aucune autre adresse n'y figure ; seul le lien d'attribution OpenStreetMap, qu'on clique, mène ailleurs.
- **Polices et pictogrammes** sont dans `web/public/map`, copiés depuis `protomaps/basemaps-assets` au commit fixé dans `web/scripts/fetch-map-assets.sh` : Noto Sans Regular, Medium et Italic (plages latin, latin étendu et ponctuation ; OFL-1.1, [ADR 004](../decisions/004-fonts-ofl.md)), et les pictogrammes v4 clair et sombre (dérivés de tangrams/icons, MIT).
- **Plages absentes :** sous `/map/` (et `/assets/`), le cœur répond 404 pour un fichier absent au lieu de la page de l'app ; les lettres hors des plages copiées (cyrillique, grec, asiatiques) ne s'affichent pas, la carte reste lisible en alphabet latin.
- **Données :** OpenStreetMap, sous ODbL, avec l'attribution affichée en bas de la carte.

## Les points

`GET /api/map/points` (`storage.GeoItems`) renvoie les photos et vidéos vivantes du compte qui ont une latitude et une longitude dans `media_meta` (lues par les workers, voir [metadata.md](metadata.md)), avec leurs champs de timeline, du plus récent au plus ancien, 100 000 au plus. La vue en fait une source GeoJSON regroupée par MapLibre (`cluster`) ; un clic sur un groupe liste ses photos (`getClusterLeaves`) dans un panneau, et une photo s'ouvre dans la visionneuse.
