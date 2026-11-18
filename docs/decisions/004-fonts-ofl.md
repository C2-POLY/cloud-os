# 004 — Les polices sous licence OFL

- Date : 2026-11-18
- Statut : acceptée
- Issue : #40

## Décision

**La SIL Open Font License 1.1 (OFL) est acceptée pour les polices**, et seulement pour elles : des fichiers de police (ou leurs glyphes convertis, comme les `.pbf` d'une carte) servis tels quels, jamais du code. Le reste de la règle des dépendances ne change pas.

Premier usage : les noms de lieux de la carte de Photos (#40) s'affichent avec Noto Sans (Regular, Medium, Italic), dont les glyphes viennent de [protomaps/basemaps-assets](https://github.com/protomaps/basemaps-assets), au commit fixé dans `web/scripts/fetch-map-assets.sh`.

## Pourquoi

**Presque toutes les polices libres sont sous OFL.** C'est la licence que Google Fonts, Noto et la plupart des fonderies libres utilisent ; une carte sans police ne montre ni villes ni rues.

**L'OFL est compatible avec une sortie en open source, sous licence permissive ou non.** Elle permet d'utiliser, d'embarquer et de redistribuer la police avec un logiciel, y compris commercial. Ses seules conditions portent sur la police elle-même : ne pas la vendre seule, la redistribuer avec sa licence, et renommer une version modifiée. Elle n'impose rien au code de Cloud OS.

## Ce que ça impose

- **Les polices restent telles quelles**, avec leur licence à côté (`web/public/map/fonts/OFL.txt`).
- **Pas de police modifiée** sous le même nom.
- **Les avis de licence** des polices accompagneront la distribution de l'OS, comme ceux des composants LGPL (ADR 002).

## Options écartées

- **Une carte sans texte** : routes, côtes et frontières seulement, sans nom de lieu. On ne retrouve pas une photo par lieu sur une carte muette.
- **Une police sous licence permissive** (MIT, Apache) : il n'en existe presque pas qui couvrent les écritures d'une carte (latin, grec, cyrillique) ; le style Protomaps est fait pour Noto.
- **Charger les polices depuis un CDN** : contraire à la règle « rien ne sort de la box ».
