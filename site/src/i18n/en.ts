// Every string the English page shows. fr.ts has the same keys.
const en = {
  locale: 'en-GB',
  langName: 'English version',
  meta: {
    title: 'Cloud OS · Your cloud, at home',
    description:
      'Cloud OS: a small box that keeps your files, photos and documents at home, with an AI search that runs on the box.',
  },
  a11y: {
    skip: 'Skip to content',
    mainNav: 'Main',
    theme: 'Switch theme',
  },
  nav: { box: 'The box', features: 'Features', screen: 'The screen', install: 'Install it' },
  badge: { wip: 'V1 · finishing', soon: 'soon', ok: 'available', wipShort: 'finishing' },
  // The layers of the box, from the top: their names in the 3D callouts.
  layers: ['Files', 'Photos', 'Search', 'Access', 'Backup'],
  hero: {
    label: 'A small box for your home',
    title: [
      ['Your', 'cloud,'],
      ['at', 'home.'],
    ],
    lead: 'Cloud OS keeps your files, photos and documents on a box in your home. Nothing leaves it by default. No cloud subscription.',
    primary: 'Open the box',
    secondary: 'Install it',
    dockLabel: 'What the box holds',
    dock: ['Files', 'Photos', 'Documents', 'Ask'],
  },
  box: {
    label: 'The box',
    title: 'What is yours stays at home.',
    lead: 'The box sits in your home, and your files live on its disk. No outside service gets them without your consent.',
    points: [
      {
        title: 'At home',
        text: 'Nothing leaves the box by default. Each way out is turned on separately, and only if you want it.',
      },
      {
        title: 'Plain files',
        text: 'Your files are kept in ordinary folders, under their real names. You can still read them without Cloud OS.',
      },
      {
        title: 'Never lost',
        text: 'A deleted file goes to the trash. A replaced file keeps its earlier versions. Every file is checked for integrity.',
      },
    ],
    tree: {
      docDir: 'Documents/',
      docFile: 'flat-lease.pdf',
      photoDir: 'Photos/2026/',
      photoFile: 'beach.heic',
      trash: 'the trash, 30 days',
      versions: 'earlier versions',
    },
    caption: 'The box’s disk, as it is. No custom format.',
  },
  files: {
    label: 'Layer 1 · Files',
    title: 'The Drive, on all your devices.',
    lead: 'Your folders open in the Mac’s Finder and in Windows File Explorer, like one more disk. They also open in the browser and on your phone.',
    points: [
      { title: 'Finder and File Explorer', text: 'They connect to the box over WebDAV or SMB.' },
      { title: 'Large uploads resume', text: 'An interrupted upload picks up where it stopped.' },
      { title: 'Nothing twice', text: 'A file already on the box is never sent again.' },
      { title: 'The web and the phone', text: 'The same folders, in the browser and in the app.' },
    ],
    figure: 'The Cloud OS Files window, with an upload resuming after a dropout',
    win: 'Files',
    segs: ['Folders', 'Timeline'],
    rows: [
      { kind: 'folder', name: 'Documents', meta: '124 items' },
      { kind: 'folder', name: 'Photos', meta: '3,812 items' },
      { kind: 'folder', name: 'Work', meta: '58 items' },
      { kind: 'doc', name: 'Kitchen quote.pdf', meta: '1.2 MB' },
    ],
    upload: { name: 'Holidays 2026.mov', state: 'resumed after a dropout' },
    chipsLabel: 'Where the Drive opens',
    chips: ['Finder · WebDAV', 'File Explorer · SMB', 'Browser', 'Phone'],
  },
  photos: {
    label: 'Layer 2 · Photos',
    title: 'Your photos, sorted by the day they were taken.',
    lead: 'Photos replaces Google Photos and iCloud. The phone app sends your photos and videos to the box on its own, in the background.',
    points: [
      { title: 'Timeline and albums', text: 'All your photos day by day, and the albums you put together.' },
      { title: 'A map', text: 'Your geotagged photos on a map, with its map tiles stored on the box.' },
      { title: 'HEIC and videos', text: 'iPhone HEIC photos and videos are supported.' },
    ],
    figure: 'The Cloud OS Photos window, with the photos of one day',
    win: 'Photos',
    segs: ['Photos', 'Albums', 'Map'],
    day: 'Saturday 14 November',
    dayCount: '8 photos',
    phone: 'Automatic phone backup, in the background',
  },
  search: {
    label: 'Layer 3 · Search',
    title: 'Ask, and the box finds it.',
    lead: 'One search box: ⌘K, “Ask”. Type in your own words. It understands the meaning, even without the file name.',
    points: [
      { title: 'By meaning', text: '“flat lease” finds the rental agreement.' },
      { title: 'Text in images', text: 'Scanned documents and photos of papers are read (OCR).' },
      { title: 'Documents in order', text: 'Each document gets its category, without you doing a thing.' },
      { title: 'The AI stays on the box', text: 'Everything runs on the box. No outside AI reads your documents.' },
    ],
    figure: 'The Cloud OS Ask palette: the search “flat lease” finds the flat’s rental agreement',
    query: 'flat lease',
    esc: 'Esc',
    kinds: ['All', 'Documents', 'Photos'],
    results: {
      first: {
        name: 'tenancy-agreement.pdf',
        meta: 'Documents · Contracts',
        before: '… the landlord and the tenant have agreed to this ',
        match: 'rental agreement',
        after: ' …',
      },
      second: { name: 'Move-in inventory scan.jpg', meta: 'Text read in the image' },
      third: { name: 'Home insurance.pdf', meta: 'Documents · Contracts' },
    },
    catsLabel: 'Example categories',
    cats: ['Invoices', 'Contracts', 'Taxes', 'Health', 'Bank'],
  },
  access: {
    label: 'Layer 4 · Access',
    title: 'At home, and anywhere else.',
    lead: 'Open your box from anywhere, over HTTPS, without opening a port on your internet router. Access goes through Tailscale.',
    points: [
      { title: 'Your devices only', text: 'Only the devices on your Tailscale network can see the box.' },
      { title: 'Links for your guests', text: 'A public share link, read only. Off by default.' },
    ],
    figure: 'The box’s HTTPS address on the Tailscale network, with public links turned off',
    domain: '.your-tailnet.ts.net',
    devices: ['MacBook', 'iPhone', 'Office PC'],
    shareTitle: 'Public links',
    shareText: 'For your guests, read only',
  },
  backup: {
    label: 'Layer 5 · Backup',
    title: 'A backup every night, tested every week.',
    lead: 'Every night, the box saves an encrypted backup to a USB disk. Every week, it checks on its own that this backup can be restored.',
    points: [
      { title: 'Encrypted', text: 'A lost backup disk gives away nothing of your files.' },
      { title: 'Restore tested', text: 'An automatic restore test, every week.' },
      { title: 'A copy elsewhere, if you want', text: 'Optionally, a remote copy, encrypted as well.' },
    ],
    figure: 'A week of nightly backups, and the Sunday restore test',
    widget: 'Box status',
    summary: 'Last night’s backup succeeded · restore test on Sunday',
    nights: ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'],
    disk: 'USB disk, encrypted',
    tested: 'Restore tested on Sunday',
    remote: 'Remote copy: optional',
  },
  desk: {
    label: 'The box’s screen',
    title: 'A desktop, in your browser.',
    lead: 'The web interface looks like a computer: a menu bar, a dock, and windows you can move and enlarge. On a phone, the front window fills the screen.',
    figure:
      'The Cloud OS desktop: the menu bar with Ask, a Photos window in front of a Files window, and the app dock',
    ask: 'Ask',
    user: 'Camille',
    // What the mock clock shows before the script sets the time of the visit.
    now: { short: 'Sat 21 Nov 14:32', time: '14:32', day: 'Saturday 21 November' },
    days: [
      { day: 'Saturday 14 November', count: '8 photos' },
      { day: 'Friday 13 November', count: '4 photos' },
    ],
    notes: [
      { title: '⌘K everywhere', text: 'Ask opens from any window.', wip: false },
      {
        title: 'The box’s status, in its own app',
        text: 'The “Box status” app shows the last backup and warns the administrator if it fails.',
        wip: true,
      },
      { title: 'One app per tile', text: 'Files, Photos, Documents, Shares, Trash and Settings, in the dock.', wip: false },
    ],
  },
  soon: {
    label: 'Next',
    title: 'What is coming to the box.',
    items: [
      { title: 'Libraries', text: 'Your films, music and books, next to your photos.' },
      { title: 'An image ready to flash', text: 'Write it to a card, and the box starts with Cloud OS.' },
      {
        title: 'A/B updates',
        text: 'The system updates on a second copy, and goes back to the old one if something goes wrong.',
      },
      { title: 'Guided first start', text: 'An assistant that sets up the box, step by step.' },
    ],
  },
  install: {
    label: 'Install it',
    title: 'A box at home, on your own hardware.',
    lead: 'Cloud OS runs today on a Raspberry Pi 5 or on any 64-bit Debian machine, and starts with a single command. Here is what you need.',
    needs: [
      {
        term: 'Machine',
        text: 'Raspberry Pi 5, or any 64-bit Debian machine',
        note: 'An ARM64 or x86-64 processor, a mini PC for example.',
      },
      {
        term: 'Memory',
        text: 'Recommended memory: measured on the Raspberry Pi 5, published with the V1',
        note: 'The AI search keeps its models in memory.',
      },
      { term: 'Data', text: 'A disk for your files', note: 'A separate disk, never the Raspberry Pi’s SD card.' },
      { term: 'Backup', text: 'A USB disk', note: 'For the encrypted backup every night.' },
      { term: 'Software', text: 'Docker, with Compose 2.24 or later', note: '' },
      { term: 'Language', text: 'The interface is in French.', note: '' },
    ],
    status: {
      ok: 'Drive, Photos and the phone app, search and documents.',
      wip: 'Access from outside, the nightly backup.',
      soon: 'Libraries, an image ready to flash, A/B updates, guided first start.',
    },
  },
  footer: {
    name: 'Working name. Software in development.',
    trackers: 'This page has no trackers.',
    code: 'The code on GitHub',
    licence: 'MIT OR Apache-2.0 licence',
    thirdParty: 'Third-party licences',
  },
};

export default en;
