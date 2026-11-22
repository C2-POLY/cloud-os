// Every string the French page shows. en.ts has the same keys.
const fr = {
  locale: 'fr-FR',
  langName: 'Version française',
  meta: {
    title: 'Cloud OS · Votre cloud, chez vous',
    description:
      'Cloud OS : une petite box qui garde vos fichiers, vos photos et vos documents chez vous, avec une recherche IA qui tourne sur la box.',
  },
  a11y: {
    skip: 'Aller au contenu',
    mainNav: 'Navigation principale',
    theme: 'Changer de thème',
  },
  nav: { box: 'La box', features: 'Fonctions', screen: "L'écran", install: "L'installer" },
  badge: { wip: 'V1 · en finition', soon: 'bientôt', ok: 'disponible', wipShort: 'en finition' },
  // The layers of the box, from the top: their names in the 3D callouts.
  layers: ['Fichiers', 'Photos', 'Recherche', 'Accès', 'Sauvegarde'],
  hero: {
    label: 'Une petite box pour la maison',
    title: [
      ['Votre', 'cloud,'],
      ['chez', 'vous.'],
    ],
    lead: "Cloud OS garde vos fichiers, vos photos et vos documents sur une box posée chez vous. Rien n'en sort par défaut. Pas d'abonnement à un cloud.",
    primary: 'Ouvrir la box',
    secondary: "L'installer",
    dockLabel: 'Ce que contient la box',
    dock: ['Fichiers', 'Photos', 'Documents', 'Demander'],
  },
  box: {
    label: 'La box',
    title: 'Ce qui est à vous reste chez vous.',
    lead: 'La box est posée chez vous, et vos fichiers vivent sur son disque. Aucun service extérieur ne les reçoit sans votre accord.',
    points: [
      {
        title: 'Chez vous',
        text: "Rien ne sort de la box par défaut. Chaque ouverture vers l'extérieur s'active à part, seulement si vous la voulez.",
      },
      {
        title: 'En clair',
        text: 'Vos fichiers sont rangés dans des dossiers normaux, sous leurs vrais noms. Ils restent lisibles même sans Cloud OS.',
      },
      {
        title: 'Jamais perdus',
        text: "Un fichier supprimé part à la corbeille. Un fichier remplacé garde ses versions d'avant. L'intégrité de chaque fichier est vérifiée.",
      },
    ],
    tree: {
      docDir: 'Documents/',
      docFile: 'bail-appartement.pdf',
      photoDir: 'Photos/2026/',
      photoFile: 'plage.heic',
      trash: 'la corbeille, 30 jours',
      versions: "les versions d'avant",
    },
    caption: "Le disque de la box, tel qu'il est. Aucun format maison.",
  },
  files: {
    label: 'Couche 1 · Fichiers',
    title: 'Le Drive, depuis tous vos appareils.',
    lead: "Vos dossiers s'ouvrent dans le Finder du Mac et dans l'Explorateur de Windows, comme un disque de plus. Ils s'ouvrent aussi dans le navigateur et sur le téléphone.",
    points: [
      { title: 'Finder et Explorateur', text: "La box s'y connecte en WebDAV ou en SMB." },
      { title: 'Les gros envois reprennent', text: "Un envoi coupé repart là où il s'était arrêté." },
      { title: 'Rien en double', text: "Un fichier déjà présent sur la box n'est jamais renvoyé." },
      { title: 'Le web et le téléphone', text: "Les mêmes dossiers, dans le navigateur et dans l'app." },
    ],
    figure: 'La fenêtre Fichiers de Cloud OS, avec un envoi qui reprend après une coupure',
    win: 'Fichiers',
    segs: ['Dossiers', 'Chronologie'],
    rows: [
      { kind: 'folder', name: 'Documents', meta: '124 éléments' },
      { kind: 'folder', name: 'Photos', meta: '3 812 éléments' },
      { kind: 'folder', name: 'Travail', meta: '58 éléments' },
      { kind: 'doc', name: 'Devis cuisine.pdf', meta: '1,2 Mo' },
    ],
    upload: { name: 'Vacances 2026.mov', state: 'reprise après coupure' },
    chipsLabel: "Où s'ouvre le Drive",
    chips: ['Finder · WebDAV', 'Explorateur · SMB', 'Navigateur', 'Téléphone'],
  },
  photos: {
    label: 'Couche 2 · Photos',
    title: 'Vos photos, rangées par date de prise de vue.',
    lead: "Photos remplace Google Photos et iCloud. L'app du téléphone envoie vos photos et vos vidéos sur la box toute seule, en arrière-plan.",
    points: [
      { title: 'Chronologie et albums', text: 'Toutes vos photos jour par jour, et les albums que vous composez.' },
      { title: 'Une carte', text: 'Vos photos géolocalisées sur une carte, dont les fonds sont stockés sur la box.' },
      { title: 'HEIC et vidéos', text: "Les photos HEIC de l'iPhone et les vidéos sont prises en charge." },
    ],
    figure: "La fenêtre Photos de Cloud OS, avec les photos d'une journée",
    win: 'Photos',
    segs: ['Photos', 'Albums', 'Carte'],
    day: 'Samedi 14 novembre',
    dayCount: '8 photos',
    phone: 'Sauvegarde automatique du téléphone, en arrière-plan',
  },
  search: {
    label: 'Couche 3 · Recherche',
    title: 'Demandez, la box retrouve.',
    lead: 'Une seule case de recherche : ⌘K, « Demander ». Écrivez avec vos mots. Elle comprend le sens, même sans le nom du fichier.',
    points: [
      { title: 'Par le sens', text: '« bail appart » trouve le contrat de location.' },
      { title: 'Le texte des images', text: 'Les documents scannés et les photos de papiers sont lus (OCR).' },
      { title: 'Des documents rangés', text: 'Chaque document reçoit sa catégorie, sans que vous y touchiez.' },
      { title: "L'IA reste sur la box", text: 'Tout tourne sur la box. Aucune IA extérieure ne lit vos documents.' },
    ],
    figure: "La palette Demander de Cloud OS : la recherche « bail appart » trouve le bail de l'appartement",
    query: 'bail appart',
    esc: 'Échap',
    kinds: ['Tout', 'Documents', 'Photos'],
    results: {
      first: {
        name: 'bail-appartement.pdf',
        meta: 'Documents · Contrats',
        before: '… entre les soussignés, il a été convenu le présent ',
        match: 'contrat de location',
        after: ' …',
      },
      second: { name: 'Scan état des lieux.jpg', meta: "Texte lu dans l'image" },
      third: { name: 'Assurance habitation.pdf', meta: 'Documents · Contrats' },
    },
    catsLabel: 'Exemples de catégories',
    cats: ['Factures', 'Contrats', 'Impôts', 'Santé', 'Banque'],
  },
  access: {
    label: 'Couche 4 · Accès',
    title: 'Chez vous, et partout ailleurs.',
    lead: "Ouvrez votre box depuis n'importe où, en HTTPS, sans ouvrir de port sur votre box internet. L'accès passe par Tailscale.",
    points: [
      { title: 'Vos appareils seulement', text: "La box n'est visible que des appareils de votre réseau Tailscale." },
      { title: 'Des liens pour vos invités', text: 'Un lien public de partage, en lecture seule. Désactivé par défaut.' },
    ],
    figure: "L'adresse HTTPS de la box sur le réseau Tailscale, et les liens publics désactivés",
    domain: '.votre-reseau.ts.net',
    devices: ['MacBook', 'iPhone', 'PC du bureau'],
    shareTitle: 'Liens publics',
    shareText: 'Pour vos invités, en lecture seule',
  },
  backup: {
    label: 'Couche 5 · Sauvegarde',
    title: 'Une sauvegarde chaque nuit, testée chaque semaine.',
    lead: 'Chaque nuit, la box enregistre une sauvegarde chiffrée sur un disque USB. Chaque semaine, elle vérifie toute seule que cette sauvegarde se restaure.',
    points: [
      { title: 'Chiffrée', text: 'Un disque de sauvegarde égaré ne livre rien de vos fichiers.' },
      { title: 'Restauration testée', text: 'Un test de restauration automatique, chaque semaine.' },
      { title: 'Une copie ailleurs, si vous voulez', text: 'En option, une copie distante, chiffrée elle aussi.' },
    ],
    figure: 'Une semaine de sauvegardes de nuit, et le test de restauration du dimanche',
    widget: 'État de la box',
    summary: 'Sauvegarde de cette nuit réussie · test de restauration dimanche',
    nights: ['lun.', 'mar.', 'mer.', 'jeu.', 'ven.', 'sam.', 'dim.'],
    disk: 'Disque USB, chiffré',
    tested: 'Restauration testée dimanche',
    remote: 'Copie distante : en option',
  },
  desk: {
    label: "L'écran de la box",
    title: 'Un bureau, dans votre navigateur.',
    lead: "L'interface web ressemble à un ordinateur : une barre de menus, un dock et des fenêtres que l'on déplace et que l'on agrandit. Sur le téléphone, la fenêtre de devant prend tout l'écran.",
    figure:
      'Le bureau de Cloud OS : la barre de menus avec Demander, une fenêtre Photos devant une fenêtre Fichiers, et le dock des applications',
    ask: 'Demander',
    user: 'Camille',
    // What the mock clock shows before the script sets the time of the visit.
    now: { short: 'sam. 21 nov. 14:32', time: '14:32', day: 'Samedi 21 novembre' },
    days: [
      { day: 'Samedi 14 novembre', count: '8 photos' },
      { day: 'Vendredi 13 novembre', count: '4 photos' },
    ],
    notes: [
      { title: '⌘K partout', text: "Demander s'ouvre depuis n'importe quelle fenêtre.", wip: false },
      {
        title: "L'état de la box, à part",
        text: "L'app « État de la box » montre la dernière sauvegarde et prévient l'administrateur si elle échoue.",
        wip: true,
      },
      { title: 'Une app par tuile', text: 'Fichiers, Photos, Documents, Partages, Corbeille et Réglages, dans le dock.', wip: false },
    ],
  },
  soon: {
    label: 'Ensuite',
    title: 'Ce qui arrive dans la box.',
    items: [
      { title: 'Bibliothèques', text: 'Vos films, votre musique et vos livres, à côté de vos photos.' },
      { title: 'Une image prête à flasher', text: 'Une carte à graver, et la box démarre avec Cloud OS.' },
      {
        title: 'Mises à jour A/B',
        text: "Le système se met à jour sur une seconde copie, et revient à l'ancienne en cas de problème.",
      },
      { title: 'Premier démarrage guidé', text: 'Un assistant pour mettre la box en route, pas à pas.' },
    ],
  },
  install: {
    label: "L'installer",
    title: 'Une box chez vous, sur votre matériel.',
    lead: "Cloud OS tourne aujourd'hui sur un Raspberry Pi 5 ou sur toute machine Debian 64 bits, et se lance d'une seule commande. Voici ce qu'il faut.",
    needs: [
      {
        term: 'Machine',
        text: 'Raspberry Pi 5, ou toute machine Debian 64 bits',
        note: 'Processeur ARM64 ou x86-64, un mini PC par exemple.',
      },
      {
        term: 'Mémoire',
        text: 'Mémoire conseillée : mesurée sur le Raspberry Pi 5, publiée avec la V1',
        note: 'La recherche IA garde ses modèles en mémoire.',
      },
      { term: 'Données', text: 'Un disque pour vos fichiers', note: 'Un disque à part, jamais la carte SD du Raspberry Pi.' },
      { term: 'Sauvegarde', text: 'Un disque USB', note: 'Pour la sauvegarde chiffrée de chaque nuit.' },
      { term: 'Logiciel', text: 'Docker, avec Compose 2.24 ou plus récent', note: '' },
    ],
    status: {
      ok: "Drive, Photos et l'app du téléphone, recherche et documents.",
      wip: "Accès depuis l'extérieur, sauvegarde de nuit.",
      soon: 'Bibliothèques, image prête à flasher, mises à jour A/B, premier démarrage guidé.',
    },
  },
  footer: {
    name: 'Nom de travail. Logiciel en développement.',
    trackers: "Cette page n'a aucun traceur.",
    code: 'Le code sur GitHub',
    licence: 'Licence MIT ou Apache-2.0',
    thirdParty: 'Licences tierces',
  },
};

export default fr;
