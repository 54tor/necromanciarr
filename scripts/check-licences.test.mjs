import assert from 'node:assert/strict'
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

test('fails on a non-allowlisted image', () => {
  const web = fabriquerWeb((dossier) => writeFileSync(path.join(dossier, 'public', 'logo.png'), 'x'))
  attendreEchec(web, /public[\\/]logo\.png/)
})

test('passes on an allowlisted image', () => {
  const web = fabriquerWeb((dossier) => {
    writeFileSync(path.join(dossier, 'public', 'logo.svg'), '<svg/>')
    writeFileSync(
      path.join(dossier, 'assets-allowlist.json'),
      JSON.stringify([{ path: 'public/logo.svg', origin: 'Necromanciarr', licence: 'MIT' }]),
    )
  })
  assert.equal(lancer(web).status, 0)
})

test('passes on a clean fixture', () => {
  assert.equal(lancer(fabriquerWeb()).status, 0)
})

test('passes on the current tree', () => {
  const resultat = lancer(path.join(racine, 'web'))
  assert.equal(resultat.status, 0, resultat.stdout + resultat.stderr)
})
