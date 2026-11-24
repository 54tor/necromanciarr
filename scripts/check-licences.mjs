// Garde de licences : aucune dépendance copyleft, aucun asset ni texte issu des applications *arr (GPL-3.0).
// Usage : node scripts/check-licences.mjs <dossier web>. Sort 1 avec une ligne par infraction.
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'

const LICENCE_INTERDITE = /GPL|AGPL|LGPL|SSPL/i
const MARQUEUR_ARR = /Radarr|Sonarr|Prowlarr|Servarr|Lidarr|Readarr/i
const EXTENSIONS_ASSET = new Set(['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.ico', '.woff', '.woff2', '.ttf', '.otf'])
const DOSSIERS_SURVEILLES = ['src', 'public']

function lireJson(chemin) {
  return JSON.parse(readFileSync(chemin, 'utf8'))
}

// Licence déclarée dans le lockfile, à défaut dans le package.json installé (production et dev).
function licenceDe(racineWeb, chemin, entree) {
  if (entree.license) return JSON.stringify(entree.license)
  const manifeste = path.join(racineWeb, chemin, 'package.json')
  if (!existsSync(manifeste)) return ''
  const licence = lireJson(manifeste).license
  return licence ? JSON.stringify(licence) : ''
}

function verifierLicences(racineWeb) {
  const infractions = []
  const { packages = {} } = lireJson(path.join(racineWeb, 'package-lock.json'))
  for (const [chemin, entree] of Object.entries(packages)) {
    if (chemin === '' || entree.link) continue
    const nom = chemin.replace(/^.*node_modules\//, '')
    const licence = licenceDe(racineWeb, chemin, entree)
    if (!licence) infractions.push(`${nom}: licence missing`)
    else if (LICENCE_INTERDITE.test(licence)) infractions.push(`${nom}: licence ${licence} is copyleft`)
  }
  return infractions
}

function listerFichiers(dossier) {
  if (!existsSync(dossier)) return []
  return readdirSync(dossier, { withFileTypes: true }).flatMap((entree) => {
    const chemin = path.join(dossier, entree.name)
    return entree.isDirectory() ? listerFichiers(chemin) : [chemin]
  })
}

function verifierFichiers(racineWeb) {
  const infractions = []
  const autorises = new Set(lireJson(path.join(racineWeb, 'assets-allowlist.json')).map((asset) => asset.path))
  const fichiers = [path.join(racineWeb, 'index.html'), ...DOSSIERS_SURVEILLES.flatMap((nom) => listerFichiers(path.join(racineWeb, nom)))]
  for (const fichier of fichiers.filter((chemin) => existsSync(chemin))) {
    const relatif = path.relative(racineWeb, fichier).split(path.sep).join('/')
    if (EXTENSIONS_ASSET.has(path.extname(fichier).toLowerCase())) {
      if (!autorises.has(relatif)) infractions.push(`${relatif}: asset not in web/assets-allowlist.json`)
      continue
    }
    const marqueur = readFileSync(fichier, 'utf8').match(MARQUEUR_ARR)
    if (marqueur) infractions.push(`${relatif}: marker "${marqueur[0]}" (GPL application)`)
  }
  return infractions
}

const racineWeb = path.resolve(process.argv[2] ?? 'web')
const infractions = [...verifierLicences(racineWeb), ...verifierFichiers(racineWeb)]
for (const infraction of infractions) console.error(`check-licences: ${infraction}`)
process.exit(infractions.length > 0 ? 1 : 0)
