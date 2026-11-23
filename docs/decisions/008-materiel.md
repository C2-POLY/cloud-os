# 008 — Une configuration requise publiée, et le Raspberry Pi 5 comme machine de référence

- Date : 2026-11-20
- Statut : acceptée
- Issue : #103

## Décision

**Cloud OS publie une configuration requise, minimale et conseillée, et la teste sur une machine de référence : le Raspberry Pi 5.** Un client choisit sa machine ; si elle suit la configuration requise, la box doit y tourner comme sur le Pi 5. La question du PRD, « Raspberry Pi 5 ou mini PC x86 », est tranchée ainsi.

La configuration requise est dans [`docs/install.md`](../install.md). Ses valeurs restent provisoires tant qu'elles n'ont pas été mesurées sur le Pi 5 avec `scripts/measure` : la mémoire au plus haut de chaque service, l'indexation de 1 000 photos et 100 PDF, et l'interface qui reste fluide pendant l'indexation (p95 sous 500 ms).

## Pourquoi

- **Les images existent déjà pour les deux architectures.** Le cœur, le worker et la sauvegarde sont construits en arm64 et en amd64, et la CI `images` démarre la pile de production sur les deux. Imposer une seule machine n'apporterait rien au code.
- **Une machine de référence rend les chiffres vérifiables.** « 4 Go de mémoire suffisent » ne vaut que mesuré quelque part. Le Pi 5 est la machine la plus contrainte que l'on vise : ce qui y tient tient aussi sur un mini PC x86 plus puissant.
- **Le Pi 5 est le choix le plus probable d'un foyer** : bon marché, silencieux, peu gourmand, avec un SSD NVMe ou USB 3. Le guide d'installation est joué sur lui.

## Options écartées

- **Imposer une seule machine** : chaque client devrait acheter la même, alors que la pile tourne déjà ailleurs. Un fournisseur unique serait aussi un risque (rupture de stock, fin de série).
- **Le x86 seul** : il écarterait le Pi 5, moins cher et moins gourmand, et ferait perdre l'arm64 déjà construit et testé.

## Conséquences

- **`scripts/install`** installe la box sur une Debian 64 bits, Raspberry Pi OS Lite compris, quelle que soit la machine. Il refuse un système 32 bits et, sans SSD de données, une machine qui démarre sur une carte SD.
- **Le SSD de données est requis sur le Pi 5** dès que le système est sur une carte SD : les données ne vont jamais sur la carte.
- **Les mesures se prennent sur le Pi 5**, dans une box jetable (`scripts/measure`), puis remplissent le tableau de `docs/install.md`. Une nouvelle version qui change la mémoire ou l'indexation se mesure de nouveau.
- **Une image à flasher**, les mises à jour A/B et l'assistant de premier démarrage restent pour la phase 5 du PRD.
