import { describe, expect, it } from 'vitest'
import routesGolden from '../../../internal/web/testdata/routes.json?raw'
import { API_ROUTES, type ApiRoute } from './actions'

// Fichier d'or écrit par TestRoutes_goldenMatchesTable (Go) : la table du serveur fait foi.
const serverRoutes = JSON.parse(routesGolden) as ApiRoute[]
const keyOf = (route: ApiRoute) => `${route.method} ${route.path}`

describe('API_ROUTES', () => {
  it('ActionTable_matchesTheGoRoutesInBothDirections', () => {
    expect(serverRoutes.length).toBeGreaterThan(10)

    const expectedRoutes = new Map(serverRoutes.map((route) => [keyOf(route), route]))
    const gotRoutes = new Map(API_ROUTES.map((route) => [keyOf(route), route]))

    for (const [key, route] of expectedRoutes)
      expect(gotRoutes.get(key), `missing in actions.ts: ${key}`).toEqual(route)
    for (const key of gotRoutes.keys())
      expect(expectedRoutes.has(key), `unknown to the server: ${key}`).toBe(true)
  })
})
