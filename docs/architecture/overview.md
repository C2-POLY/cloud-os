# Vue d'ensemble

La pile de Cloud OS et la façon dont ses services se parlent. Les choix de langages et leurs raisons sont dans [`docs/decisions/001-stack.md`](../decisions/001-stack.md).
Issue : #3.

## Services

```mermaid
flowchart LR
    subgraph Clients
        W[Navigateur]
        M[App mobile, plus tard]
    end
    subgraph Box["Box (Docker Compose)"]
        C["core (Go)<br/>API, comptes, stockage,<br/>front compilé"]
        K["worker (Python)<br/>tâches de fond, IA"]
        DB[("Postgres + pgvector")]
        D[/"données : CLOUDOS_DATA_DIR<br/>monté en /data"/]
    end
    W -- "HTTP /api, cookie de session" --> C
    M -. "HTTP /api, Bearer" .-> C
    C -- "migrations, index, sessions" --> DB
    K -- "battement, plus tard file de tâches" --> DB
    C -- "seul à écrire" --> D
```

- **Les clients ne parlent qu'au cœur.** Le cœur sert aussi le front compilé (`STATIC_DIR`). En développement, Vite le sert avec rechargement à chaud et renvoie `/api` vers le cœur.
- **Le cœur et les workers ne se parlent que par Postgres** : battement aujourd'hui, file de tâches demain (#8). Un worker n'écrit jamais dans les fichiers des utilisateurs.
- **Seul le cœur applique les migrations**, au démarrage, sous un verrou Postgres (goose). Un worker attend que les tables dont il a besoin existent.

| Service | Image | Port hôte (défaut) |
|---|---|---|
| `db` | `pgvector/pgvector:0.8.1-pg17` | 54320 |
| `core` | `core/Dockerfile` (front compilé + binaire Go, distroless, non root) | 8480 |
| `worker` | `worker/Dockerfile` (Python 3.13, uv) | aucun |
| `web` | `node:24-alpine`, Vite en développement | 5180 |

Tous les ports sont liés à `127.0.0.1` et réglables (`CLOUDOS_*_PORT`), pour qu'une pile par worktree tourne sans conflit.

## Démarrage du cœur

```mermaid
sequenceDiagram
    participant C as core
    participant DB as Postgres
    participant FS as /data
    C->>DB: connexion
    C->>DB: goose up (verrou), migrations embarquées
    C->>FS: CleanTmp : écritures interrompues
    C->>DB: RecoverMoves : déplacements interrompus
    C->>C: écoute sur :8080
```

Les sous-commandes `core import`, `core verify` et `core reset-password` passent par les mêmes deux premières étapes, puis font leur travail sans démarrer le serveur. Elles ne nettoient rien, parce qu'un serveur peut tourner à côté.

## Santé

`GET /api/health` est public. Il renvoie la version (fichier `VERSION`), l'état de la base, la version de migration et les workers vus depuis moins de 30 s. Si la base ne répond pas, il renvoie 503 avec `db: down`.

```mermaid
sequenceDiagram
    participant W as web
    participant C as core
    participant DB as Postgres
    participant K as worker
    loop toutes les 10 s
        K->>DB: UPSERT worker_heartbeat(worker_id, now())
    end
    W->>C: GET /api/health (toutes les 5 s)
    C->>DB: SELECT 1, version goose, workers vus depuis < 30 s
    alt base joignable
        C-->>W: 200 {version, db: ok, migration, workers}
    else base injoignable
        C-->>W: 503 {version, db: down}
    end
```

Le worker reprend tout seul quand la base revient : il se reconnecte toutes les 2 s.
