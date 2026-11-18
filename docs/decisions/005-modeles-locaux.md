# 005 — Les modèles d'IA locaux : texte, image, OCR

- Date : 2026-11-18
- Statut : acceptée ; révisée le 2026-11-18 par #70 : Arctic en fp16 au lieu d'int8
- Issue : #66 (Feature #65)

## Décision

Le worker embarque trois modèles, en ONNX, exécutés sur le CPU par ONNX Runtime :

| Usage | Modèle retenu | Licence | Fichiers | Vecteur |
|---|---|---|---|---|
| Sens des documents (embeddings texte) | **Snowflake Arctic Embed M v2.0**, fp16 | Apache-2.0 | 630 Mo | 768 dimensions |
| Contenu des photos (embeddings image–texte) | **SigLIP 2 base/16 à 224 px** (Google), int8 | Apache-2.0 | 412 Mo | 768 dimensions |
| Texte des images et des scans (OCR) | **PP-OCRv5** : détecteur mobile et reconnaissance latine mobile (PaddleOCR) | Apache-2.0 | 13 Mo | — |

Les modèles entrent dans l'image du worker au build, comme ffmpeg (ADR 002) : `worker/models/fetch.sh` les télécharge à une révision figée, vérifie le SHA-256 de chaque fichier et échoue au moindre écart. La box ne télécharge rien à l'exécution. Pas de PyTorch : l'image n'a qu'ONNX Runtime (MIT), `tokenizers` (Apache-2.0) et numpy (BSD).

## Pourquoi

**La justesse d'abord.** Sur le banc d'essai (ci-dessous), Arctic est le seul modèle de texte qui classe en premier le bon document pour 32 des 33 requêtes, dont « contrat de l'appart », qui donne le bail devant l'assurance habitation, la quittance et la location de vacances. SigLIP 2 trouve la bonne photo pour les 15 requêtes, dont « chien à la plage », qui donne les trois photos de chien sur une plage devant le chien dans la neige. PP-OCRv5 lit les pages scannées et photographiées avec 0,1 % d'erreurs de caractères.

**Puis tenir sur un Raspberry Pi 5.** Les trois modèles font 1,04 Go sur le disque. Chargés ensemble, ils occupent 1,5 Go de RAM (mesure ci-dessous). Estimé sur un Pi 5 : environ 1,1 s par document d'une page ou deux, 0,3 s par photo et 3 s par page lue par l'OCR. Une requête est encodée en quelques dizaines de millisecondes, assez vite pour la palette Demander.

**Arctic plutôt que multilingual-e5-small, plus léger.** e5-small manque deux requêtes de plus. Il est deux à trois fois plus rapide et deux fois plus petit, mais ses scores se tassent : 0,87 pour le bail contre 0,86 pour l'assurance auto sur « contrat de l'appart ». Arctic sépare mieux les voisins trompeurs (0,39 contre 0,36). Il lit aussi jusqu'à 8 192 tokens contre 512, ce qui laisse #70 découper les longs documents en moins de morceaux. Sous licence Apache-2.0, il est conçu pour la recherche multilingue, dont le français.

**Arctic en fp16 plutôt qu'en int8 (révision de #70).** La première décision prenait l'export int8, quatre fois plus petit en RAM de calcul et trois fois plus rapide. Sa CI l'a pris en défaut : sur sa machine x86-64, « contrat de l'appart » mettait l'assurance habitation devant le bail. L'export int8 quantifie au vol les activations de chaque produit matriciel, et les noyaux entiers d'ONNX Runtime ne calculent pas pareil d'un processeur à l'autre (AVX2, VNNI, i8mm) ni d'un build à l'autre (ses fusions de graphe). Sous Linux, ses vecteurs sont à 0,96 de cosinus de ceux du fp32, sur arm64 comme sur x86-64, et ils bougent jusqu'à 0,03 entre deux machines. Or le bail ne devance l'assurance que de 0,03. Le fp16 calcule en flottants : 0,99996 du fp32, et les vecteurs d'arm64 et de x86-64 ne diffèrent que de 0,0015. La box pouvant être un mini PC x86 (PRD), la justesse ne doit pas dépendre du processeur. Le détail des mesures est dans [search-ai.md](../architecture/search-ai.md).

**PP-OCRv5 plutôt que Tesseract.** Tesseract, réglé avec le seuillage Sauvola, lit aussi bien les scans et presque aussi bien les photos, deux fois plus vite et avec bien moins de RAM. Mais il tire 71 paquets Debian dans l'image du worker (cairo, pango, glib, gnutls, curl, krb5, ldap, ICU…), dont plusieurs sous LGPL à suivre au titre de l'ADR 002. Tel qu'il est livré, avec le seuillage Otsu, il ne lit presque rien sur une photo éclairée de travers (92 % d'erreurs). PP-OCRv5 n'ajoute que 13 Mo de modèles à ONNX Runtime, déjà là pour les embeddings. Tesseract reste le plan B documenté, si l'OCR des scans doit un jour aller plus vite sur la box.

## Le banc d'essai

`worker/bench` (`python -m bench`, `python -m bench.ocr`), hors de l'image, sur le corpus `worker/tests/fixtures/bench` :

- **30 documents fictifs** d'un foyer, en français : bail, quittance, état des lieux, assurances, factures, avis d'impôt, attestation CAF, ordonnances, relevés, bulletin de paie, carte grise, devis, certificat de scolarité… Certains sont des voisins trompeurs exprès.
- **17 photos sous CC0** (Wikimedia Commons, provenance dans `photos/PROVENANCE.md`).
- **33 requêtes de texte et 15 d'images**, tapées comme un membre du foyer, avec leur réponse attendue (`queries.toml`).
- **L'OCR** lit chaque document rendu en page A4 de deux façons : scan (légère inclinaison, grain, JPEG) et photo de téléphone (perspective, éclairage inégal, flou, résolution plus basse). On compare le texte lu au texte rendu (CER), puis on refait la recherche sur le texte lu.

**Mesures** : recall@1 (la bonne réponse arrive en premier) et MRR (rang moyen inversé), latence par élément encodé un à un comme le fera le worker, et taille des fichiers. La RAM est le pic de mémoire résidente d'un processus qui charge un seul modèle et encode tout le corpus.

**Conditions** : Linux arm64 dans un conteneur (`worker/bench/Dockerfile`, même base que le worker) sur un Mac M4 Pro, ONNX Runtime 1.32 limité à 4 fils comme les 4 cœurs d'un Pi 5. Le même banc sous macOS va jusqu'à deux fois plus vite ; on retient Linux, plus proche de la box.

**Pi 5 estimé** : la mesure Linux multipliée par 6. Le Cortex-A76 à 2,4 GHz tourne deux fois moins vite que les cœurs du M4 Pro. Ses unités vectorielles sont deux fois plus étroites. Il n'a pas les instructions de produit matriciel int8 (i8mm) dont profite ONNX Runtime. C'est une estimation, à remplacer par une mesure quand le matériel de référence sera choisi (PRD : Pi 5 ou mini PC x86).

### Texte

| Modèle (int8 sauf mention) | Licence | recall@1 | MRR | Fichiers | RAM (pic) | ms/document | ms/requête | Pi 5 estimé, par document |
|---|---|---|---|---|---|---|---|---|
| **Arctic Embed M v2.0, fp16** (#70) | Apache-2.0 | **0,97** | **0,97** | 630 Mo | 1 322 Mo | 189 | 16 | 1,1 s |
| Arctic Embed M v2.0 (première décision) | Apache-2.0 | 0,97 | 0,97 | 328 Mo | 850 Mo | 55 | 5 | 0,3 s |
| Arctic Embed M v2.0, fp32 | Apache-2.0 | 0,97 | 0,97 | 1 242 Mo | 2 270 Mo | 148 | 12 | 0,9 s |
| multilingual-e5-small | MIT | 0,94 | 0,96 | 135 Mo | 573 Mo | 18 | 2 | 0,1 s |
| multilingual-e5-small, fp32 | MIT | 0,94 | 0,97 | 487 Mo | 1 181 Mo | 35 | 3 | 0,2 s |
| BGE-M3 | MIT | 0,91 | 0,94 | 587 Mo | 1 158 Mo | 180 | 12 | 1,1 s |
| Qwen3-Embedding-0.6B | Apache-2.0 | 0,82 | 0,87 | 625 Mo | 1 081 Mo | 342 | 30 | 2,1 s |
| multilingual-e5-base | MIT | 0,76 | 0,85 | 296 Mo | 816 Mo | 44 | 4 | 0,3 s |

Les documents font 230 tokens en moyenne. Avec 33 requêtes, une requête manquée retire 0,03 au recall@1. Les lignes fp16 et fp32 d'Arctic sont mesurées par #70 dans les mêmes conditions, avec son découpage en fenêtres de 512 tokens (31 fenêtres pour les 30 documents) et le stockage en demi-précision.

### Image–texte

| Modèle | Licence | recall@1 | MRR | Fichiers | RAM (pic) | ms/photo | ms/requête | Pi 5 estimé, par photo |
|---|---|---|---|---|---|---|---|---|
| **SigLIP 2 base/16 224, int8** | Apache-2.0 | **1,00** | **1,00** | 412 Mo | 856 Mo | 43 | 17 | 0,3 s |
| SigLIP 2 base/16 224, fp32 | Apache-2.0 | 1,00 | 1,00 | 1 536 Mo | 2 406 Mo | 107 | 34 | 0,6 s |
| CLIP multilingue (texte `clip-ViT-B-32-multilingual-v1`, image CLIP ViT-B/32), int8 | Apache-2.0 et MIT | 0,93 | 0,95 | 228 Mo | 444 Mo | 15 | 2 | 0,1 s |
| CLIP multilingue, fp32 | Apache-2.0 et MIT | 0,93 | 0,97 | 894 Mo | 1 386 Mo | 32 | 3 | 0,2 s |

La latence par photo comprend le décodage du JPEG et le redimensionnement.

### OCR

| Moteur | Licence | CER scan | CER photo | s/page (scan) | Pi 5 estimé, par page | Modèles | RAM (pic) | recall@1 après OCR (scan / photo) |
|---|---|---|---|---|---|---|---|---|
| **PP-OCRv5, détecteur mobile + reconnaissance latine mobile** | Apache-2.0 | **0,1 %** | **0,1 %** | 0,52 | 3 s | 13 Mo | 599 Mo | 0,97 / 0,97 |
| PP-OCRv6 small (multilingue) | Apache-2.0 | 0,2 % | 0,2 % | 0,83 | 5 s | 31 Mo | 601 Mo | 0,97 / 0,97 |
| PP-OCRv6 tiny | Apache-2.0 | 1,0 % | 1,0 % | 0,30 | 1,8 s | 6 Mo | 457 Mo | 0,97 / 0,97 |
| Tesseract 5.5 `fra`, seuillage Sauvola | Apache-2.0 | 0,2 % | 0,4 % | 0,28 | 1,7 s | 16 Mo + 71 paquets | 78 Mo | 0,97 / 0,97 |
| Tesseract 5.5 `fra`, réglage par défaut | Apache-2.0 | 0,2 % | 92 % | 0,29 | 1,7 s | 16 Mo + 71 paquets | 78 Mo | 0,97 / 0,24 |

Les modèles PP-OCR tournent ici avec le pipeline de RapidOCR (OpenCV compris, d'où sa RAM), dans le banc d'essai seulement. Le worker n'a pas OpenCV : `worker.models.OcrModel` fait tourner les deux modèles avec numpy et Pillow, et lit la même ligne de test, accents et « € » compris. La recherche après OCR utilise Arctic.

### Le worker avec ses trois modèles

Mesuré dans l'image du worker, sans réseau (`docker run --network none`) : les imports seuls font 68 Mo. Arctic ajoute 650 Mo en int8, 990 Mo en fp16 (#70), et SigLIP 2 513 Mo. Leurs tokenizers en comptent 280 chacun, pour des vocabulaires de 250 000 et 256 000 tokens : c'est la première piste si la RAM manque. PP-OCRv5 n'ajoute presque rien. Les trois modèles chargés en 2,4 s occupent 1,2 Go avec Arctic en int8, 1,5 Go en fp16. Après quelques documents, photos et une page lus, le pic atteignait 1,8 Go en int8, dont la mémoire de travail qu'ONNX Runtime garde d'un appel à l'autre ; en fp16, il faut compter 340 Mo de plus. Sur un Pi 5 de 8 Go, il reste de la place pour Postgres et le cœur. Sur un Pi 5 de 4 Go, #70 et #71 devront charger un modèle à la fois. L'image du worker grossit de 1,04 Go.

## Ce que ça impose

- **Rien n'est téléchargé à l'exécution.** Un fichier absent est une erreur (`FileNotFoundError`), jamais un téléchargement. `HF_HUB_OFFLINE=1` dans l'image : `tokenizers` tire `huggingface_hub`, qui ne doit jamais rien chercher.
- **La télémétrie d'ONNX Runtime est coupée.** ONNX Runtime 1.32 envoie par défaut des événements d'usage à Microsoft (1DS) : identifiant d'appareil, version, processus. Il les met en file dans le cache de l'utilisateur et les envoie en HTTPS. Le paquet `worker` pose `ORT_DISABLE_TELEMETRY=1` avant tout import d'ONNX Runtime, `worker.models` appelle `disable_telemetry_events()`, et l'image pose la même variable. `test_models.py` vérifie qu'aucune file n'apparaît. Toute mise à jour d'ONNX Runtime revérifie ce point.
- **Chaque fichier est figé et vérifié.** URL à une révision figée (commit Hugging Face, release RapidOCR) et SHA-256 dans `worker/models/fetch.sh`. Un fichier qui change en amont fait échouer le build. La liste vérifiée est dans `/opt/models/SHA256SUMS`.
- **Les licences voyagent avec les modèles** : `NOTICE.md` (source, révision, auteurs) et le texte de la licence Apache-2.0, copiés dans `/opt/models`. Ils accompagneront la distribution de l'OS, comme les avis des ADR 002 et 004.
- **Le nom du modèle accompagne chaque vecteur** (`worker.models.TEXT_MODEL`, `IMAGE_MODEL`) : une table par modèle (CLAUDE.md), pour qu'un changement de modèle ne mélange jamais des vecteurs incomparables.
- **Les requêtes et les documents ne s'encodent pas pareil.** Arctic veut le préfixe `query: ` devant une requête, rien devant un document. SigLIP 2 met le texte en minuscules et le complète à 64 tokens.
- **512 tokens par passage de texte** tant que #70 n'a pas décidé du découpage : Arctic en accepte 8 192, mais l'attention coûte le carré de la longueur.

## Options écartées

- **multilingual-e5-small** (MIT) : le plus léger, mais deux requêtes manquées de plus et des scores trop serrés pour départager des voisins. Il reste le repli si la RAM de la box manque.
- **BGE-M3** (MIT) : moins juste qu'Arctic ici, trois fois plus lent, deux fois plus gros. Il a manqué « contrat de l'appart » sous Linux, alors qu'il le trouvait sous macOS : la quantification int8 le rend sensible au CPU.
- **Qwen3-Embedding-0.6B** (Apache-2.0) : le plus lent (2 s par document estimées sur un Pi 5) et, en int8, moins juste. Sa version fp32 fait 2,4 Go, trop pour la box.
- **multilingual-e5-base** (MIT) : sa version int8 publiée chute à 0,76. La fp32 (1,1 Go) n'a pas été mesurée, puisqu'Arctic fait mieux à taille égale.
- **Arctic en int8** (la première décision) : sa justesse dépend du processeur, voir « Pourquoi ». Ses variantes uint8 et 4 bits ne font pas mieux : l'uint8 met l'assurance devant le bail sous Linux arm64 ; le 4 bits (q4) donne les mêmes vecteurs partout mais à 0,95 du fp32, avec 1,9 Go de RAM, sa table d'embeddings restant en fp32, et pas plus vite que le fp16.
- **Arctic en fp32** : la même justesse que le fp16, un peu plus rapide sous Linux arm64 (148 ms par fenêtre contre 189), mais 2,3 Go de RAM au lieu de 1,3.
- **SigLIP 2 en fp32** : même justesse ici, trois à quatre fois plus gros, et plus lent sous Linux. Sa partie texte en int8 souffre du même écart entre processeurs qu'Arctic ([embeddings.md](../architecture/embeddings.md)).
- **CLIP multilingue** (Apache-2.0 et MIT) : trois fois plus rapide, mais il manque une requête sur quinze. Sa partie texte est distillée d'un modèle anglais.
- **Tesseract** (Apache-2.0) : voir « Pourquoi ». Plan B pour l'OCR des scans.
- **PP-OCRv6 small** : à peine moins juste que PP-OCRv5 latin, et plus lent. Il couvre plus d'écritures (chinois, japonais…), pas utiles pour un foyer français. Ses fichiers sont aussi dans le wheel `rapidocr` de PyPI : c'est le repli si modelscope.cn, d'où viennent les ONNX de PP-OCRv5, devenait indisponible.
- **Écartés d'office pour leur licence**, sans mesure : jina-embeddings-v3 et jina-clip-v2 (CC-BY-NC), NLLB-CLIP (CC-BY-NC, à cause de NLLB), EmbeddingGemma (Gemma Terms of Use). Les licences non commerciales et les conditions d'usage propres à un éditeur ferment la porte à une sortie en open source (CLAUDE.md).
- **Écartés d'office pour leur taille** : SigLIP 2 so400m et les modèles de texte au-delà d'un milliard de paramètres. Ils ne tiennent pas sur un Pi 5 à côté de Postgres.
- **Télécharger les modèles au premier lancement** : contraire à « rien ne sort de la box par défaut ». Une box hors ligne n'aurait pas de recherche.
- **Exporter en ONNX au build, depuis PyTorch** : il faudrait PyTorch dans une étape du build, et l'export n'est pas reproductible octet pour octet, donc pas vérifiable par SHA-256. On prend les ONNX publiés et on vérifie leur empreinte.

## Mise en œuvre

- **`worker/models/fetch.sh <dossier>`** : télécharge les 7 fichiers et vérifie chaque SHA-256. Le téléchargement reprend après une coupure. Il copie ensuite `NOTICE.md` et la licence, puis écrit `SHA256SUMS` en dernier. Il sert à trois endroits : l'image (étape `models` de `worker/Dockerfile`, vers `/opt/models`), le Mac du dev (`make models`, vers `.tools/models`) et la CI (même cible, en cache tant que le script ne change pas).
- **`worker/src/worker/models.py`** : charge les modèles depuis `CLOUDOS_MODELS_DIR` et les fait tourner avec ONNX Runtime sur 4 fils (`CLOUDOS_MODEL_THREADS`). Il fournit `text_encoder()`, `image_encoder()`, `image_query_encoder()` et `ocr_model()`. Pas de tâche ni de table : l'indexation, c'est #70 (texte), #71 (photos) et #68 (OCR, dont le découpage d'une page en lignes).
- **`worker/tests/test_models.py`** (et `test_textemb.py` pour l'export fp16 d'Arctic épinglé dans `fetch.sh`) : les fichiers correspondent à `SHA256SUMS`, « contrat de l'appart » trouve le bail, « chien à la plage » trouve les trois photos de chien sur une plage, l'OCR lit une ligne avec accents et « € » et détecte le texte d'une page. Les tests sont sautés sans les modèles, sauf avec `CLOUDOS_REQUIRE_MODELS` (CI).
- **Pour refaire le banc d'essai** : `cd worker && uv run --group bench python -m bench.fetch`, puis, depuis la racine, `docker build -t cloud-os-bench -f worker/bench/Dockerfile worker` et `docker run --rm -v "$PWD:/repo:ro" cloud-os-bench python -m bench text image` (et `python -m bench.ocr`).
- **Pour revérifier l'image** : `docker run --rm --network none --entrypoint cat <image du worker> /opt/models/SHA256SUMS`, et `pip list` dans l'image ne montre ni `torch` ni `opencv`.

## Conséquences

- #70 (embeddings texte), #71 (embeddings image) et #68 (OCR) peuvent commencer, sur ces modèles et `worker.models`.
- Le corpus du banc d'essai est petit et propre : à rejouer sur un fonds réel (critère de sortie de la phase 3) avant d'en tirer plus.
- `CLAUDE.md` : `make models` et `CLOUDOS_MODELS_DIR`. `docs/architecture/models.md` décrit le code.
