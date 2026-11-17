# 003 — Animations et défilement du front : Motion et Lenis

- Date : 2026-11-17
- Statut : acceptée
- Issue : #31

## Décision

Le front web anime ses transitions avec **Motion** (`motion`, MIT) et fait défiler la page avec **Lenis** (`lenis`, MIT). **GSAP** (avec `@gsap/react`) et les composants **React Bits**, proposés par la skill `front-motion-stack`, sont écartés pour leur licence. La règle de licence de `CLAUDE.md` reste sans exception.

## Licences vérifiées

Vérifiées le 2026-11-17 sur le registre npm et sur les dépôts des projets.

| Paquet | Licence | Ce qu'elle permet | Ce qu'elle interdit |
|---|---|---|---|
| `lenis` 1.3.27 | MIT | Tout, avec la notice | Rien |
| `motion` 14.14.0, et ses dépendances `framer-motion`, `motion-dom`, `motion-utils` | MIT | Tout, avec la notice | Rien |
| `tslib` (dépendance de Motion) | 0BSD | Tout, sans condition | Rien |
| `gsap` 3.15.1, `@gsap/react` 2.1.2 | « GSAP Standard No Charge License » de Webflow, propriétaire et gratuite | Utiliser, reproduire et afficher GSAP dans des sites, des applications web et des interfaces, y compris pour un usage commercial | Les « Prohibited Uses » : un outil visuel d'animation sans code qui concurrence Webflow, sauf accord écrit ; retirer ses mentions. La licence ne dit rien de la modification, de la redistribution ni de l'inclusion dans un projet sous licence libre. Webflow peut l'amender, l'usage continu valant acceptation, et la résilier en exigeant la destruction des copies |
| React Bits (composants copiés dans le projet) | MIT + Commons Clause | Utiliser, modifier et distribuer les composants « as part of an application, website, or product », y compris commercialement | Vendre, sous-licencier ou redistribuer les composants eux-mêmes, seuls, en lot ou portés |

## Pourquoi

**Le projet doit pouvoir devenir open source.** C'est la raison de la règle de licence de `CLAUDE.md`. Ni la licence de GSAP ni Commons Clause ne sont des licences open source au sens de l'OSI : elles restreignent un usage.

- **GSAP** serait une dépendance propriétaire de chaque image de la box. Quiconque reprend le projet hériterait de ses restrictions (un fork qui en ferait un éditeur visuel d'animations serait hors licence), et Webflow peut en changer les termes à tout moment. Les distributions qui trient le logiciel libre la classeraient comme non libre.
- **React Bits** se copie dans le dépôt. Un dépôt public qui les contient les redistribue, et la frontière entre « dans une application » et « les composants eux-mêmes » se lirait fichier par fichier dans chaque fork. Du code sous Commons Clause dans `web/src/` rendrait le dépôt non libre.

**Motion couvre le besoin.** Les animations voulues sont courtes et sobres : une transition de section, l'entrée des lignes d'une liste, l'ouverture et la fermeture d'une fenêtre, le panneau d'envoi. Motion les fait avec `animate()` de `motion/mini`, qui passe par l'API Web Animations du navigateur : environ 4 Ko compressés, contre plus de 30 pour le moteur complet. Lenis en ajoute environ 5. Lenis, lui, n'a pas besoin de GSAP : il tourne avec sa propre boucle (`autoRaf`).

## Options écartées

- **GSAP, `@gsap/react` et Lenis synchronisé avec ScrollTrigger** (la skill `front-motion-stack`) : techniquement le plus complet, écarté pour la licence de GSAP.
- **Les accepter comme exceptions nommées** dans la règle de licence : possible, puisqu'aucune des deux licences n'interdit notre usage aujourd'hui, mais le projet perdrait ce qui fait l'intérêt de la règle. Le dev a demandé la décision la plus juste pour de l'open source.
- **Des composants React Bits réécrits** : réécrire un composant à partir de sa source reste une œuvre dérivée sous la même licence.
- **Des animations CSS seules** : pas de dépendance, mais la fermeture animée d'une fenêtre avant son démontage, le décalage entre lignes et l'arrêt propre au démontage demanderaient d'écrire à la main ce que Motion fait déjà.

## Conséquences

- `web/package.json` dépend de `motion` et `lenis`, en versions exactes. Pas de `gsap`, de `@gsap/react` ni de dossier de composants React Bits.
- La skill `front-motion-stack` ne s'applique pas telle quelle à ce dépôt : ses étapes GSAP et React Bits sont remplacées par Motion.
- Le fonctionnement (ce qui s'anime, où, et le chemin `prefers-reduced-motion`) est décrit dans `docs/architecture/web.md`, section « Animations et défilement ».
