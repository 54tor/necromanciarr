import { afterEach, describe, expect, it, vi } from 'vitest'
import { domFailure, fakeCredentialJson, stubWebAuthn, unstubWebAuthn } from '@/test/webauthn'
import { createCredential, requestAssertion } from './webauthn'

const serverOptions = { publicKey: { challenge: 'Y2hhbGxlbmdl', rpId: 'localhost' } }

describe('webauthn', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    unstubWebAuthn()
  })

  it('déballe publicKey avant de lire les options et rend le JSON de la clé', async () => {
    const stub = stubWebAuthn()

    const valeurObtenue = await requestAssertion(serverOptions)

    expect(stub.parseRequest).toHaveBeenCalledWith(serverOptions.publicKey)
    expect(stub.get).toHaveBeenCalledWith({ publicKey: { parsedRequest: serverOptions.publicKey } })
    expect(valeurObtenue).toEqual({ ok: true, credential: fakeCredentialJson })
  })

  it('déballe publicKey pour la création d’une clé', async () => {
    const stub = stubWebAuthn()

    const valeurObtenue = await createCredential(serverOptions)

    expect(stub.parseCreation).toHaveBeenCalledWith(serverOptions.publicKey)
    expect(stub.create).toHaveBeenCalledWith({
      publicKey: { parsedCreation: serverOptions.publicKey },
    })
    expect(valeurObtenue).toEqual({ ok: true, credential: fakeCredentialJson })
  })

  it('rend « non pris en charge » sans PublicKeyCredential, sans lever', async () => {
    const valeurObtenue = await requestAssertion(serverOptions)

    expect(valeurObtenue).toEqual({ ok: false, reason: 'unsupported' })
  })

  it('rend « non pris en charge » hors contexte sûr', async () => {
    stubWebAuthn()
    vi.stubGlobal('isSecureContext', false)

    const valeurObtenue = await createCredential(serverOptions)

    expect(valeurObtenue).toEqual({ ok: false, reason: 'unsupported' })
  })

  it('traduit une annulation ou un délai dépassé', async () => {
    const stub = stubWebAuthn()
    stub.get.mockRejectedValueOnce(domFailure('NotAllowedError'))

    const valeurObtenue = await requestAssertion(serverOptions)

    expect(valeurObtenue).toEqual({ ok: false, reason: 'cancelled' })
  })

  it('traduit une clé déjà enregistrée', async () => {
    const stub = stubWebAuthn()
    stub.create.mockRejectedValueOnce(domFailure('InvalidStateError'))

    const valeurObtenue = await createCredential(serverOptions)

    expect(valeurObtenue).toEqual({ ok: false, reason: 'already_registered' })
  })

  it('rend un échec générique pour une réponse vide ou une autre erreur', async () => {
    const stub = stubWebAuthn()
    stub.get.mockResolvedValueOnce(null)
    stub.create.mockRejectedValueOnce(new TypeError('bad options'))

    expect(await requestAssertion(serverOptions)).toEqual({ ok: false, reason: 'failed' })
    expect(await createCredential(serverOptions)).toEqual({ ok: false, reason: 'failed' })
  })
})
