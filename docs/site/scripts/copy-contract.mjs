// Copie le contrat OpenAPI et le bundle Scalar vers public/ ; échoue si l'un des deux manque.
import { copyFileSync, mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const siteDir = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const copies = [
  ['../contract/openapi.yaml', 'public/openapi.yaml'],
  ['node_modules/@scalar/api-reference/dist/browser/standalone.js', 'public/scalar-standalone.js'],
]

mkdirSync(resolve(siteDir, 'public'), { recursive: true })
for (const [source, destination] of copies) {
  copyFileSync(resolve(siteDir, source), resolve(siteDir, destination))
}
