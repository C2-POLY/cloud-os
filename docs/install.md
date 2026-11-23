# Installer la box

D'une machine neuve à une box en service, avec Tailscale et la sauvegarde. Le Raspberry Pi 5 est la machine de référence : c'est sur lui que la configuration requise est mesurée et que ce guide est joué ([décision 008](decisions/008-materiel.md)). Une autre machine qui suit la configuration requise convient aussi, un mini PC x86 par exemple. Issue : #103.

Le fonctionnement technique (le script, les unités systemd, le démarrage) est dans [`architecture/box.md`](architecture/box.md), section « Installation et démarrage ».

## La configuration requise

> **Provisoire, à mesurer sur le Pi 5 avec `scripts/measure`.** Ces valeurs viennent du design de #103. Les mesures ci-dessous les confirmeront ou les corrigeront ; tant que leur tableau est vide, rien ici n'est garanti.

| | Minimale | Conseillée |
|---|---|---|
| Processeur | 64 bits, 4 cœurs : Cortex-A76 (Pi 5) ou x86-64-v2 | idem |
| Mémoire | 4 Go | 8 Go |
| Système | Debian 12 ou plus récent, 64 bits, ou Raspberry Pi OS Lite 64 bits | idem |
| Données | un SSD en ext4, en USB 3 | un SSD NVMe en ext4 |
| Sauvegarde | un disque USB au moins aussi grand que les données | idem, rangé à part |
| Réseau | Ethernet | idem |

- **Le système** peut rester sur une carte SD, mais les données jamais : sans SSD de données, `scripts/install` refuse une machine qui démarre sur une carte SD.
- **xfs et btrfs** conviennent aussi pour les données et la sauvegarde. exFAT, NTFS et FAT ne conviennent pas : `storage` a besoin des liens durs, et la sauvegarde des propriétaires Unix.
- **Les images** sont construites pour arm64 et amd64 ; un système 32 bits est refusé.

### Les mesures sur le Pi 5

À remplir avec la sortie de `scripts/measure`, sur le Pi 5 installé par ce guide. La mesure tourne dans une box jetable, à côté de la vraie, jamais dedans :

```bash
# 1 000 photos et 100 PDF pris dans le dossier, copiés dans une box jetable
# (projet cloud-os-mesure, dossier /srv/cloudos/measure, port 8494), puis retirés.
scripts/measure ~/corpus-mesure
```

Le dossier n'est que lu, et rien ne sort de la machine. La ligne du premier build vient de `scripts/install`, qui l'affiche à la fin (« the box was built and started in … »).

| Mesure | Valeur sur le Pi 5 |
|---|---|
| Machine | à mesurer |
| Cloud OS | à mesurer |
| Corpus | 1 000 photos et 100 PDF, à mesurer |
| Mémoire au plus haut : `core` | à mesurer |
| Mémoire au plus haut : `db` | à mesurer |
| Mémoire au plus haut : `worker` | à mesurer |
| Indexation : file vide après | à mesurer |
| Tâches en échec | à mesurer |
| `GET /api/files` (dossier de 1 000 photos) pendant l'indexation, p50 / p95 | à mesurer |
| Chronologie (`GET /api/timeline`) pendant l'indexation, p50 / p95 | à mesurer |
| Interface fluide (p95 sous 500 ms) | à mesurer |
| Premier build (`scripts/install` : ffmpeg et environ 1 Go de modèles) | à mesurer |

## Le guide

### 1. Flasher le système

Sur un Raspberry Pi 5 : avec Raspberry Pi Imager, choisissez **Raspberry Pi OS Lite (64-bit)**. Dans les réglages de l'Imager, donnez un nom à la machine, un utilisateur et votre clé SSH. Branchez le Pi en Ethernet.

Sur un mini PC x86 : Debian 12 ou 13 (image `netinst`), sans bureau, avec le serveur SSH.

Connectez-vous ensuite en SSH avec cet utilisateur : c'est lui qui fera tourner la box.

### 2. Préparer les disques

Branchez le SSD de données et le disque USB de sauvegarde, puis :

```bash
lsblk -f
```

La colonne `UUID` donne l'identifiant de chaque partition, et `FSTYPE` son système de fichiers. **`scripts/install` ne formate jamais rien** : un disque en ext4, xfs ou btrfs est utilisé tel quel, avec ce qu'il contient.

Un disque neuf est souvent en exFAT ou en NTFS. Il faut alors le formater vous-même, une fois :

> **Attention : `mkfs.ext4` efface tout le contenu de la partition nommée, sans retour possible.** Vérifiez trois fois son nom avec `lsblk -f` : la carte SD est `mmcblk0`, le SSD et le disque USB sont `sda`, `sdb` ou `nvme0n1`. Ne la lancez jamais sur un disque qui contient des données à garder.

```bash
# EXEMPLE, à adapter : efface /dev/sda1 et en fait une partition ext4.
sudo mkfs.ext4 -L cloudos-data /dev/sda1
```

Relancez `lsblk -f` et notez les deux UUID : celui du SSD de données et celui du disque de sauvegarde.

### 3. Installer

```bash
sudo apt-get install -y git
git clone https://github.com/C2-POLY/cloud-os.git ~/cloud-os
cd ~/cloud-os
sudo scripts/install --data <UUID du SSD> --backup <UUID du disque USB>
```

Sur une machine qui démarre déjà sur son SSD (NVMe sur le Pi 5, ou un mini PC), `--data` peut être omis : les données restent alors sur le disque système, dans `/srv/cloudos`. Le disque de sauvegarde peut aussi s'ajouter plus tard, en relançant `sudo scripts/install --backup <UUID>`.

Lancez-le avec `sudo` depuis votre utilisateur, propriétaire du clone. Le script vérifie d'abord tout, y compris la clé de Docker par son empreinte, puis :

- il monte le SSD sur `/srv/cloudos` et le disque USB sur `/media/cloudos-backup`, par leur UUID, dans `/etc/fstab` (copié avant en `/etc/fstab.cloudos.bak`) ;
- il installe les deux unités systemd : Docker attend le SSD, et `cloudos.service` relance la box à chaque démarrage. Avec un SSD de données, le dossier de Docker va sur le SSD (`/srv/cloudos/docker`) : la base de données n'est jamais sur la carte SD ;
- il installe Docker depuis le dépôt apt de Docker ;
- il ajoute votre utilisateur au groupe `docker`, puis lance `scripts/box init` et `scripts/box up` en votre nom. Le groupe `docker` donne les droits de root sur la machine, comme `sudo` : réservez ce compte à l'admin de la box.

Le premier build est long : il compile ffmpeg et télécharge environ 1 Go de modèles, chacun vérifié par son SHA-256. Sa durée sur le Pi 5 est à mesurer (dernière ligne du tableau). Relancé, le script ne change rien de plus.

À la fin, **déconnectez-vous puis reconnectez-vous** une fois, pour que votre utilisateur soit dans le groupe `docker`. La box tourne alors sur `http://localhost:8480`, sur la machine seulement.

### 4. Tailscale, la seule porte d'entrée

Le cœur n'écoute que sur `127.0.0.1` : on ne joint la box que par Tailscale, chez soi comme dehors. Suivez [`access.md`](access.md), sections 1 et 2 : le compte, puis dans `box.env` :

```bash
CLOUDOS_TAILSCALE=1
TS_AUTHKEY=tskey-auth-…
```

```bash
scripts/box up
scripts/box tailscale
```

Désactivez l'expiration de la clé de la box dans la console Tailscale, puis retirez `TS_AUTHKEY` de `box.env`.

### 5. Le compte admin

Depuis un appareil du réseau Tailscale ([`access.md`](access.md), section 3), ouvrez `https://cloudos.<tailnet>.ts.net`. La première visite propose de créer le compte admin.

### 6. Le mot de passe SMB

Pour le disque réseau du Finder : `CLOUDOS_SMB=1` dans `box.env`, puis `scripts/box up`. Tirez ensuite votre mot de passe SMB dans **Réglages**. Il est différent du mot de passe du compte.

### 7. La sauvegarde

```bash
scripts/box backup-init /media/cloudos-backup/cloudos-backup
```

La commande affiche la clé de la sauvegarde **une seule fois**. Notez-la hors de la box, sur papier ou dans un gestionnaire de mots de passe : sans elle, la sauvegarde est illisible. La sauvegarde tourne ensuite chaque nuit ([`architecture/backup.md`](architecture/backup.md)).

### 8. Redémarrer

```bash
sudo reboot
```

Puis vérifiez que la box revient seule :

```bash
systemctl status cloudos.service   # active (exited)
scripts/box ps                     # db, core et worker « healthy »
scripts/box run --rm core verify   # ok …, problems 0
```

## Ensuite

- **Mettre à jour** : `scripts/box update`. La mise à jour commence par une sauvegarde, et s'arrête si elle échoue. Un redémarrage ne met jamais à jour : `cloudos.service` relance les images déjà construites (`scripts/box up --no-build`).
- **Restaurer** sur une box neuve : [`architecture/backup.md`](architecture/backup.md), « Restaurer sur une box neuve, pas à pas ». Installez d'abord la box neuve avec ce guide, jusqu'à l'étape 3.
- **La box ne démarre plus parce que le SSD est absent.** C'est voulu : sans le SSD, Docker ne démarre pas, et rien n'est écrit sur la carte SD à sa place. `systemctl status docker` dit alors « Dependency failed », et `findmnt /srv/cloudos` ne montre rien. Rebranchez le SSD, puis `sudo reboot`, ou `sudo mount /srv/cloudos && sudo systemctl start docker cloudos`. Ne créez jamais `/srv/cloudos/files` à la main.
- **Le disque de sauvegarde est absent.** La box tourne ; la sauvegarde de la nuit échoue proprement et le dit dans `scripts/box backup-status`. Aucun dépôt n'est recréé ailleurs.
- **Retirer l'installation.** Les données restent sur le SSD.

  ```bash
  sudo systemctl disable --now cloudos.service
  sudo rm /etc/systemd/system/cloudos.service /etc/systemd/system/docker.service.d/cloudos.conf
  sudo systemctl daemon-reload
  # Le dossier de Docker est sur le SSD (/etc/docker/daemon.json) : laissez ce
  # fichier tant que Docker garde ses volumes, la base comprise, dans /srv/cloudos/docker.
  # Puis retirez de /etc/fstab les lignes « Cloud OS » (deux par disque), ou
  # remettez /etc/fstab.cloudos.bak si fstab n'a pas changé depuis.
  ```

  Docker reste installé. `sudo apt-get remove docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin` le retire, sans toucher à ses images ni à ses volumes.

## Ce que `scripts/install` refuse

Chaque vérification passe avant la moindre écriture : un refus laisse la machine telle qu'elle était.

| Refus | Pourquoi |
|---|---|
| Un utilisateur autre que root | il écrit dans `/etc` |
| Un clone qui n'appartient pas à celui qui lance `sudo` | le groupe `docker` vaut root : il ne va qu'à l'admin |
| Un système autre que Debian (ou dérivé, comme Raspberry Pi OS) | le dépôt de Docker utilisé est celui de Debian |
| Un système 32 bits (`dpkg --print-architecture` autre que `arm64` ou `amd64`) | les images n'existent qu'en 64 bits |
| Un dépôt apt de Docker déjà déclaré ailleurs, ou le Docker de Debian (`docker.io`, `containerd`, `runc`…) | une seule source de paquets Docker, la sienne |
| Une clé de Docker dont l'empreinte n'est pas `9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88` | rien n'est installé depuis une source non vérifiée |
| Un UUID vide, ou qui n'a pas la forme `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` | un UUID vide ne veut jamais dire « pas de disque » |
| Un UUID que `blkid` ne connaît pas | le disque n'est pas branché, ou l'UUID est faux |
| Un disque sur la carte SD ou l'eMMC (`mmcblk`), ou celui du système | les données vont sur un disque à part |
| Sans `--data`, une machine qui démarre sur une carte SD | les données n'y vont jamais |
| Un disque qui n'est ni en ext4, ni en xfs, ni en btrfs | les liens durs de `storage` et les propriétaires Unix de la sauvegarde |
| Le même disque pour les données et la sauvegarde, même sur deux partitions, ou la sauvegarde sur le disque du système | une panne ne doit pas emporter les données et leur sauvegarde |
| Un point de montage déjà décrit dans `fstab` avec un autre disque, ou ce disque déjà monté ailleurs | ne jamais remplacer une ligne de `fstab` |
| Un point de montage qui contient des fichiers alors que rien n'y est monté | monter par-dessus les cacherait |
| Un point de montage, ou un dossier de la box sur un disque réutilisé, qui est un lien symbolique | le disque, ou le cœur, irait là où il pointe |
| Un autre disque déjà monté au point de montage | idem |
| Un `fstab` que `findmnt --verify` refuse | `fstab` reste tel quel |
| Un `box.env` qui garde les fichiers ailleurs que dans `/srv/cloudos/files` | `init` ne réécrit jamais `box.env` |

## Ce qui sort de la machine

Pendant l'installation et les mises à jour seulement : les dépôts de Debian et de Docker, GitHub, puis, pendant le build, les images de base, les modules Go, les paquets npm, les sources de ffmpeg et les modèles, chacun vérifié par son SHA-256. Ensuite, rien ne sort de la box par défaut ; Tailscale et la sauvegarde distante sont des choix explicites ([`access.md`](access.md), [`architecture/backup.md`](architecture/backup.md)).
