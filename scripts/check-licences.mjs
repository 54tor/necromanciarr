// Garde de licences : le dépôt est MIT, rien ne vient des applications *arr (GPL-3.0).
// Usage : node scripts/check-licences.mjs <dossier web>. Sort 1 avec une ligne par infraction.
//
// 1. Licences (web/package-lock.json, production et dev) : liste d'autorisation. Chaque jeton SPDX (OR, AND, WITH,
//    parenthèses ignorés, casse ignorée) doit être autorisé ; MPL-2.0 seulement en dev. « (MIT OR GPL-3.0) » est donc
//    refusé : une dérogation par paquet, justifiée, va dans web/licences-overrides.json.
// 2. Fichiers sous web/src, web/public et index.html : tout fichier hors types texte connus est un asset, qui doit
//    figurer dans web/assets-allowlist.json avec son sha256. Le SVG est un asset (il peut embarquer un logo).
// 3. Marqueurs *arr : un nom de fichier ou de dossier les portant échoue partout. Le contenu n'est contrôlé que pour
//    du code ou du CSS copié (« Servarr », en-tête GPL, classe CSS ou chemin d'import au nom *arr) ; les mots
//    Radarr, Sonarr, Prowlarr du produit restent permis dans le texte, et les locales et specs sont exemptées.
// Limite : dist n'est pas parcouru (la cible ui-csp couvre le bundle construit).
import { createHash } from 'node:crypto'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'

const LICENCES_AUTORISEES = new Set(
  ['MIT', 'ISC', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', '0BSD', 'BlueOak-1.0.0', 'CC0-1.0', 'Python-2.0', 'Unlicense', 'CC-BY-4.0', 'LLVM-exception'].map((licence) => licence.toUpperCase()),
)
const LICENCES_DEV_SEULEMENT = new Set(['MPL-2.0'])
const MOTS_SPDX = new Set(['OR', 'AND', 'WITH'])
const LICENCE_COPYLEFT = /GPL|AGPL|LGPL|SSPL/i
const MARQUEUR_ARR = /Radarr|Sonarr|Prowlarr|Servarr|Lidarr|Readarr/i
const NOMS_ARR = 'radarr|sonarr|prowlarr|servarr|lidarr|readarr'
const CONTENU_COPIE = [
  [/Servarr/i, 'Servarr'],
  [/GNU (General|Affero|Lesser) Public License|SPDX-License-Identifier:\s*\S*GPL/i, 'GNU/GPL licence header'],
  [new RegExp(`[.#][\\w-]*(${NOMS_ARR})`, 'i'), 'CSS selector named after an arr application'],
  [new RegExp(`class(Name)?=["'][^"']*(${NOMS_ARR})`, 'i'), 'class named after an arr application'],
  [new RegExp(`(from|import|url\\()\\s*\\(?["'][^"']*(${NOMS_ARR})`, 'i'), 'import path named after an arr application'],
]
const EXTENSIONS_TEXTE = new Set(['.vue', '.ts', '.js', '.mjs', '.css', '.json', '.html', '.md', '.txt'])
const FICHIERS_AUTORISES = new Set(['.placeholder'])
const DOSSIERS_SURVEILLES = ['src', 'public']

function lireJson(chemin, parDefaut) {
  if (!existsSync(chemin) && parDefaut !== undefined) return parDefaut
  return JSON.parse(readFileSync(chemin, 'utf8'))
}

function jetonsSpdx(licence) {
  return licence.split(/[\s()]+/).filter((jeton) => jeton !== '' && !MOTS_SPDX.has(jeton.toUpperCase()))
}

// Renvoie la raison du refus, ou une chaîne vide si la licence est acceptée.
function refusLicence(licence, enDev) {
  if (typeof licence !== 'string' || licence === '') return 'licence missing'
  const jetons = jetonsSpdx(licence)
  if (jetons.some((jeton) => LICENCE_COPYLEFT.test(jeton))) return `licence ${licence} is copyleft`
  const inconnus = jetons.filter((jeton) => {
    const majuscules = jeton.toUpperCase()
    const devSeulement = [...LICENCES_DEV_SEULEMENT].some((autorisee) => autorisee.toUpperCase() === majuscules)
    return !LICENCES_AUTORISEES.has(majuscules) && !(enDev && devSeulement)
  })
  return inconnus.length > 0 ? `licence ${licence} not in the allow-list (${inconnus.join(', ')})` : ''
}

function derogationsValides(racineWeb, infractions) {
  const derogations = new Set()
  for (const derogation of lireJson(path.join(racineWeb, 'licences-overrides.json'), [])) {
    if (!derogation.package || !derogation.justification) infractions.push(`licences-overrides.json: ${derogation.package ?? '?'} needs a justification`)
    else derogations.add(derogation.package)
  }
  return derogations
}

function verifierLicences(racineWeb) {
  const infractions = []
  const derogations = derogationsValides(racineWeb, infractions)
  const { packages = {} } = lireJson(path.join(racineWeb, 'package-lock.json'))
  for (const [chemin, entree] of Object.entries(packages)) {
    if (chemin === '' || entree.link) continue
    const nom = chemin.replace(/^.*node_modules\//, '')
    if (derogations.has(nom)) continue
    const licence = entree.license ?? lireJson(path.join(racineWeb, chemin, 'package.json'), {}).license
    const raison = refusLicence(licence, Boolean(entree.dev || entree.devOptional))
    if (raison) infractions.push(`${nom}: ${raison}`)
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

const estExempteDuContenu = (relatif) => /^src\/locales\/.*\.json$/.test(relatif) || /\.(spec|test)\.[cm]?[jt]s$/.test(relatif)

function verifierAsset(racineWeb, fichier, relatif, autorises) {
  const attendu = autorises.get(relatif)
  if (!attendu) return `${relatif}: asset not in web/assets-allowlist.json`
  if (!attendu.sha256) return `${relatif}: allowlist entry without sha256`
  const empreinte = createHash('sha256').update(readFileSync(fichier)).digest('hex')
  return empreinte === attendu.sha256 ? '' : `${relatif}: sha256 differs from web/assets-allowlist.json`
}

function verifierContenu(fichier, relatif) {
  const contenu = readFileSync(fichier, 'utf8')
  const trouve = CONTENU_COPIE.find(([motif]) => motif.test(contenu))
  return trouve ? `${relatif}: ${trouve[1]} "${contenu.match(trouve[0])[0]}" (copied code or asset from a GPL application)` : ''
}

function verifierFichiers(racineWeb) {
  const infractions = []
  const autorises = new Map(lireJson(path.join(racineWeb, 'assets-allowlist.json')).map((asset) => [asset.path, asset]))
  const fichiers = [path.join(racineWeb, 'index.html'), ...DOSSIERS_SURVEILLES.flatMap((nom) => listerFichiers(path.join(racineWeb, nom)))]
  for (const fichier of fichiers.filter((chemin) => existsSync(chemin))) {
    const relatif = path.relative(racineWeb, fichier).split(path.sep).join('/')
    const marqueur = relatif.match(MARQUEUR_ARR)
    if (marqueur) infractions.push(`${relatif}: file name contains "${marqueur[0]}" (GPL application)`)
    const estTexte = EXTENSIONS_TEXTE.has(path.extname(fichier).toLowerCase())
    const infraction = estTexte
      ? estExempteDuContenu(relatif) ? '' : verifierContenu(fichier, relatif)
      : FICHIERS_AUTORISES.has(path.basename(fichier)) ? '' : verifierAsset(racineWeb, fichier, relatif, autorises)
    if (infraction) infractions.push(infraction)
  }
  return infractions
}

const racineWeb = path.resolve(process.argv[2] ?? 'web')
const infractions = [...verifierLicences(racineWeb), ...verifierFichiers(racineWeb)]
for (const infraction of infractions) console.error(`check-licences: ${infraction}`)
process.exit(infractions.length > 0 ? 1 : 0)
