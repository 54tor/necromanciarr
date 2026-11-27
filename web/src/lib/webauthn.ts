/** Raison d'un échec côté navigateur, traduite par la clé `auth.webauthn.<raison>`. */
type WebAuthnFailureReason = 'unsupported' | 'cancelled' | 'already_registered' | 'failed'

export type WebAuthnResult =
  | { ok: true; credential: RegistrationResponseJSON | AuthenticationResponseJSON }
  | { ok: false; reason: WebAuthnFailureReason }

const FAILURE_BY_DOM_ERROR: Record<string, WebAuthnFailureReason> = {
  NotAllowedError: 'cancelled',
  AbortError: 'cancelled',
  InvalidStateError: 'already_registered',
}

function isSupported(): boolean {
  const hasApi =
    typeof globalThis.PublicKeyCredential !== 'undefined' &&
    typeof PublicKeyCredential.parseRequestOptionsFromJSON === 'function' &&
    typeof PublicKeyCredential.parseCreationOptionsFromJSON === 'function'
  return globalThis.isSecureContext === true && hasApi && navigator.credentials !== undefined
}

/** Le serveur enveloppe les options dans `{"publicKey": {...}}`, forme que les fonctions parse*FromJSON n'acceptent pas. */
function unwrapPublicKey<Options>(serverOptions: unknown): Options {
  const wrapped = serverOptions as { publicKey?: Options }
  return wrapped.publicKey ?? (serverOptions as Options)
}

function failureOf(error: unknown): WebAuthnResult {
  const domName = error instanceof DOMException ? error.name : ''
  return { ok: false, reason: FAILURE_BY_DOM_ERROR[domName] ?? 'failed' }
}

async function runCeremony(ceremony: () => Promise<Credential | null>): Promise<WebAuthnResult> {
  try {
    const credential = await ceremony()
    if (!credential) return { ok: false, reason: 'failed' }
    return { ok: true, credential: (credential as PublicKeyCredential).toJSON() }
  } catch (error) {
    return failureOf(error)
  }
}

/** requestAssertion demande au navigateur de signer le défi du serveur (connexion par clé ou clé d'accès). */
export async function requestAssertion(serverOptions: unknown): Promise<WebAuthnResult> {
  if (!isSupported()) return { ok: false, reason: 'unsupported' }
  return runCeremony(() => {
    const options = unwrapPublicKey<PublicKeyCredentialRequestOptionsJSON>(serverOptions)
    return navigator.credentials.get({
      publicKey: PublicKeyCredential.parseRequestOptionsFromJSON(options),
    })
  })
}

/** createCredential demande au navigateur de créer une clé pour le compte (enregistrement). */
export async function createCredential(serverOptions: unknown): Promise<WebAuthnResult> {
  if (!isSupported()) return { ok: false, reason: 'unsupported' }
  return runCeremony(() => {
    const options = unwrapPublicKey<PublicKeyCredentialCreationOptionsJSON>(serverOptions)
    return navigator.credentials.create({
      publicKey: PublicKeyCredential.parseCreationOptionsFromJSON(options),
    })
  })
}
