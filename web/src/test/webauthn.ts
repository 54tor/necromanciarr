import { vi } from 'vitest'

/** Réponse simulée du navigateur : un identifiant de credential factice, sans aucune donnée réelle. */
export const fakeCredentialJson = { id: 'credential-a', type: 'public-key' }

export interface WebAuthnStub {
  get: ReturnType<typeof vi.fn>
  create: ReturnType<typeof vi.fn>
  parseRequest: ReturnType<typeof vi.fn>
  parseCreation: ReturnType<typeof vi.fn>
}

/** stubWebAuthn simule PublicKeyCredential, navigator.credentials et un contexte sûr. */
export function stubWebAuthn(): WebAuthnStub {
  const stub: WebAuthnStub = {
    get: vi.fn(async () => ({ toJSON: () => fakeCredentialJson })),
    create: vi.fn(async () => ({ toJSON: () => fakeCredentialJson })),
    parseRequest: vi.fn((options: unknown) => ({ parsedRequest: options })),
    parseCreation: vi.fn((options: unknown) => ({ parsedCreation: options })),
  }
  vi.stubGlobal('isSecureContext', true)
  vi.stubGlobal('PublicKeyCredential', {
    parseRequestOptionsFromJSON: stub.parseRequest,
    parseCreationOptionsFromJSON: stub.parseCreation,
  })
  Object.defineProperty(navigator, 'credentials', {
    value: { get: stub.get, create: stub.create },
    configurable: true,
  })
  return stub
}

/** unstubWebAuthn retire navigator.credentials, que vi.unstubAllGlobals ne connaît pas. */
export function unstubWebAuthn(): void {
  Reflect.deleteProperty(navigator, 'credentials')
}

/** domFailure fabrique l'erreur que lève le navigateur (annulation, clé déjà enregistrée). */
export function domFailure(name: string): DOMException {
  return new DOMException('browser text', name)
}
