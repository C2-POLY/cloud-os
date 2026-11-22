# 007 — Le code est open source, sous MIT ou Apache-2.0

- Date : 2026-11-22
- Statut : acceptée
- Issue : #113

## Décision

**Le code de Cloud OS est open source, au choix de celui qui le reprend sous la licence MIT ou sous la licence Apache 2.0** (« MIT OR Apache-2.0 »), comme celui de Fourchette. Les deux textes sont à la racine du dépôt : `LICENSE-MIT` et `LICENSE-APACHE`. La question « open source ou fermé » du PRD est tranchée.

## Pourquoi

- **La règle des dépendances le préparait.** Depuis le début, `CLAUDE.md` n'accepte que des dépendances sous licence permissive, pour que le projet puisse sortir en open source (ADR 002, 003, 004 et 005). Rien dans la box n'empêche cette sortie.
- **Les deux licences se complètent.** Apache 2.0 donne une licence de brevet explicite et dit comment traiter les contributions. MIT est courte et compatible avec plus de licences, dont la GPL v2, avec laquelle Apache 2.0 ne l'est pas. Offrir les deux laisse chacun prendre celle qui lui convient, sans rien retirer à l'autre.
- **La même licence que Fourchette**, l'autre projet de C2-POLY : une seule règle pour les deux dépôts.

## Options écartées

- **Rester fermé** : écarté par le dev le 2026-11-22, en même temps que la décision de publier le site (#113) depuis un dépôt public.
- **Une licence copyleft** (GPL, AGPL) : elle obligerait chaque reprise à rester sous la même licence et fermerait les usages commerciaux que le PRD laisse ouverts.

## Conséquences

- **Le dépôt devient public**, avec tout son historique, ses issues et ses PR, après un contrôle avant publication et l'accord du dev.
- **Les composants tiers gardent leur licence** : LGPL (ADR 002) et polices sous OFL (ADR 004), avec leurs avis.
- **Une contribution** proposée au projet est sous les deux mêmes licences, sauf mention contraire (Apache 2.0, section 5).
