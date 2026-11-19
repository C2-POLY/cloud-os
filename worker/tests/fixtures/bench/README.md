# Corpus du banc d'essai des modèles

Le banc d'essai de `worker/bench` (#66, `docs/decisions/005-modeles-locaux.md`) classe les modèles locaux sur ce corpus. Les tests du worker (`tests/test_models.py`) s'en servent aussi.

- `docs/` : 30 documents d'un foyer, en français, tels que l'extraction de texte les rendrait : bail, quittance, factures, avis d'impôt, ordonnances, relevés bancaires… **Tous sont fictifs** : les personnes, les sociétés, les adresses et les numéros sont inventés. Certains sont des voisins trompeurs exprès (la quittance, l'état des lieux, l'assurance habitation et la location de vacances parlent aussi de l'appartement).
- `photos/` : 17 photos sous CC0, avec leur provenance dans `photos/PROVENANCE.md`.
- `queries.toml` : les requêtes, tapées comme un membre du foyer les taperait, et les réponses attendues ; les requêtes mixtes (`[[mixed]]`) attendent photos et documents dans la liste de Demander (#72).
- `categories.toml` : la catégorie attendue de chaque document (#73, `worker/src/worker/doccat.py`), avec une seconde quand un document est honnêtement les deux.

Le test Go de cette liste (`core/internal/search/bench_test.go`) lit les vecteurs que les modèles font de ce corpus dans `core/internal/search/testdata/bench.json.gz`, écrit par `worker/bench/golden.py` : à refaire quand le corpus ou les requêtes changent (`tests/test_golden.py` le rappelle).
