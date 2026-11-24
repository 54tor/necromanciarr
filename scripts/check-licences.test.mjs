import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { after, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('./check-licences.mjs', import.meta.url))
const racine = path.dirname(path.dirname(script))
const dossiersTemporaires = []

after(() => {
  for (const dossier of dossiersTemporaires) rmSync(dossier, { recursive: true, force: true })
})

// Construit un faux dossier web/ propre ; `modifier` y ajoute le défaut à tester.
function fabriquerWeb(modifier = () => {}) {
  const web = mkdtempSync(path.join(tmpdir(), 'licences-'))
  dossiersTemporaires.push(web)
  mkdirSync(path.join(web, 'src'))
  mkdirSync(path.join(web, 'public'))
  writeFileSync(path.join(web, 'index.html'), '<html></html>')
  writeFileSync(path.join(web, 'assets-allowlist.json'), '[]')
  writeFileSync(
    path.join(web, 'package-lock.json'),
    JSON.stringify({
      packages: {
        '': { name: 'fixture' },
        'node_modules/ok': { version: '1.0.0', license: 'MIT' },
        'node_modules/outil': { version: '1.0.0', license: 'ISC', dev: true },
      },
    }),
  )
  modifier(web)
  return web
}

function ajouterPaquet(web, nom, entree) {
  const lock = path.join(web, 'package-lock.json')
  const contenu = JSON.parse(readFileSync(lock, 'utf8'))
  contenu.packages[`node_modules/${nom}`] = entree
  writeFileSync(lock, JSON.stringify(contenu))
}

function lancer(web) {
  return spawnSync('node', [script, web], { encoding: 'utf8' })
}

function attendreEchec(web, fragmentAttendu) {
  const resultat = lancer(web)
  assert.equal(resultat.status, 1, resultat.stdout + resultat.stderr)
  assert.match(resultat.stdout + resultat.stderr, fragmentAttendu)
}

test('fails on a GPL production dependency', () => {
  const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'copyleft', { version: '1.0.0', license: 'GPL-3.0-only' }))
  attendreEchec(web, /copyleft.*GPL-3\.0-only/)
})

test('fails on a LGPL dev dependency', () => {
  const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'faible', { version: '1.0.0', license: 'LGPL-2.1', dev: true }))
  attendreEchec(web, /faible.*LGPL-2\.1/)
})

test('fails on a missing licence', () => {
  const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'sans-licence', { version: '1.0.0' }))
  attendreEchec(web, /sans-licence.*(missing|absente)/i)
})

test('fails on a Radarr marker in src', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'src', 'a.css'), '.radarr-poster { color: red }'))
  attendreEchec(web, /src[\\/]a\.css.*radarr/i)
})

test('fails on a Servarr marker in index.html', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'index.html'), '<!-- servarr -->'))
  attendreEchec(web, /index\.html.*servarr/i)
})

const empreinte = (contenu) => createHash('sha256').update(contenu).digest('hex')

function declarerAsset(dossier, chemin, contenu, contenuDeclare = contenu) {
  mkdirSync(path.dirname(path.join(dossier, chemin)), { recursive: true })
  writeFileSync(path.join(dossier, chemin), contenu)
  writeFileSync(
    path.join(dossier, 'assets-allowlist.json'),
    JSON.stringify([{ path: chemin, origin: 'Necromanciarr', licence: 'MIT', sha256: empreinte(contenuDeclare) }]),
  )
}

test('fails on a non-allowlisted image', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'public', 'logo.png'), 'x'))
  attendreEchec(web, /public[\\/]logo\.png|public\/logo\.png/)
})

for (const extension of ['avif', 'bmp', 'eot', 'tiff', 'pdf', 'woff2', 'svg', 'bin']) {
  test(`fails on a non-allowlisted .${extension} file`, () => {
    const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'public', `fichier.${extension}`), 'x'))
    attendreEchec(web, new RegExp(`fichier\\.${extension}`))
  })
}

test('passes on the build marker .placeholder', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'public', '.placeholder'), ''))
  assert.equal(lancer(web).status, 0)
})

test('passes on an allowlisted image with the right hash', () => {
  const web = fabriquerWeb((dossier) => declarerAsset(dossier, 'public/logo.svg', '<svg/>'))
  assert.equal(lancer(web).status, 0)
})

test('fails when an allowlisted path has different content than its hash', () => {
  const web = fabriquerWeb((dossier) => declarerAsset(dossier, 'public/logo.svg', '<svg>autre</svg>', '<svg/>'))
  attendreEchec(web, /logo\.svg.*(hash|sha256)/i)
})

test('fails on an allowlist entry without sha256', () => {
  const web = fabriquerWeb((dossier) => {
    writeFileSync(path.join(dossier, 'public', 'logo.svg'), '<svg/>')
    writeFileSync(path.join(dossier, 'assets-allowlist.json'), JSON.stringify([{ path: 'public/logo.svg', origin: 'x', licence: 'MIT' }]))
  })
  attendreEchec(web, /logo\.svg/)
})

test('fails on a file named after an arr application', () => {
  const web = fabriquerWeb((dossier) => declarerAsset(dossier, 'public/radarr-icon.svg', '<svg/>'))
  attendreEchec(web, /radarr-icon\.svg/)
})

test('passes on a locale string naming Radarr', () => {
  const web = fabriquerWeb((dossier) => {
    mkdirSync(path.join(dossier, 'src', 'locales'))
    writeFileSync(path.join(dossier, 'src', 'locales', 'fr.json'), '{"sources":"Radarr, Sonarr et Prowlarr"}')
  })
  assert.equal(lancer(web).status, 0)
})

test('passes on a spec file naming Radarr', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'src', 'a.spec.ts'), "it('Radarr, Servarr', () => {})"))
  assert.equal(lancer(web).status, 0)
})

test('fails on a CSS class named after an arr application', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'src', 'a.css'), '.radarr-btn { color: red }'))
  attendreEchec(web, /a\.css.*radarr/i)
})

test('fails on an import path naming an arr application', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'src', 'a.ts'), "import x from './sonarr/theme'"))
  attendreEchec(web, /a\.ts.*sonarr/i)
})

test('fails on a GNU General Public License header in src', () => {
  const web = fabriquerWeb((dossier) =>
    writeFileSync(path.join(dossier, 'src', 'a.ts'), '/* This program is free software: GNU General Public License v3 */'),
  )
  attendreEchec(web, /a\.ts.*(GNU|GPL)/i)
})

test('passes on a plain mention of Prowlarr in a component', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'src', 'a.vue'), '<template><p>Prowlarr</p></template>'))
  assert.equal(lancer(web).status, 0)
})

const licencesRefusees = [
  ['UNLICENSED', 'UNLICENSED'],
  ['SEE LICENSE IN', 'SEE LICENSE IN LICENSE.txt'],
  ['an OR expression with a GPL alternative', '(MIT OR GPL-3.0)'],
  ['an AND expression with a non-free licence', 'MIT AND CC-BY-3.0'],
  ['GPL-2.0-only', 'GPL-2.0-only'],
  ['lowercase gpl', 'gpl-3.0-or-later'],
  ['CC-BY-NC-4.0', 'CC-BY-NC-4.0'],
  ['BUSL-1.1', 'BUSL-1.1'],
  ['an unknown licence', 'Totally-Made-Up-1.0'],
]
for (const [nom, licence] of licencesRefusees) {
  test(`fails on ${nom}`, () => {
    const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'suspect', { version: '1.0.0', license: licence }))
    attendreEchec(web, /suspect/)
  })
}

const licencesAcceptees = [
  ['Apache-2.0 WITH LLVM-exception', 'Apache-2.0 WITH LLVM-exception'],
  ['lowercase mit', 'mit'],
  ['an OR expression of allowed licences', '(MIT OR Apache-2.0)'],
  ['BlueOak-1.0.0', 'BlueOak-1.0.0'],
]
for (const [nom, licence] of licencesAcceptees) {
  test(`passes on ${nom}`, () => {
    const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'permissif', { version: '1.0.0', license: licence }))
    assert.equal(lancer(web).status, 0)
  })
}

test('fails on MPL-2.0 in production', () => {
  const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'mozilla', { version: '1.0.0', license: 'MPL-2.0' }))
  attendreEchec(web, /mozilla/)
})

test('passes on MPL-2.0 in development', () => {
  const web = fabriquerWeb((dossier) => ajouterPaquet(dossier, 'mozilla', { version: '1.0.0', license: 'MPL-2.0', dev: true }))
  assert.equal(lancer(web).status, 0)
})

test('passes on a justified override', () => {
  const web = fabriquerWeb((dossier) => {
    ajouterPaquet(dossier, 'double', { version: '1.0.0', license: '(MIT OR GPL-3.0)' })
    writeFileSync(
      path.join(dossier, 'licences-overrides.json'),
      JSON.stringify([{ package: 'double', justification: 'MIT chosen under the SPDX OR' }]),
    )
  })
  assert.equal(lancer(web).status, 0)
})

test('fails on an override without justification', () => {
  const web = fabriquerWeb((dossier) => {
    ajouterPaquet(dossier, 'double', { version: '1.0.0', license: '(MIT OR GPL-3.0)' })
    writeFileSync(path.join(dossier, 'licences-overrides.json'), JSON.stringify([{ package: 'double' }]))
  })
  attendreEchec(web, /double/)
})

test('passes on a clean fixture', () => {
  assert.equal(lancer(fabriquerWeb()).status, 0)
})

test('passes on the current tree', () => {
  const resultat = lancer(path.join(racine, 'web'))
  assert.equal(resultat.status, 0, resultat.stdout + resultat.stderr)
})
