# Ouvrir la box hors de la maison

La box s'ouvre en HTTPS à `https://cloudos.<tailnet>.ts.net`, chez vous comme dehors : le web, l'app, le Finder (WebDAV et SMB). Aucun port n'est ouvert sur la box internet. Les appareils du foyer rejoignent un réseau privé Tailscale, chiffré de bout en bout. En option, Tailscale Funnel ouvre les liens de partage aux invités qui n'ont pas Tailscale, et rien d'autre.

Tout est éteint par défaut. Le fonctionnement technique est dans [`architecture/box.md`](architecture/box.md), section « Accès ».

## Ce qui sort de la box

- **Le serveur de coordination** de Tailscale, un service tiers, voit les appareils, leurs adresses et leurs clés publiques. Il ne voit pas vos fichiers : le trafic est chiffré entre vos appareils et la box.
- **Le nom de la box** (`cloudos.<tailnet>.ts.net`) est publié dans les journaux publics des certificats HTTPS.
- **Avec Funnel**, les invités passent par les relais de Tailscale. Leur trafic reste chiffré jusqu'à la box.

## 1. Le compte Tailscale

1. Créez un compte sur <https://login.tailscale.com>, avec l'identité du foyer (Google, Apple, Microsoft, GitHub…). Le plan gratuit suffit pour un foyer.
2. Dans la console d'administration, ouvrez **DNS** :
   - vérifiez que **MagicDNS** est activé (il l'est par défaut) ;
   - sous **HTTPS Certificates**, cliquez **Enable HTTPS**.
3. Le nom de votre réseau, `<tailnet>.ts.net`, est affiché en haut de la page **DNS**.

## 2. Relier la box

1. Dans la console, **Settings › Keys › Generate auth key**. Prenez une clé non réutilisable, valable un jour. Elle ne sert qu'une fois.
2. Sur la box, dans `box.env` :

   ```bash
   CLOUDOS_TAILSCALE=1
   TS_AUTHKEY=tskey-auth-…
   ```

3. Lancez la pile :

   ```bash
   scripts/box up
   ```

4. La box apparaît dans **Machines** sous le nom `cloudos`. Ouvrez son menu **…** et choisissez **Disable key expiry**. Sinon, Tailscale la déconnecte au bout de six mois.
5. Écrivez son adresse dans `box.env` et relancez la pile :

   ```bash
   scripts/box tailscale
   ```

   La commande affiche `the box is at https://cloudos.<tailnet>.ts.net`.
6. Retirez la ligne `TS_AUTHKEY` de `box.env`. La box garde son identité dans le volume `cloud-os-box_tailscale-state`.

Un autre nom que `cloudos` : `TS_HOSTNAME=<nom>` dans `box.env`, avant la première connexion.

## 3. Ajouter un Mac

1. Installez Tailscale (Mac App Store ou <https://tailscale.com/download>), puis connectez-vous avec le compte du foyer.
2. Ouvrez `https://cloudos.<tailnet>.ts.net` dans le navigateur.
3. Dans le Finder, **Aller › Se connecter au serveur** (⌘K) :
   - WebDAV, avec le mot de passe du compte Cloud OS : `https://cloudos.<tailnet>.ts.net/dav/` ;
   - SMB, avec le mot de passe SMB tiré dans Réglages, quand SMB est allumé (`CLOUDOS_SMB=1`) : `smb://<compte>@cloudos.<tailnet>.ts.net/drive`.

## 4. Ajouter un iPhone

1. Installez l'app Tailscale (App Store) et connectez-vous avec le compte du foyer.
2. Dans l'app Tailscale, ouvrez **Settings › VPN On Demand** et activez-le. La sauvegarde des photos de l'app Cloud OS joint ainsi la box même quand l'iPhone l'ouvre en arrière-plan.
3. Dans l'app Cloud OS, l'adresse de la box est `cloudos.<tailnet>.ts.net`. L'app ajoute `https://` d'elle-même.

## 5. Retirer un appareil

- **Un appareil perdu ou donné** : dans la console, **Machines**, ouvrez le menu **…** de l'appareil, puis **Remove**. Il n'atteint plus la box, même s'il a encore une session Cloud OS. Fermez aussi ses sessions en changeant le mot de passe du compte, et révoquez son mot de passe SMB dans Réglages.
- **Une personne** : **Users**, puis **Remove** sur son compte. Ses appareils partent avec elle.

Un compte Cloud OS et un appareil Tailscale sont deux choses distinctes. Tailscale décide qui atteint la box, Cloud OS ce que chacun y voit.

## 6. Liens de partage publics (Funnel)

Sans Funnel, un lien de partage ne s'ouvre que depuis un appareil du réseau Tailscale. Avec Funnel, un invité l'ouvre de n'importe où. Seules les pages des liens de partage sont publiques, sur le port 8443 : ni la connexion, ni les fichiers, ni WebDAV, ni SMB.

1. Dans la console, **Access controls** : la politique doit donner l'attribut `funnel`. Une politique récente le donne déjà. Sinon, ajoutez :

   ```json
   "nodeAttrs": [
     { "target": ["autogroup:member"], "attr": ["funnel"] }
   ]
   ```

2. Dans `box.env`, ajoutez `CLOUDOS_FUNNEL=1`, puis :

   ```bash
   scripts/box tailscale
   ```

   Les liens créés ensuite commencent par `https://cloudos.<tailnet>.ts.net:8443/s/`.
3. Pour refermer : retirez `CLOUDOS_FUNNEL=1`, puis relancez `scripts/box tailscale`. Les liens déjà donnés ne s'ouvrent plus qu'au sein du réseau Tailscale.

## Tout éteindre

- **Couper l'accès** : retirez `CLOUDOS_TAILSCALE=1` de `box.env`, puis `scripts/box up` arrête le service `tailscale`. La box ne parle plus à Tailscale.
- **Quitter le réseau Tailscale** : retirez aussi la box dans **Machines**, puis supprimez son identité avec `docker volume rm cloud-os-box_tailscale-state`, la pile arrêtée. Pour la relier de nouveau, il faut une nouvelle clé.
