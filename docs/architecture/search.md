# Recherche

Trouver un fichier ou un dossier sans savoir où il est rangé : par nom et par chemin. La recherche par le sens (phase 3) est dans [search-ai.md](search-ai.md).
Issue : #48 (Feature #45).

## Cœur

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

## Web : la palette Demander

`web/src/desktop/Palette.tsx` (et `search.ts` pour les groupes) :

- **Ouvrir :** ⌘K ou Ctrl+K depuis n'importe quel écran (le même raccourci la referme), le bouton Demander du dock, ou la pilule de la barre de menus (masquée sur téléphone).
- **Pendant la frappe :** la requête part 150 ms après la dernière touche. Une réponse plus ancienne qui arrive en retard est ignorée.
- **Résultats :** groupés en Dossiers, Photos et vidéos, puis Documents. Les flèches les parcourent dans l'ordre affiché, en bouclant.
- **Aperçu :** la miniature (#14), sinon l'icône, avec le dossier, la taille et la date. Masqué sur téléphone.
- **Actions :** Entrée ou un clic ouvre le dossier (ou le dossier du fichier) dans Fichiers. Télécharger récupère le fichier. Échap ou un clic à côté ferme la palette.

```mermaid
sequenceDiagram
    participant U as Clavier
    participant P as Palette
    participant C as Cœur
    participant DB as Postgres
    U->>P: ⌘K, puis « ete »
    P->>P: 150 ms sans frappe
    P->>C: GET /api/search?q=ete&limit=30
    C->>DB: cloudos_fold(path) LIKE '%ete%' ESCAPE, par mot, trash_id IS NULL
    DB-->>C: lignes classées (nom au début, nom, chemin ; récent d'abord)
    C-->>P: Entry[]
    P->>P: groupes, sélection, aperçu
    U->>P: Entrée
    P->>P: navigate(/drive/<dossier>) : la fenêtre Fichiers passe devant
```
