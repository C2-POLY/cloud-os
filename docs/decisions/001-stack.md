# 001 — Stack de la V1

- Date : 2026-11-17
- Statut : acceptée
- Issue : #1

## Décision

| Partie | Choix |
|---|---|
| Cœur (comptes, stockage, index, API, WebDAV, partage, file de tâches) | Go |
| Workers d'IA (embeddings texte et image, OCR, Whisper, classement) | Python, modèles exécutés avec ONNX Runtime, sans PyTorch dans l'image |
| File de tâches | Une table PostgreSQL (`SELECT … FOR UPDATE SKIP LOCKED`), pas de Redis |
| Base de données | PostgreSQL avec pgvector |
| LLM | Ollama ou llama.cpp, derrière l'interface de fournisseur d'IA |
| Front web | React avec Vite, en TypeScript, servi en statique par le cœur |
| Mobile | React Native, en TypeScript |
| Déploiement | Docker Compose, images ARM64 et AMD64 |

## Pourquoi

**Le langage de l'IA ne dépend pas de celui du cœur.** Le PRD fait tourner l'IA à part, en tâche de fond : les workers lisent la file de tâches et écrivent leurs résultats en base. On choisit donc chaque langage pour son rôle.

**Cœur en Go.** Le cœur fait surtout des entrées-sorties : uploads en parallèle, lecture de fichiers, WebDAV, hachage. Go produit un seul binaire léger en RAM, ce qui compte sur un Raspberry Pi, gère bien la concurrence, et a déjà un serveur WebDAV (`golang.org/x/net/webdav`).

**IA en Python.** Le calcul d'un modèle se fait de toute façon en C++ ou sur le GPU : la lenteur de Python ne pèse presque pas. Ce qui compte, c'est l'écosystème : chaque modèle sort d'abord en Python, avec sa préparation des entrées (tokenizer, redimensionnement et normalisation des images). En Go, il faudrait la recoder à chaque modèle, et une erreur à ce niveau donne de mauvais vecteurs sans aucune erreur visible. Le PRD prévoit de changer de modèle : Python rend ce changement rapide. ONNX Runtime plutôt que PyTorch garde des images Docker et une RAM raisonnables, comme le fait le service de machine learning d'Immich.

**File de tâches dans Postgres.** Un composant de moins à installer, surveiller et sauvegarder sur la box ; les tâches partagent la transaction et la sauvegarde des données qu'elles traitent.

**React et React Native.** Un seul langage, TypeScript, pour le web et le mobile, et des types d'API partagés entre les deux.

## Options écartées

- **Tout en Go** : l'IA passerait par ONNX Runtime via cgo ou par Ollama en HTTP. Correct pour le LLM et les embeddings texte, mais chaque modèle d'image, d'OCR ou d'audio demanderait un portage de sa préparation des entrées.
- **Tout en Python** (FastAPI et workers) : un seul langage et un démarrage plus simple, mais un cœur plus lourd en RAM et moins à l'aise avec beaucoup d'entrées-sorties parallèles.
- **Cœur en TypeScript** (comme Immich) : un langage partagé avec le front et le mobile, mais un cœur plus lourd que Go sur un Pi.
- **Redis pour la file de tâches** : plus de débit qu'on n'en a besoin, et un service de plus à faire tourner et sauvegarder.
- **Flutter pour le mobile** : écarté pour garder TypeScript partout côté client.

## Conséquences

- Le premier sujet de code met en place la pile Docker Compose (cœur Go, worker Python, Postgres avec pgvector, front) et ajoute ses commandes de build, de test et de lint à `CLAUDE.md`, section « Commands », puis aux étapes 5 à 7 de la skill `topic`.
- Le contrat entre le cœur et les workers est la table de la file de tâches et le schéma des résultats en base. Il se décrit dans le design de l'issue qui le crée.
- Les licences des modèles se vérifient avant chaque intégration (PRD, « Risques »).
