# La box en production

Comment la pile se lance en production, d'une seule commande, et ce qui la distingue de la pile de développement. Puis comment on l'installe sur une machine neuve et comment elle redémarre seule (« Installation et démarrage »), et comment on l'ouvre en HTTPS, chez soi comme dehors, par Tailscale (« Accès »). Issues : #99, #100, #103 (Feature #98).

La production est une surcouche de `compose.yaml` : `compose.prod.yaml`. La pile de développement ne change pas.

## Lancer

```bash
make prod        # scripts/box init (une fois), puis scripts/box up
make prod-logs   # suivre les journaux
make prod-down   # arrêter, en gardant la base
```

`scripts/box` est le seul point d'entrée. Il porte le projet `cloud-os-box`, `box.env` et les deux fichiers Compose, pour que la production ne se mélange jamais à la pile d'un worktree (autre projet, autre volume de base). Toute autre commande est passée à Compose avec les mêmes drapeaux : `scripts/box run --rm core verify`, `scripts/box logs --follow`, `scripts/box down -v`.

```mermaid
flowchart TD
  M["make prod"] --> I["scripts/box init<br>cmd_init"]
  I --> E{"box.env existe ?"}
  E -- oui --> K["laissé tel quel, rien n'est réécrit"]
  E -- non --> W["random_password ×2 (32 caractères, /dev/urandom)<br>box.env en 0600 (noclobber)<br>make_dir CLOUDOS_DATA_DIR et CLOUDOS_MAPS_DIR (uid 65532)"]
  K --> U
  W --> U["scripts/box up"]
  U --> V{"check_compose : 2.24 ou plus<br>check_env : mots de passe ni vides ni de développement,<br>CLOUDOS_DATA_DIR existe"}
  V -- échec --> X["refus avec un message, rien ne démarre"]
  V -- ok --> C["docker compose -p cloud-os-box --env-file box.env<br>-f compose.yaml -f compose.prod.yaml<br>up --build --detach --wait"]
  C --> S["db, core, worker (et smb si CLOUDOS_SMB=1)"]
```

## Ce que la surcouche change

| | Développement (`compose.yaml`) | Production (`compose.prod.yaml`) |
|---|---|---|
| Mots de passe | `cloudos` / `cloudos-smb` | `CLOUDOS_DB_PASSWORD` et `CLOUDOS_SMB_DB_PASSWORD` de `box.env`, sans valeur par défaut (`${VAR:?}`) |
| `db` | port publié (54320) | aucun port : seul le cœur s'y connecte, par le réseau Compose |
| `worker`, `smb` | `smb` publié sur 1445 | aucun port publié ; `smb` n'est joignable que par le service `tailscale` |
| `core` | `127.0.0.1:8480` | idem : le seul port ouvert |
| `web` (Vite) | lancé | profil `dev`, jamais lancé |
| Redémarrage | `smb` seulement | `db`, `core` et `worker` : `unless-stopped` |
| Journaux | illimités | pilote `local`, 10 Mo × 5 fichiers, pour tous les services |

La surcouche retire des ports avec `!reset`, qui demande Compose 2.24 ou plus : `scripts/box up` refuse un Compose plus ancien.

## `box.env`

Un fichier à la racine du dépôt, en mode 0600, ignoré par Git et par Docker (`.gitignore`, `.dockerignore`) :

| Clé | Rôle |
|---|---|
| `CLOUDOS_DB_PASSWORD` | mot de passe du rôle `cloudos` de Postgres |
| `CLOUDOS_SMB_DB_PASSWORD` | mot de passe du rôle `cloudos_smb`, posé par le cœur à chaque démarrage ([smb.md](smb.md), « Rôle en base ») |
| `CLOUDOS_DATA_DIR` | les fichiers des utilisateurs, `/srv/cloudos/files` par défaut |
| `CLOUDOS_MAPS_DIR` | le fichier `.pmtiles` de la carte, `/srv/cloudos/maps` par défaut |
| `RESTIC_PASSWORD`, `CLOUDOS_BACKUP_DB_PASSWORD`, `CLOUDOS_BACKUP_DIR` | la sauvegarde, ajoutés par `scripts/box backup-init` ([backup.md](backup.md)) |

On peut y ajouter `CLOUDOS_CORE_PORT`, ainsi que les réglages de l'accès (« Accès » ci-dessous) :

| Clé | Rôle |
|---|---|
| `CLOUDOS_SMB` | `1` lance le service `smb` |
| `CLOUDOS_TAILSCALE` | `1` lance le service `tailscale` |
| `TS_AUTHKEY` | la clé d'authentification de Tailscale, pour la première connexion seulement |
| `TS_HOSTNAME` | le nom de la box dans le réseau Tailscale, `cloudos` par défaut |
| `CLOUDOS_FUNNEL` | `1` ouvre les liens de partage à internet par Funnel |
| `CLOUDOS_PUBLIC_URL`, `CLOUDOS_SHARE_ORIGIN` | écrites par `scripts/box tailscale`, jamais à la main |

`CLOUDOS_SMB`, `CLOUDOS_TAILSCALE` et `CLOUDOS_FUNNEL` peuvent aussi venir du shell, qui l'emporte alors sur le fichier. `scripts/box` ignore les variables `CLOUDOS_DB_PASSWORD`, `CLOUDOS_SMB_DB_PASSWORD`, `CLOUDOS_DATA_DIR`, `CLOUDOS_MAPS_DIR`, `TS_AUTHKEY`, `TS_HOSTNAME`, `CLOUDOS_PUBLIC_URL`, `CLOUDOS_SHARE_ORIGIN` et `CLOUDOS_TRUSTED_PROXIES` du shell, pour qu'un réglage de développement ne passe jamais devant le fichier.

`init` ne réécrit jamais un `box.env` qui existe : Postgres ne lit `POSTGRES_PASSWORD` qu'à la création du volume, et un nouveau mot de passe fermerait la porte de la base. `CLOUDOS_DATA_DIR=… CLOUDOS_MAPS_DIR=… scripts/box init` choisit d'autres dossiers (le fichier lui-même se choisit avec `CLOUDOS_BOX_ENV`, comme le fait la CI, et le projet Compose avec `CLOUDOS_BOX_PROJECT`, pour une box de test à côté de la vraie).

Les dossiers sont donnés à l'uid 65532, celui du cœur (distroless `nonroot`) ; le worker, `nobody`, y lit en lecture seule. `init` utilise `sudo` seulement si elle ne peut pas créer ou changer un dossier elle-même, et sans terminal elle ne l'attend pas : elle dit alors la commande à lancer. Docker Desktop masque ce sujet sur le Mac, pas Linux.

## Reprendre

- **`box.env` perdu.** Aucune donnée n'est perdue, mais le cœur ne peut plus se connecter. Dans le conteneur `db`, la connexion locale ne demande pas de mot de passe : `scripts/box exec db psql -U cloudos -c "ALTER ROLE cloudos PASSWORD '<nouveau>'"`, puis `CLOUDOS_DB_PASSWORD=<nouveau>` dans un nouveau `box.env` (le mot de passe de `cloudos_smb` est reposé par le cœur au démarrage).
- **Disque de données absent.** `up` refuse un `CLOUDOS_DATA_DIR` qui n'existe pas, sans jamais le créer, pour que le cœur n'écrive pas sur la carte SD à la place d'un disque non monté. Sur une box installée par `scripts/install`, Docker lui-même ne démarre pas sans le SSD, et les fichiers sont dans `/srv/cloudos/files`, un dossier du SSD : le point de montage vide ne l'a pas (« Installation et démarrage » ci-dessous).
- **Mettre à jour.** `scripts/box update` : `git pull --ff-only`, une sauvegarde, puis les images se reconstruisent sur la box et les migrations passent au démarrage du cœur. Une mise à jour commence par une sauvegarde : si elle échoue, la mise à jour s'arrête ([backup.md](backup.md)).
- **La box ou son disque meurt.** La restauration complète depuis le disque de sauvegarde : [backup.md](backup.md), « Restaurer sur une box neuve, pas à pas ».

## Installation et démarrage

`scripts/install` prépare une machine neuve, une fois, en root : une Debian 64 bits, Raspberry Pi OS Lite compris. Il ne formate, ne partitionne et n'efface jamais un disque ; il n'écrit que des lignes dans `/etc/fstab`, après une copie. Toutes ses vérifications passent avant la moindre écriture, et relancé, il ne change rien de plus. Le guide de l'admin, de la carte SD à la box en service, est [`docs/install.md`](../install.md). Issue : #103.

```mermaid
flowchart TD
  subgraph install["sudo scripts/install [--data UUID] [--backup UUID], une fois"]
    checks["check_machine : root, Debian (ou ID_LIKE), arm64 ou amd64<br/>check_clone : propriétaire = SUDO_USER, chemin simple<br/>check_apt : une seule source Docker, pas docker.io<br/>check_disks : UUID 8-4-4-4-12, blkid -U, ext4/xfs/btrfs, ni mmcblk<br/>ni disque système, sauvegarde sur un autre disque (lsblk PKNAME),<br/>fstab, point de montage vide et pas un lien<br/>check_docker_root : daemon.json avec data-root et containerd-snapshotter false, ou absent<br/>et /var/lib/docker vide<br/>check_docker_key : empreinte 9DC8…CD88 (gpg --show-keys)"]
    checks -- "un refus" --> stop1["message, rien n'est écrit"]
    checks --> disks["add_fstab : fstab.cloudos.bak, puis<br/>UUID=… /srv/cloudos (SSD), UUID=… /media/cloudos-backup (USB)<br/>nofail,noatime,x-systemd.device-timeout=10s<br/>findmnt --verify, mv, sync, puis mount ; aucun formatage"]
    disks --> units["install_units : docker.service.d/cloudos.conf (RequiresMountsFor=/srv/cloudos)<br/>cloudos.service (User = propriétaire du clone)<br/>/etc/docker/daemon.json : data-root /srv/cloudos/docker,<br/>features.containerd-snapshotter false (images en overlay2 sous data-root)"]
    units --> apt["install_docker : /etc/apt/keyrings/docker.asc, sources.list.d/docker.list<br/>docker-ce, docker-compose-plugin, Compose ≥ 2.24"]
    apt --> rootdir["docker info : DockerRootDir = /srv/cloudos/docker<br/>et Driver = overlay2, sinon arrêt avant le build"]
    rootdir --> box["start_box : groupe docker, /srv/cloudos/files et maps (uid 65532, pas de lien)<br/>scripts/box init puis up, en propriétaire du clone (runuser)<br/>systemctl enable cloudos.service"]
  end
  subgraph boot["À chaque démarrage"]
    mount["srv-cloudos.mount : le SSD"] --> docker["docker.service, son dossier et ses images sur le SSD"]
    docker --> svc["cloudos.service : scripts/box up --no-build"]
    nossd["SSD absent (10 s)"] -. "docker.service : Dependency failed" .-> sd["aucun conteneur, rien n'est écrit sur la carte SD"]
  end
  box --> guide["docs/install.md : Tailscale, compte admin,<br/>mot de passe SMB, backup-init, redémarrage"]
```

### Ce qu'il vérifie, puis ce qu'il écrit

- **La machine** (`check_machine`) : root, `ID=debian` ou `debian` dans `ID_LIKE` (Raspberry Pi OS), un `VERSION_CODENAME`, et `dpkg --print-architecture` à `arm64` ou `amd64` : un système 32 bits est refusé.
- **Le clone** (`check_clone`) : un chemin sans espace ni symbole (systemd et `sed` le lisent tel quel), hors de `/srv/cloudos` et de `/media/cloudos-backup`. Son propriétaire doit être celui qui lance `sudo` (`SUDO_USER`) : le groupe `docker`, qui vaut root, ne va à personne d'autre, et root ne copie rien de l'arbre d'un autre utilisateur. Les deux unités ne doivent pas être des liens. Un `box.env` déjà là doit garder les fichiers dans `/srv/cloudos/files`.
- **apt** (`check_apt`) : aucune autre source ne déclare `download.docker.com`, et aucun paquet Docker de Debian (`docker.io`, `docker-compose`, `podman-docker`, `containerd`, `runc`) n'est installé.
- **Les disques** (`check_disks`, `check_disk`) :
  - un UUID vide ou qui n'a pas la forme 8-4-4-4-12 est refusé, jamais lu comme « pas de disque » ; il est mis en minuscules ;
  - refus d'un UUID que `blkid -U` ne connaît pas, d'une carte SD ou d'une eMMC (`/dev/mmcblk*`), du disque système, et d'un système de fichiers autre qu'ext4, xfs ou btrfs ;
  - la sauvegarde va sur un autre disque physique que les données et que le système (`lsblk -o PKNAME`) : une panne ne doit pas emporter les deux ;
  - `fstab` est lu en ramenant `UUID="…"` et `/dev/disk/by-uuid/…` à `UUID=…`, et le point de montage sans `/` final. Refus d'une ligne qui monte un autre disque au même point, ou ce disque ailleurs ;
  - refus d'un point de montage qui est un lien symbolique, qui porte un autre disque, ou qui contient des fichiers alors que rien n'y est monté ;
  - sans `--data`, un disque déjà monté sur `/srv/cloudos` passe par les mêmes contrôles, puis entre dans `fstab` ; sinon, une machine dont `/` est sur `/dev/mmcblk*` est refusée.
- **La clé de Docker** (`check_docker_key`), encore avant toute écriture : `ca-certificates`, `curl` et `gnupg` s'il le faut, puis la clé, téléchargée en HTTPS seulement (`--proto '=https' --tlsv1.2`) dans un dossier temporaire. Elle est lue par `gpg --show-keys` dans un `GNUPGHOME` jetable, et gardée seulement si elle a une seule clé primaire, d'empreinte `9DC858229FC7DD38854AE2D88D81803C0EBFCD88`. Une clé déjà dans `/etc/apt/keyrings/docker.asc` est vérifiée de même. Aucun `curl | sh`.
- **`fstab`** (`add_fstab`) : la première fois, une copie en `/etc/fstab.cloudos.bak`, jamais remplacée ensuite. La nouvelle version est écrite à côté, vérifiée par `findmnt --verify --tab-file`, mise en place par `mv`, puis `sync`. Passe de `fsck` : 2 pour ext4, 0 pour xfs et btrfs. Le disque est ensuite monté, et le script vérifie que c'est bien cet UUID qui est monté avant d'écrire quoi que ce soit dessous.
- **Le dossier de Docker** (`check_docker_root`, avec un SSD de données seulement) : un `/etc/docker/daemon.json` qui porte déjà `"data-root": "/srv/cloudos/docker"` et `"containerd-snapshotter": false` passe, quelle que soit sa mise en page. Un `daemon.json` sans `data-root`, ou avec un autre dossier, est refusé ; sans `"containerd-snapshotter": false`, ou avec `true`, aussi. Un Docker qui garde déjà des données dans `/var/lib/docker` est refusé de même. Le script ne déplace jamais rien lui-même ; [`docs/install.md`](../install.md) dit comment le faire.
- **systemd et `daemon.json`** (`install_units`), avant le premier démarrage de Docker, car le paquet le démarre dès son installation. Le script installe `box/systemd/docker.service.d/cloudos.conf` et `box/systemd/cloudos.service`, où `@ROOT@` et `@USER@` deviennent le chemin du clone et son propriétaire. Avec un SSD de données, il écrit aussi `/etc/docker/daemon.json`, avec deux réglages :
  - `"data-root": "/srv/cloudos/docker"` : les volumes de Docker y vivent, la base Postgres comprise ;
  - `"features": {"containerd-snapshotter": false}` : les images et les couches des conteneurs restent sous ce dossier, dans `overlay2`.

  Sur une installation neuve de Docker 29, le magasin d'images par défaut est celui de containerd, qui garde images et couches dans `/var/lib/containerd`, sur le disque système. `data-root` ne le déplace pas : la documentation de Docker le dit (pages « Daemon data directory » et « containerd image store »). Avec `false`, le démon choisit le pilote classique explicitement, installation neuve comprise (`daemon/image_store_choice.go` de moby). Un `docker:29-dind` le confirme : avec ce `daemon.json`, `docker info` donne `Storage Driver: overlay2` et `Docker Root Dir: /srv/cloudos/docker`, et une image tirée finit dans `/srv/cloudos/docker/overlay2`. Sans ce réglage, `docker info` donne `overlayfs`, le snapshotter de containerd. La base, les images et les couches des conteneurs ne vont donc jamais sur la carte SD.
- **Docker** (`install_docker`) : la clé vérifiée va dans `/etc/apt/keyrings/docker.asc`, puis la source `deb [arch=… signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian <codename> stable`. Chaque fichier n'est réécrit que s'il diffère. Ensuite viennent `docker-ce`, `docker-ce-cli`, `containerd.io`, `docker-buildx-plugin` et `docker-compose-plugin`, et le script vérifie que Compose est en 2.24 ou plus récent. Avec un SSD de données, `docker info` doit donner `/srv/cloudos/docker` comme `DockerRootDir` et `overlay2` comme `Driver`, sinon le script s'arrête avant de construire quoi que ce soit. `overlay2` est le premier pilote de Docker sur ext4, xfs (`ftype=1`) et btrfs, les trois systèmes de fichiers acceptés.
- **La box** (`start_box`) :
  - le propriétaire entre dans le groupe `docker` ;
  - `files` et `maps` sont créés sous `/srv/cloudos`, et `cloudos-backup` sur le disque USB, à l'uid 65532 ; un lien à leur place, laissé sur un disque réutilisé, est refusé ;
  - `scripts/box init` puis `up` tournent au nom du propriétaire (`runuser`), avec `CLOUDOS_DATA_DIR=/srv/cloudos/files` et `CLOUDOS_MAPS_DIR=/srv/cloudos/maps` ;
  - le script affiche la durée du build, active `cloudos.service`, puis donne les étapes suivantes. `backup-init` reste à l'admin, car c'est lui qui doit voir la clé.
- **Les tests** (`scripts/test-install.sh`) lancent le script sur un arbre à eux, avec `CLOUDOS_INSTALL_SYSROOT` et des faux sur le `PATH`. Hors des tests (`CLOUDOS_INSTALL_TEST=1`), le script refuse cette variable.

### Au démarrage

- **`RequiresMountsFor=/srv/cloudos`** dans `docker.service` : sans le SSD, son montage échoue au bout de 10 s (`x-systemd.device-timeout`), `docker.service` aussi, et aucun conteneur ne redémarre. Sans cette règle, Docker relancerait `core` (`restart: unless-stopped`), et le montage de `CLOUDOS_DATA_DIR` créerait `/srv/cloudos/files` sur la carte SD. Grâce à `nofail`, la machine démarre quand même, et l'admin s'y connecte en SSH.
- **`cloudos.service`** : `oneshot`, après `docker.service` et `network-online.target`, sous l'utilisateur propriétaire du clone. Il lance `scripts/box up --no-build`, qui revérifie `box.env` et relance une box arrêtée par `scripts/box down` avant le redémarrage. Il ne construit jamais : un nouveau code ne tourne qu'après `scripts/box update`, qui sauvegarde d'abord. Un `git pull` suivi d'un redémarrage garde donc les images d'avant.
- **Sans le disque USB**, la box démarre ; la sauvegarde de la nuit échoue avec `ErrNoRepository`, sans recréer de dépôt ailleurs ([backup.md](backup.md)).

### Une coupure pendant un envoi

Un envoi qui a répondu est sur le disque. `storage` passe par `syncFile` et `syncDir` avant chaque réponse :

- **la création d'un envoi reprenable** (`CreateUpload`) : le fichier partiel et son nom dans `.cloudos/uploads`, avant que le client ne reçoive son adresse ;
- **chaque morceau** (`appendBytes`) : ses octets ;
- **le dépôt du fichier** (`writeTemp`, puis `place`) : le contenu, puis son nom dans son dossier ; `mkdirSynced` crée les dossiers parents qui manquent et synchronise le dossier qui contient chacun ;
- **un remplacement** (`replace`) : le nom du nouveau contenu, et celui de l'ancien, devenu une version.

`durable_test.go` le vérifie : chaque écriture passe par ces deux fonctions avant de rendre la main. `scripts/test-crash.sh` coupe un envoi de 200 Mo à mi-chemin par `docker kill -s KILL` sur `db` et `core`, sur une box jetable (projet `cloud-os-crash`, port 8494, `box.env` et dossiers temporaires). Après `scripts/box up core`, `core verify` passe, l'envoi reprend à l'offset que le disque donne et finit avec le même SHA-256, et le dossier ne contient que le fichier. La CI `images` le lance en amd64 et en arm64. Le débranchement de l'alimentation reste à jouer sur le Pi 5 ([`docs/install.md`](../install.md)).

### Mesurer : `scripts/measure`

La configuration requise vient de mesures prises sur le Pi 5, dans une box jetable, jamais dans la vraie : projet `cloud-os-mesure`, son propre `box.env`, ses dossiers dans `/srv/cloudos/measure` (sur le SSD), port 8494. Le compte est créé par `/api/setup` avec un mot de passe tiré au hasard, jamais affiché. 1 000 photos et 100 PDF du dossier donné y sont copiés, puis importés par `core import` en lecture seule. Pendant l'indexation, le script relève la mémoire de chaque conteneur (`docker stats` toutes les 5 s), le temps jusqu'à une file vide (`tasks` de `/api/health`), et les p50 et p95 de `GET /api/files` et de `GET /api/timeline`. Il rend le tableau de `docs/install.md`, puis retire la box et son dossier.

## Accès

La box s'ouvre en HTTPS à `https://cloudos.<tailnet>.ts.net`, chez soi comme dehors, sans ouvrir aucun port sur la box internet. Un service Compose, `tailscale`, la relie au réseau Tailscale du foyer. Le guide de l'utilisateur, du compte Tailscale au retrait d'un appareil, est [`docs/access.md`](../access.md). Issue : #100.

```mermaid
flowchart LR
  subgraph tailnet["Réseau Tailscale (WireGuard de bout en bout)"]
    Mac["Mac : web, WebDAV, SMB"]
    iPhone["iPhone : app, sauvegarde"]
  end
  Guest["Invité sur internet"]
  subgraph box["Box (Compose, projet cloud-os-box)"]
    TS["tailscale<br>tailscale serve, certificat ts.net<br>box/tailscale/serve.json"]
    Core["core :8080<br>edge.Config.Handler : ClientIP, Secure,<br>garde Funnel (404 hors des liens)"]
    SMB["smb :445<br>edge.ProxyListener (PROXY v1)"]
  end
  Mac -->|"https :443"| TS
  iPhone -->|"https :443"| TS
  Mac -->|"tcp :445"| TS
  Guest -->|"Funnel https :8443<br>/s/, /api/s/, /assets/ seulement"| TS
  TS -->|"http, X-Forwarded-For,<br>X-Forwarded-Proto, Tailscale-Funnel-Request"| Core
  TS -->|"tcp, en-tête PROXY v1"| SMB
  Coord["Serveur de coordination Tailscale<br>(métadonnées seulement)"] -.-> TS
```

### Le service `tailscale`

- **Éteint par défaut.** Il est sous le profil Compose `tailscale`, que `scripts/box` n'allume qu'avec `CLOUDOS_TAILSCALE=1` (et `make up` de même en développement). Si on l'éteint, `scripts/box up` arrête le conteneur.
- **L'image** est `tailscale/tailscale` à une version épinglée (v1.104.7, licence BSD-3).
- **Le réseau est en mode « userspace »** (`TS_USERSPACE`) : ni `NET_ADMIN` ni `/dev/net/tun`. Le service ne publie aucun port : il sort vers les serveurs de Tailscale, et les appareils du réseau Tailscale entrent par là.
- **Son identité** vit dans le volume `tailscale-state`. `TS_AUTHKEY` ne sert qu'à la première connexion (`TS_AUTH_ONCE`) : `scripts/box up` la demande tant que le volume n'existe pas.
- **`TS_SERVE_CONFIG`** pointe vers `box/tailscale/serve.json`, ou vers `serve-funnel.json` avec `CLOUDOS_FUNNEL=1`. `tailscale serve` y remplace `${TS_CERT_DOMAIN}` par le nom de la box.

`tailscale serve` (vérifié dans son code source, v1.104.7) :

- termine HTTPS avec le certificat `ts.net` de la box, puis relaie à `http://core:8080` par le réseau Compose. Il joint une cible hors localhost, `core` ou `smb`, par le DNS de Compose ;
- pose `X-Forwarded-For` (l'adresse de l'appareil ou de l'invité, qui remplace celle d'un client), `X-Forwarded-Proto: https` et, sur une requête venue par Funnel, `Tailscale-Funnel-Request: ?1`. Il efface les `Tailscale-*` envoyés par le client ;
- relaie SMB en TCP avec un en-tête PROXY v1 (`"ProxyProtocol": 1`), qui nomme l'appareil ;
- retire le point de montage avant de relayer : la cible de `/s/` est donc `http://core:8080/s`.

### Deux portes

| Port | Qui | Mène à |
|---|---|---|
| `:443` (HTTPS) | les appareils du réseau Tailscale | tout le cœur : web, API, WebDAV (`serve.json`) |
| `:445` (TCP) | les appareils du réseau Tailscale | `smb:445`, quand SMB est allumé |
| `:8443` (HTTPS, Funnel) | tout internet, avec `CLOUDOS_FUNNEL=1` seulement | `/s/`, `/api/s/` et `/assets/` du cœur, rien d'autre (`serve-funnel.json`) |

Funnel n'ouvre que les ports 443, 8443 et 10000, et `AllowFunnel` ne vaut que pour `:8443`. SMB n'est jamais public.

### Ce que le cœur croit de sa porte : `core/internal/edge`

- **Le proxy de confiance.** `CLOUDOS_TRUSTED_PROXIES` liste des adresses, des préfixes ou des noms. Avec `CLOUDOS_TAILSCALE=1`, `scripts/box` (et `make`) y met `tailscale`, que `edge.Proxies` résout au démarrage puis toutes les 30 s (`RefreshEvery`), car le conteneur peut changer d'adresse en redémarrant sans le cœur. Un nom qui ne se résout pas garde ses dernières adresses. Sans Tailscale, la liste est vide : aucun en-tête n'est cru, et aucun nom n'est cherché, car le DNS de Docker poserait la question d'un nom inconnu au DNS du réseau de la maison.
- **`edge.ClientIP`.** L'adresse du client est le dernier `X-Forwarded-For` quand la connexion vient du proxy de confiance, sinon l'adresse de la connexion. Les limites d'essais comptent par elle : connexion (`httpapi.clientIP`), WebDAV (`auth/basic.go`) et mot de passe d'un lien. Un client qui écrit lui-même `X-Forwarded-For` reste compté à sa propre adresse.
- **`edge.Secure`.** Les cookies de session et d'accès à un lien sont `Secure` quand `CLOUDOS_PUBLIC_URL` est en https, ou quand le proxy de confiance envoie `X-Forwarded-Proto: https`. Un en-tête d'un autre client ne décide plus de ce drapeau.
- **La garde Funnel** (`edge.Config.Handler`). Une requête qui porte `Tailscale-Funnel-Request` n'atteint que `/s/…`, `/api/s/…` et `/assets/…`, chemin nettoyé (`/s/../api/me` reste dehors). Tout le reste répond 404 : connexion, `/api/me`, `/api/files`, `/dav/`, `/`. La garde vaut quelle que soit l'adresse d'où vient la requête : un client qui envoie l'en-tête lui-même ne fait que se fermer la porte. C'est la seconde barrière, après les chemins de `serve-funnel.json`.
- **`GET /api/config`** (route de session) rend `share_origin`, `CLOUDOS_SHARE_ORIGIN`. Le web construit les liens de partage sur cette adresse quand elle existe (`shareOrigin`, `shareURL` dans `web/src/share/shares.ts`), sinon sur sa propre adresse.
- **SMB.** `smbfs.Config.Proxies` enveloppe l'écoute d'un `edge.ProxyListener`. Une connexion du proxy de confiance doit commencer par un en-tête PROXY v1, sinon elle est fermée. Son en-tête est lu hors de la boucle d'acceptation, en 5 s au plus. La connexion d'un autre client est prise telle quelle. `smbsrv` reçoit l'écoute par `Config.Listener` (`PATCHES.md`, 8). Ses limites par adresse et `auth.SMBHash` voient donc l'appareil, et `allowed` admet les adresses Tailscale (100.64.0.0/10) mais refuse toujours une adresse publique, même nommée par le proxy.

### Les adresses : `scripts/box tailscale`

Après la première connexion, `scripts/box tailscale` lit le nom de la box dans `tailscale status --json`. Il refuse si MagicDNS ou les certificats HTTPS sont éteints dans le réseau Tailscale. Il écrit ensuite dans `box.env`, en gardant le mode 0600 :

- `CLOUDOS_PUBLIC_URL=https://cloudos.<tailnet>.ts.net` ;
- `CLOUDOS_SHARE_ORIGIN=https://cloudos.<tailnet>.ts.net:8443` avec Funnel, vide sinon.

Puis il relance la pile, pour que le cœur prenne les nouvelles valeurs. Le cœur refuse de démarrer si l'une d'elles n'est pas une origine `http(s)://hôte[:port]` (`frontDoor`, `originOf` dans `core/cmd/core/main.go`).

L'app mobile prend `https://` par défaut pour une adresse en `.ts.net` (`normalizeBox`).

## Ce qui sort de la box

Construire les images télécharge les images de base, les paquets et les modèles, à l'installation et à la mise à jour. À l'exécution, sans `CLOUDOS_TAILSCALE=1`, rien ne sort.

Avec Tailscale :

- **Le serveur de coordination** de Tailscale voit les métadonnées : les appareils, leurs adresses et leurs clés publiques. Il ne voit pas le trafic, chiffré en WireGuard de bout en bout entre les appareils.
- **Le nom de la box** (`cloudos.<tailnet>.ts.net`) est publié dans les journaux publics des certificats, quand la box demande son certificat.
- **Avec Funnel**, le trafic des invités passe par les relais de Tailscale. Il reste chiffré jusqu'à la box, qui termine elle-même TLS.

## La CI

`.github/workflows/images.yml` tourne à chaque push sur `main` et sur les PR qui touchent une entrée des images, sur des runners natifs `ubuntu-24.04` (amd64) et `ubuntu-24.04-arm` (arm64), avec le cache des couches (le worker compile ffmpeg et récupère environ 1 Go de modèles). Dans l'ordre :

1. `scripts/box init` dans un dossier temporaire : `box.env` en 0600, dossiers à l'uid 65532, un second `init` qui le laisse intact.
2. `docker compose config` : aucun port pour `db`, `worker` et `smb`, un seul pour le cœur sur `127.0.0.1`, pas de service `web`, `restart` et journaux plafonnés. Pas de service `tailscale` sans `CLOUDOS_TAILSCALE=1`. Avec lui : aucun port ni privilège, réseau en userspace, `serve-funnel.json` seulement avec Funnel. Funnel ne vaut que pour `:8443` et les chemins des liens.
3. `up` refuse `cloudos`, `cloudos-smb`, un dossier de données absent, Funnel sans Tailscale et Tailscale sans `TS_AUTHKEY`.
4. Les images se construisent, `up` démarre la pile, `/api/health` répond 200, `scripts/box run --rm core verify` passe.
5. La sauvegarde : `backup-init` sur un dossier temporaire, `backup`, la restauration test, puis `backup-status` sans échec.
6. La coupure : `scripts/test-crash.sh`, sur une box jetable à côté (« Une coupure pendant un envoi » ci-dessus) ; enfin `down -v`.

`make test` lance aussi `scripts/test-install.sh` sur chaque PR (`ci.yml`). Publier des images signées dans un registre, pour éviter le long premier build sur la box, reste un autre sujet.
