# cloud-os (nom de travail)

OS cloud tout-en-un, installé sur une box à la maison, qui remplace Drive, Photos et le NAS avec un seul cœur et une recherche IA locale.

- PRD : https://claude.ai/artifact/Ga3px6XWQWViJftYeD8pcR
- V1 : phases 1 à 3 (Cœur + Drive, Photos + mobile, Recherche IA + Documents) en Docker Compose ; l'emballage OS vient après.

## Stack

Cœur en Go, workers d'IA en Python (ONNX Runtime), PostgreSQL avec pgvector, front React (Vite), mobile React Native. Le pourquoi : [docs/decisions/001-stack.md](docs/decisions/001-stack.md).

## À trancher

- Nom du produit
- Open source ou fermé
- Matériel de référence : Raspberry Pi 5 ou mini PC x86
