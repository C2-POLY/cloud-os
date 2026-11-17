# WebDAV : le drive dans le Finder et l'Explorateur

Monter son drive comme un disque réseau, sans client à installer : `core/internal/davfs`, servi à `/dav/` par `core/internal/httpapi` (`davHandler`).
Issue : #12.

## Se connecter

| Système | Comment |
|---|---|
| macOS | Finder › Aller › Se connecter au serveur… › `http://<box>:8480/dav/`, avec son identifiant et son mot de passe Cloud OS |
| Windows | Explorateur › Connecter un lecteur réseau › `http://<box>:8480/dav/`. Windows refuse par défaut l'authentification Basic sur HTTP : il faudra HTTPS (prévu par le PRD), ou le réglage `BasicAuthLevel` du service WebClient |

## Protocole

`golang.org/x/net/webdav` (BSD) parle WebDAV (PROPFIND, GET, PUT, MKCOL, MOVE, COPY, DELETE, LOCK). `davfs.FS` lui fournit le système de fichiers, branché sur `storage` : toutes les garanties du stockage s'appliquent.

| Action dans le Finder | Requête | Ce que fait le cœur |
|---|---|---|
| Ouvrir un dossier | PROPFIND | `List` |
| Ouvrir un fichier | GET | `Open` |
| Enregistrer | PUT | `Create` puis `Commit` : écriture temporaire, puis remplacement ; l'ancien contenu devient une version |
| Nouveau dossier | MKCOL | `Mkdir` (le dossier parent doit exister, sinon 409) |
| Renommer, déplacer | MOVE | `Move` journalisé ; une destination existante part d'abord à la corbeille |
| Dupliquer, copier | COPY | lecture puis écriture, comme un enregistrement |
| Supprimer | DELETE | `Trash` : rien n'est effacé |

## Connexion

Le Finder et l'Explorateur n'envoient que du HTTP Basic : identifiant et mot de passe du compte, à chaque requête.

- **Coût :** vérifier un mot de passe argon2id coûte environ 100 ms, alors `auth.BasicAuth` fait confiance à une connexion réussie pendant 10 minutes (`BasicTTL`).
- **Cache :** il est indexé par le SHA-256 de l'identifiant et du mot de passe, jamais par le mot de passe lui-même.
- **Désactivation :** le compte est relu à chaque requête, donc un compte désactivé est coupé aussitôt.
- **Échecs :** la limitation des échecs de connexion s'applique comme pour la connexion web (`CheckCredentials`).
- **CSRF :** `/dav` échappe à la règle « requêtes JSON » de l'API. Un navigateur ne peut pas envoyer PROPFIND, PUT, MOVE ou DELETE vers un autre site sans vérification préalable CORS, que le cœur n'accorde jamais ; un POST sur `/dav` n'est qu'une lecture.

## Fichiers du Finder

Le Finder écrit ses propres fichiers à côté des documents : `.DS_Store` (disposition d'un dossier) et `._*` (métadonnées et ressources). Les refuser lui ferait afficher des erreurs ; les traiter comme des documents encombrerait l'index et la corbeille.

`davfs.AppleDouble` les range donc à part, sous `.cloudos/appledouble/<compte>/<chemin>` :
- **Hors du drive :** ils ne sont ni indexés, ni listés, ni visibles dans le drive web.
- **Effacement direct :** quand le Finder les supprime, ils sont effacés sans passer par la corbeille. C'est `removeAppleFile`, dans la liste du garde-fou, confiné à ce dossier et refusé pour tout nom qui n'est pas un fichier du Finder (`IsAppleFile`).
