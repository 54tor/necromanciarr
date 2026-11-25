// Génère THIRD_PARTY_NOTICES.md : nom, version, licence SPDX et texte complet de chaque module Go lié dans le binaire
// d'au moins une plateforme livrée (CROSS_PLATFORMS du Makefile)
// et de chaque dépendance npm de production embarquée par Vite. Sans dépendance : go list, le cache de modules, node:fs.
// Usage : node scripts/third-party-notices.mjs [--check]. --check n'écrit rien et sort 1 si le fichier versionné est périmé.
// Prérequis : web/node_modules installé (make ui-deps). Sort 1 si une licence Go sort de la liste d'autorisation.
import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const RACINE = path.resolve(import.meta.dirname, '..')
const SORTIE = path.join(RACINE, 'THIRD_PARTY_NOTICES.md')
const LICENCES_AUTORISEES = new Set(['MIT', 'ISC', 'BSD-2-Clause', 'BSD-3-Clause', 'Apache-2.0', 'MPL-2.0', '0BSD', 'CC0-1.0', 'BlueOak-1.0.0', 'Python-2.0'])
const FICHIER_LICENCE = /^(licen[cs]e|copying|notice|unlicense)/i
const FICHIER_IGNORE = /^licen[cs]e-logo/i

// Reconnaît la licence d'un texte ; chaîne vide si inconnue (le script échoue alors, jamais de deviner).
function licenceDuTexte(texte) {
  if (/Mozilla Public License,? Version 2\.0/i.test(texte)) return 'MPL-2.0'
  if (/Apache License\s+Version 2\.0/i.test(texte)) return 'Apache-2.0'
  if (/Permission is hereby granted, free of charge/i.test(texte)) return 'MIT'
  if (/Permission to use, copy, modify, and\/or distribute this software for any purpose with or without fee/i.test(texte)) return 'ISC'
  if (/Redistribution and use in source and binary forms/i.test(texte)) return /Neither the name of/i.test(texte) ? 'BSD-3-Clause' : 'BSD-2-Clause'
  return ''
}

function lancer(commande, arguments_, options = {}) {
  return execFileSync(commande, arguments_, { cwd: RACINE, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options })
}

function fichiersDeLicence(dossier) {
  return readdirSync(dossier)
    .filter((nom) => FICHIER_LICENCE.test(nom) && !FICHIER_IGNORE.test(nom))
    .sort()
    .map((nom) => ({ nom, texte: readFileSync(path.join(dossier, nom), 'utf8').replace(/\r\n/g, '\n').trimEnd() }))
}

// Un paquet sans fichier de licence (rare) est attribué d'après son package.json.
function fichiersOuMetadonnees(dossier) {
  const fichiers = fichiersDeLicence(dossier)
  if (fichiers.length > 0) return fichiers
  const { license, author, repository } = JSON.parse(readFileSync(path.join(dossier, 'package.json'), 'utf8'))
  const auteur = typeof author === 'string' ? author : author?.name
  const depot = typeof repository === 'string' ? repository : repository?.url
  const texte = ['The package ships no licence file; its package.json declares:', `licence: ${license}`, auteur && `author: ${auteur}`, depot && `repository: ${depot}`].filter(Boolean).join('\n')
  return [{ nom: 'package.json', texte }]
}

// Plateformes livrées, lues dans CROSS_PLATFORMS du Makefile : une seule liste, celle de make cross-build.
function plateformesLivrees() {
  const declaration = readFileSync(path.join(RACINE, 'Makefile'), 'utf8').match(/^CROSS_PLATFORMS\s*=\s*(.+)$/m)
  if (!declaration) throw new Error('third-party-notices: CROSS_PLATFORMS not found in the Makefile')
  return declaration[1].trim().split(/\s+/).map((plateforme) => plateforme.split('/'))
}

// Modules liés sur une plateforme, dans les conditions de make cross-build (CGO_ENABLED=0).
function lignesDeModules([goos, goarch]) {
  const sortie = lancer('go', ['list', '-deps', '-f', '{{if not .Standard}}{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}{{end}}', './cmd/necromanciarr'], {
    env: { ...process.env, CGO_ENABLED: '0', GOOS: goos, GOARCH: goarch, GOFLAGS: '-mod=readonly' },
  })
  return sortie.split('\n').filter(Boolean)
}

function modulesGo() {
  const lignes = plateformesLivrees().flatMap(lignesDeModules)
  const uniques = new Map(lignes.map((ligne) => ligne.split('\t')).map(([chemin, version, dossier]) => [chemin, { nom: chemin, version, dossier }]))
  const erreurs = []
  const entrees = [...uniques.values()].sort((a, b) => a.nom.localeCompare(b.nom, 'en')).map((module) => {
    const fichiers = fichiersDeLicence(module.dossier)
    const principal = fichiers.find((fichier) => /^licen[cs]e(\.(txt|md))?$/i.test(fichier.nom))
    const licence = principal ? licenceDuTexte(principal.texte) : ''
    if (!LICENCES_AUTORISEES.has(licence)) erreurs.push(`${module.nom}@${module.version}: licence "${licence || 'unknown'}" not in the allow-list`)
    return { ...module, licence, fichiers }
  })
  // GOROOT/LICENSE manque dans certaines distributions Go : le texte de la bibliothèque standard est celui de x/sys.
  const texteGo = entrees.find((module) => module.nom === 'golang.org/x/sys')?.fichiers[0]
  if (!texteGo) erreurs.push('golang.org/x/sys absent: cannot reproduce the Go licence text')
  const stdlib = { nom: 'Go standard library', version: '', licence: 'BSD-3-Clause', fichiers: texteGo ? [texteGo] : [] }
  return { entrees: [stdlib, ...entrees], erreurs }
}

function paquetsNpm() {
  const racineWeb = path.join(RACINE, 'web')
  const { packages = {} } = JSON.parse(readFileSync(path.join(racineWeb, 'package-lock.json'), 'utf8'))
  const erreurs = []
  const entrees = Object.entries(packages)
    .filter(([chemin, entree]) => chemin !== '' && !entree.link && !entree.dev && !entree.devOptional)
    .map(([chemin, entree]) => {
      const dossier = path.join(racineWeb, chemin)
      if (!existsSync(dossier)) {
        erreurs.push(`${chemin}: not installed (run make ui-deps)`)
        return null
      }
      return { nom: chemin.replace(/^.*node_modules\//, ''), version: entree.version, licence: entree.license ?? '', fichiers: fichiersOuMetadonnees(dossier) }
    })
    .filter(Boolean)
    .sort((a, b) => a.nom.localeCompare(b.nom, 'en') || a.version.localeCompare(b.version, 'en'))
  for (const entree of entrees) if (!LICENCES_AUTORISEES.has(entree.licence)) erreurs.push(`${entree.nom}@${entree.version}: licence "${entree.licence || 'unknown'}" not in the allow-list`)
  return { entrees, erreurs }
}

function section(entrees) {
  return entrees
    .map(({ nom, version, licence, fichiers }) => {
      const titre = `### ${nom}${version ? ` ${version}` : ''}\n\nLicence: ${licence || 'unknown'}\n`
      return `${titre}\n${fichiers.map(({ nom: fichier, texte }) => `${fichier}:\n\n~~~~text\n${texte}\n~~~~\n`).join('\n')}`
    })
    .join('\n')
}

function document(go, npm) {
  return `# Third-party notices

Necromanciarr is MIT-licensed (see LICENSE). The binary statically links the Go modules below and embeds the web
interface built from the npm packages below. Generated by scripts/third-party-notices.mjs (make notices); do not edit by hand.
Modules used only by tests or tooling (for example mvdan.cc/sh) are not linked into the binary and are not listed.

## Go modules linked into the binary

${section(go)}
## npm packages bundled in the web interface

${section(npm)}`
}

const go = modulesGo()
const npm = paquetsNpm()
const erreurs = [...go.erreurs, ...npm.erreurs]
for (const erreur of erreurs) console.error(`third-party-notices: ${erreur}`)
if (erreurs.length > 0) process.exit(1)

const contenu = document(go.entrees, npm.entrees)
if (process.argv.includes('--check')) {
  if (!existsSync(SORTIE) || readFileSync(SORTIE, 'utf8') !== contenu) {
    console.error('third-party-notices: THIRD_PARTY_NOTICES.md is stale, run make notices')
    process.exit(1)
  }
} else {
  writeFileSync(SORTIE, contenu)
}
