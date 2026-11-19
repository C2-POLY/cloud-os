# Recherche

Trouver un fichier ou un dossier sans savoir où il est rangé : par nom et par chemin, et par le texte des documents. La recherche par le sens est dans [search-ai.md](search-ai.md) pour les documents (#70) et [embeddings.md](embeddings.md) pour les photos (#71). Demander réunit les quatre en une seule liste (#72, [plus bas](#une-seule-liste--get-apisearchall)).
Issues : #48 (Feature #45) pour les noms, #69 (Feature #65) pour le texte, #72 (Feature #65) pour la liste fusionnée.

## Par nom : `GET /api/search`

`GET /api/search?q=&limit=` (session requise) appelle `storage.Store.Search`, qui cherche dans les fichiers et dossiers vivants (`trash_id IS NULL`) du compte connecté. Le propriétaire est toujours celui de la session, jamais un paramètre.

- **Accents et casse ignorés :** la migration 0012 ajoute `cloudos_fold(text)` = `lower(unaccent(…))`. « ete » trouve « Été 2024 », « BAIL » trouve « bail.pdf ».
- **Mots :** chaque mot de `q` (8 au plus) doit se trouver dans le chemin, dans n'importe quel ordre. Les caractères `%` et `_` sont cherchés tels quels (`escapeLike`).
- **Classement :**
  1. le nom qui commence par le premier mot ;
  2. le nom qui le contient ;
  3. le reste (un mot trouvé seulement dans le dossier).
  
  À rang égal, le plus récemment modifié passe d'abord. 30 résultats par défaut, 100 au plus.
- **Index :** GIN trigramme (`pg_trgm`) sur `cloudos_fold(path)` pour les lignes vivantes (`files_path_fold`). Une condition par mot, pour que chacune puisse s'en servir. Sur une petite table, Postgres préfère l'index du propriétaire ; sur une grande, l'index trigramme.
- **Pourquoi une fonction à nous :** `unaccent()` n'est que STABLE, car son dictionnaire pourrait changer, et un index exige une fonction IMMUTABLE. Nommer le dictionnaire (`'public.unaccent'::regdictionary`) permet de déclarer l'enveloppe IMMUTABLE.
- **Licences :** `unaccent` et `pg_trgm` sont livrées avec PostgreSQL (licence PostgreSQL).

## Dans le texte : `GET /api/search/content`

`GET /api/search/content?q=&limit=` (session requise) appelle `storage.Store.SearchContent`, qui cherche dans le texte que les workers ont extrait des documents (table `document_text`, [documents.md](documents.md)) : PDF, Office, OpenDocument, textes, et l'OCR quand il y sera. Seuls comptent les fichiers vivants du compte connecté. La route est à part de celle des noms : la recherche par nom garde sa forme et sa vitesse, et Demander lance les deux ensemble.

**Index (migration 0018).**

- **`cloudos_fr` :** une copie de la configuration `french` où les mots non ASCII passent par `unaccent` avant le radical français (Snowball). « contrats », « CONTRAT » et « contrat » donnent `contrat` ; « Électricité » et « electricite » donnent `electricit` ; « œufs » donne `oeuf`. Les mots vides (« le », « de ») sont ignorés.
- **`document_text.tsv` :** une colonne `tsvector` générée (`GENERATED ALWAYS AS (cloudos_doc_tsv(text)) STORED`), calculée quand le worker écrit la ligne, sans code de plus dans le worker, et servie par un index GIN (`document_text_tsv`). La migration la calcule pour les lignes déjà là.
- **`cloudos_doc_tsv(text)` :** un `tsvector` ne tient que 1 Mo de lexèmes et de positions. Un tableur d'un million de caractères fait de nombres tous différents le dépasse (1,4 Mo), et l'erreur ferait échouer l'`INSERT` du worker, qui perdrait aussi le texte. Dans ce seul cas, la fonction indexe les 100 000 premiers caractères. Un texte d'un million de caractères fait de mots tient en entier (305 Ko).

**Requête.**

- **Syntaxe :** `websearch_to_tsquery('public.cloudos_fr', q)`, sur les 8 premiers mots. Tous les mots doivent y être, dans n'importe quel ordre ; `"contrat de location"` cherche la phrase, `-vente` exclut un mot, `or` accepte l'un ou l'autre. Une requête sans mot à chercher (que des mots vides, ou que des exclusions) ne trouve rien : sans mot, elle trouverait tous les documents qui n'ont pas le mot exclu.
- **Filtre :** l'index GIN donne les contenus qui ont les mots, puis un `EXISTS` garde ceux dont le compte a un fichier vivant (`owner = $1 AND kind = 'file' AND trash_id IS NULL`, index `files_sha256`). Deux comptes qui ont le même contenu partagent sa ligne de `document_text`, mais chacun ne voit que ses fichiers. Un document mis à la corbeille disparaît ; restauré, il revient (sa ligne reste tant qu'un fichier, même à la corbeille, a son empreinte).
- **Classement :** `ts_rank_cd(tsv, query, 2)` : les mots proches les uns des autres comptent plus, et le rang est divisé par la longueur du document, pour qu'un relevé de 20 pages qui cite une fois « notaire » ne passe pas devant l'acte qui en parle. À rang égal, le plus récemment modifié passe d'abord. 20 résultats par défaut, 100 au plus.
- **Plusieurs fichiers, un contenu :** les contenus sont classés et limités d'abord, puis chaque fichier vivant du compte qui a ce contenu est une ligne du résultat, avec le même extrait.

**Extrait.**

- `ts_headline` donne deux fragments d'une quinzaine de mots, séparés par « … », les mots trouvés entre `\x01` et `\x02`. Le cœur les découpe en morceaux `{text, hit}` (`splitSnippet`) et replie les blancs (sauts de ligne et de page) en une espace. Un mot court (trois lettres au plus) en bord de fragment est retiré, et la ponctuation finale aussi.
- **Une fenêtre, pas tout le texte :** `ts_headline` analyse tout ce qu'on lui donne, 40 à 100 ms pour un document de 460 000 caractères, 850 ms pour 20. Le cœur cherche donc la première occurrence avec `regexp_instr` et une expression construite à partir des lexèmes de la requête (`firstWordPattern`) : chaque voyelle accentuée ou non, `œ` et `æ`, un mot qui commence par le radical suivi de 8 lettres au plus, la casse ignorée. `ts_headline` lit alors 2 400 caractères autour d'elle. Si l'expression ne trouve rien, ou si la fenêtre n'a aucun mot marqué (un radical qui ne commence pas son mot : « chevaux » donne `cheval`), il lit tout le texte.
- **PostgreSQL 17.8 :** `left()` et `substr()` sur un texte stocké et compressé peuvent échouer (« invalid byte sequence for encoding "UTF8" ») quand la tranche décompressée coupe un caractère accentué : une régression de 17.8, corrigée en 17.9, reproduite sur l'image locale. La fenêtre et `cloudos_doc_tsv` découpent donc une copie entière du texte (`text || ''`), jamais le texte stocké.

**Réponse.** `ContentHit[]` : un `Entry` comme celui de `/api/search`, plus `snippet`, la liste des morceaux de l'extrait (`{"text": "contrat", "hit": true}`, `{"text": " de location"}`). Le front affiche ces morceaux en nœuds texte : rien du document n'est lu comme du HTML.

**Vitesse.** Mesurée sur un corpus synthétique de 10 000 documents (211 millions de caractères, dont 100 documents de 460 000 caractères), sur le Mac de dev, base chaude : de 8 à 48 ms par recherche, pour un mot rare (50 documents) comme pour un mot présent dans 3 333 documents. Index GIN de 21 Mo ; la colonne `tsv` pèse à peu près la taille du texte compressé ; la calculer pour les 10 000 documents prend environ 100 s (à la migration, une seule fois). Les requêtes passent par un CTE : leur plan ne dépend pas des mots cherchés, et reste bon quand pgx les prépare.

**Pas encore :** le préfixe pendant la frappe (« fact » ne trouve pas « facture » dans le texte ; la recherche par nom, elle, le trouve).

## Une seule liste : `GET /api/search/all`

`GET /api/search/all?q=&limit=&type=&from=&to=&tz=` (session requise) : `core/internal/httpapi/search.go`, puis `search.Searcher.Search` (`core/internal/search`). Demander n'appelle plus que cette route ; les quatre autres restent.

```mermaid
sequenceDiagram
    autonumber
    participant P as Palette Demander
    participant H as cœur : GET /api/search/all
    participant S as search.Searcher
    participant DB as Postgres
    participant E as worker : POST /embed

    P->>P: 150 ms sans frappe, ou un filtre changé
    P->>H: GET /api/search/all?q=chien à la plage&limit=30 (&type=photos&from=…&to=…&tz=…)
    H->>S: Search(owner de la session, Query{Q, Filter, Limit})
    par noms
        S->>DB: Store.SearchNames : trigramme, Filter, Tier du nom
    and mots
        S->>DB: Store.SearchContent : GIN tsv, Filter, extrait
    and sens des documents, 800 ms au plus
        S->>E: arctic-embed-m-v2.0, « query: … »
        E-->>S: vecteur
        S->>DB: textemb.Searcher.Search : HNSW halfvec, Filter dans l'EXISTS
    and sens des photos, 800 ms au plus, sauf type=docs
        S->>E: siglip2-base-patch16-224, « une photo de … »
        E-->>S: vecteur
        S->>DB: photovec.Searcher.Search : HNSW, MinScore, Filter dans l'EXISTS
    end
    alt worker arrêté, sans modèle, ou plus lent que DefaultBudget
        S->>S: fuse(noms, mots), Partial = true
    else
        S->>S: fuse(noms, mots, sens, photos) : confiances, 1 − Π(1 − c), seuil
    end
    S-->>H: limit résultats, du meilleur au moins bon
    H-->>P: {results: SearchHit[], partial}
    P->>P: une liste, aperçu du sélectionné, flèches, Entrée, Échap
```

**Les quatre recherches** partent en même temps, avec le même filtre et au moins 40 résultats chacune (`candidates`) : par nom (`Store.SearchNames`, qui rend aussi le rang du nom, `Tier`), dans le texte (`Store.SearchContent`), par le sens des documents (`textemb`) et des photos (`photovec`). Une requête vide ne demande rien à personne.

**Le budget.** Les deux recherches par le sens ont 800 ms en tout (`DefaultBudget`), du vecteur demandé au worker jusqu'au parcours de l'index. Passé ce délai, ou si le worker est arrêté ou n'a pas le modèle, la réponse part avec les noms et les mots, et `partial: true` ; Demander l'écrit en une ligne. Une erreur des noms ou des mots fait échouer la route ; une erreur du sens la rend seulement partielle, et s'écrit dans le journal (jamais la requête).

**La fusion : des scores calibrés, pas RRF.** Les cosinus des deux modèles ne se comparent pas tels quels : un bon document fait 0,27 à 0,68 avec Arctic, une bonne photo 0,09 à 0,14 avec SigLIP 2, qui donne jusqu'à 0,10 à une photo sans rapport. Chaque recherche donne donc, pour chaque fichier trouvé, une confiance entre 0 et 1 :

| Recherche | Confiance |
|---|---|
| Nom (`confNameStart`, `confName`, `confPath`) | 0,9 si le nom commence par le premier mot, 0,8 s'il le contient, 0,6 si seul un dossier du chemin le contient |
| Mots (`confText`) | 0,4 : le texte a tous les mots |
| Sens d'un document (`docScale`) | `(cos − 0,15) / 0,30`, bornée à [0, 1] |
| Sens d'une photo (`photoScale`) | `(cos − 0,065) / 0,055`, bornée à [0, 1] |

- **Le score d'un fichier** est `1 − Π(1 − c)` sur les recherches qui l'ont trouvé : une recherche sûre suffit, deux qui s'accordent se renforcent. Il classe la liste d'une requête et ne se compare pas d'une requête à l'autre.
- **Le seuil** (`keepAbs`, `keepRel`) : un fichier trouvé seulement par le sens reste si son score atteint `max(0,2 ; 0,3 × le meilleur score de la requête)`. Le nom et les mots ne sont jamais coupés. Pour « contrat de l'appart », le bail fait 1 et les photos les plus proches 0,2 : elles partent. Une requête vague, où rien n'est sûr, garde sa liste.
- **À score égal**, le fichier le mieux placé dans l'une des listes passe d'abord, puis le plus récent, puis le chemin.
- **Pourquoi pas RRF** : la fusion par rangs réciproques donne le même poids au premier de chaque liste, quel que soit son score. Le premier document par le sens est donc ex aequo avec la première photo, même pour « un chat qui dort » : simulée sur les réponses des quatre routes, elle ne met la bonne photo en premier que pour 1 requête de photo sur 15. La pondérer par le score demanderait la même calibration.
- **Les constantes** viennent du banc d'essai (30 documents, 17 photos, 52 requêtes, `worker/tests/fixtures/bench`). Dans une grille de 2 916 jeux de constantes autour d'elles, 1 582 gardent 32 sur 33, 15 sur 15, les chiens en tête et aucune photo devant le bon document : elles ne tiennent pas à un cheveu. À revoir avec le matériel de référence, et si un modèle change.

**Réponse.** `SearchAnswer` (`shared/api-types.ts`) : `results`, des `SearchHit` (l'entrée du fichier, son `score`, `found_by` parmi `name`, `text`, `meaning`, `image`, l'extrait des mots `snippet` ou, à défaut, le passage le plus proche par le sens `excerpt`), et `partial`. 30 résultats par défaut, 100 au plus.

**Filtres** (`storage.Filter`, `core/internal/storage/filter.go`), appliqués dans le SQL de chaque recherche et non après la fusion, pour qu'un filtre étroit ne vide pas la liste :

- `type=docs` ou `type=photos`, par type MIME comme la frise : images et vidéos d'un côté, le reste de l'autre. Avec `type=docs`, la recherche des photos ne part pas. Un autre type répond 400.
- `from` et `to`, des jours inclus (`2025-12-31`), sur la date de la frise : la prise de vue (`media_meta.taken_at`), sinon la modification, au fuseau `tz` du navigateur (UTC s'il manque ou si la box ne le connaît pas). Une date invalide, ou `to` avant `from`, répond 400.
- Avec un filtre, les dossiers ne sont pas listés.
- Dans les recherches par le sens, le filtre est dans l'`EXISTS` que l'index HNSW évalue pendant son parcours (`iterative_scan`) : un filtre étroit ne vide pas la liste.

**Le banc d'essai** (`core/internal/search/bench_test.go`) passe par la route, sur les vecteurs que les modèles du worker font du banc : `testdata/bench.json.gz`, écrit par `worker/bench/golden.py` dans l'image du worker, avec les fonctions des tâches (découpage, encodeurs, gabarits des requêtes). Le test Go n'a pas de modèle ; `worker/tests/test_golden.py` échoue quand les versions des modèles, le découpage ou le corpus ne sont plus ceux du fichier.

- **Résultats** : la bonne réponse arrive en premier pour 32 requêtes de document sur 33 et pour les 15 requêtes de photo.
  - La manquée, « montant du dépôt de garantie », l'est déjà par Arctic seul ([search-ai.md](search-ai.md)).
  - Les trois photos de chien à la plage sont en tête de « chien à la plage », et aucune photo ne passe devant le bon document.
  - Sur les 33 requêtes de document, 9 photos sont listées en tout, après le document ; sur les 15 requêtes de photo, 5 documents, après la photo.
  - « chien », « plage », « voiture » et « vacances au bord de la mer » mêlent photos et documents dans leurs 8 premiers.
  - Sans le worker, 18 requêtes de document sur 33 trouvent encore le leur en premier, par les noms et les mots.
- **Les vecteurs se font sous Linux**, comme sur la box. Sous macOS, l'export int8 de la partie texte de SigLIP 2 donne d'autres vecteurs, qui notent jusqu'à 0,02 plus haut des photos sans rapport : avec eux, des photos passaient le seuil sous des requêtes de document.

**Vitesse.** Mesurée par `TestSpeedOn10000Files` (à part : `CLOUDOS_SEARCH_BENCH=1`) sur le Mac de dev, sur 10 000 fichiers synthétiques :

- **Le fonds** : 6 000 photos, dont un tiers avec une date de prise de vue, et 4 000 documents de 300 à 900 mots du vocabulaire du banc, d'une à trois fenêtres chacun. Les vecteurs sont aléatoires, et un faux worker répond après un délai.
- **Avec un worker à 20 ms** (Arctic encode une requête en 16 ms sur le Mac) : de 30 à 60 ms par recherche au 50e centile, 80 ms au plus au 95e, filtres compris.
- **Avec un worker à 100 ms** (estimé sur un Raspberry Pi 5) : de 114 à 157 ms au 95e centile.

## Web : la palette Demander

`web/src/desktop/Palette.tsx` (et `search.ts` pour les filtres) :

- **Ouvrir :** ⌘K ou Ctrl+K depuis n'importe quel écran (le même raccourci la referme), le bouton Demander du dock, ou la pilule de la barre de menus (masquée sur téléphone).
- **Pendant la frappe :** 150 ms après la dernière touche ou le dernier filtre changé, une seule requête part, `GET /api/search/all` (`searchAll`). Une réponse plus ancienne qui arrive en retard est ignorée.
- **Résultats :** une seule liste, dans l'ordre du cœur, documents, photos et dossiers mêlés, sans groupes. Une photo ou une vidéo a sa vignette dans sa ligne. Les flèches parcourent la liste en bouclant.
- **Filtres :** sous le champ, « Tout », « Documents », « Photos », et une période : « Toutes les dates », « 30 derniers jours », « Cette année », « L'an dernier », « Plus ancien » (`filtersOf`, comptée depuis aujourd'hui sur l'horloge du navigateur, envoyée avec son fuseau). Choisir un type rend la main au champ.
- **Extrait :** deux lignes sous le nom : les mots trouvés en `<mark>`, ou à défaut le passage le plus proche par le sens. Rien du document n'est lu comme du HTML.
- **Messages :** « Rien trouvé » (« avec ces filtres » s'il y en a), l'erreur de la route, et une ligne quand la recherche par le sens ne répond pas : les noms et les mots restent.
- **Aperçu :** la miniature (#14), le PDF ou le début du texte (#52), sinon l'icône ; l'extrait ; le dossier, la taille, la date, et par quoi le fichier a été trouvé (« le nom, le sens du texte »). Masqué sur téléphone.
- **Actions :** Entrée ou un clic ouvre le dossier (ou le dossier du fichier) dans Fichiers. Télécharger récupère le fichier. Échap ou un clic à côté ferme la palette.
