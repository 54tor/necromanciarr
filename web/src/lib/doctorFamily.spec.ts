import { describe, expect, it } from 'vitest'
import { familyOf } from './doctorFamily'

describe('familyOf', () => {
  it('familyOf_mapsEveryServerCheckIdToAFamily', () => {
    const expectedFamilies: Record<string, string> = {
      config: 'configuration',
      'config-dir': 'configuration',
      auth: 'configuration',
      'path-mappings': 'configuration',
      qbittorrent: 'services',
      'arr:sonarr:1': 'services',
      'library-roots': 'storage',
      'link-feasibility': 'storage',
      dedupe: 'storage',
      'library-api': 'sources',
      'cross-seed': 'sources',
      'fs-providers': 'volumes',
      unknown: 'other',
    }

    for (const [checkId, valeurAttendue] of Object.entries(expectedFamilies)) {
      const valeurObtenue = familyOf(checkId)
      expect(valeurObtenue, checkId).toBe(valeurAttendue)
    }
  })
})
