# 002 — La LGPL, sous conditions

- Date : 2026-11-17
- Statut : acceptée
- Issue : #42

## Décision

Les dépendances restent sous licence permissive (MIT, BSD, Apache-2.0, MPL-2.0, ISC, BlueOak-1.0.0), avec une exception : **la LGPL est acceptée** pour

- une bibliothèque **liée dynamiquement** et non modifiée (libheif, libde265 pour lire les HEIC) ;
- un **binaire séparé non modifié**, appelé comme un programme (un build LGPL de ffmpeg pour les miniatures et les métadonnées des vidéos).

La GPL et l'AGPL restent exclues. Un composant qui embarque du code GPL ne passe pas, même s'il est lui-même sous LGPL : ni x264 ni x265 (donc ni le ffmpeg `--enable-gpl` des distributions, ni les wheels pillow-heif, qui embarquent x265). On ne fait que décoder : aucun encodeur HEVC ou H.264 n'est nécessaire.

## Pourquoi

**Sans HEIC ni vidéo, on ne coupe pas iCloud.** Un iPhone enregistre ses photos en HEIC et ses vidéos en HEVC. La sortie de la phase 2 du PRD, c'est que le foyer coupe la sauvegarde Google Photos et iCloud : la box doit au moins afficher une miniature et lire la date de ces fichiers. Il n'existe pas de décodeur HEIC ou HEVC sous licence permissive qui soit maintenu.

**La LGPL reste compatible avec une sortie en open source, sous licence permissive ou non.** Elle impose de laisser l'utilisateur remplacer la bibliothèque : c'est le cas d'une bibliothèque liée dynamiquement ou d'un binaire à part, que l'image Docker installe tel quel. Elle n'impose rien au code de Cloud OS qui l'appelle.

## Ce que ça impose

- **Pas de lien statique** et **pas de modification** du code LGPL. Si un correctif devient nécessaire, il se publie en amont ou avec ses sources.
- **Le binaire ou la bibliothèque vient d'un paquet identifiable** (paquet Debian, build officiel), avec sa licence, pour qu'on puisse le remplacer et en donner la source.
- **Chaque ajout se vérifie** : le paquet choisi n'est pas compilé avec une option GPL (`ffmpeg -version` ne doit pas montrer `--enable-gpl`, ni `libx264` ou `libx265`).
- **Les avis de licence** des composants LGPL accompagneront la distribution de l'OS.

## Options écartées

- **Rester strictement permissif** : pas de miniatures HEIC ni de vidéos, donc pas de remplacement d'iCloud, la raison d'être de la phase 2.
- **Accepter la GPL** (ffmpeg complet, pillow-heif avec x265) : ferme la porte à une sortie sous licence permissive, et aux usages commerciaux que le PRD laisse ouverts.
- **Convertir sur le téléphone** (l'app envoie du JPEG et du H.264) : perd l'original, contraire à la règle « aucune perte de données », et ne couvre pas les fichiers déjà sur la box ou venus d'ailleurs.
- **Décodage matériel ou API du système** (Apple, Android) : n'existe pas sur la box Linux, où tournent les workers.

## Conséquences

- `CLAUDE.md`, règle des dépendances : renvoie à cet ADR.
- #30 (miniatures HEIC) et #41 (vidéos) peuvent avancer : libheif et un ffmpeg LGPL entrent dans l'image du worker.
