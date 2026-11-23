# cloud-os (nom de travail)

OS cloud tout-en-un, installé sur une box à la maison, qui remplace Drive, Photos et le NAS avec un seul cœur et une recherche IA locale.

- Site : https://c2-poly.github.io/cloud-os/
- PRD : https://claude.ai/artifact/Ga3px6XWQWViJftYeD8pcR
- V1 : phases 1 à 3 (Cœur + Drive, Photos + mobile, Recherche IA + Documents) en Docker Compose ; l'emballage OS vient après.

## Stack

Cœur en Go, workers d'IA en Python (ONNX Runtime), PostgreSQL avec pgvector, front React (Vite), mobile React Native. Le pourquoi : [docs/decisions/001-stack.md](docs/decisions/001-stack.md). Le site public est dans [`site/`](site/) (Astro), à part de la box.

## Matériel

Une configuration requise publiée, testée sur le Raspberry Pi 5, la machine de référence ; l'installation pas à pas : [docs/install.md](docs/install.md). Le pourquoi : [docs/decisions/008-materiel.md](docs/decisions/008-materiel.md).

## À trancher

- Nom du produit

## Licence

Au choix, sous [licence Apache, version 2.0](LICENSE-APACHE) ou sous [licence MIT](LICENSE-MIT). Le pourquoi : [docs/decisions/007-licence.md](docs/decisions/007-licence.md).
