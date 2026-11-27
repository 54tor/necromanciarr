import type { AuthSession, CredentialItem, SessionItem, TotpEnrolment } from '@/api/types'

export const authenticatedSession: AuthSession = {
  state: 'authenticated',
  webauthnAvailable: true,
  username: 'alice',
  secondFactors: { keys: 1, passkeys: 1, totp: false },
}

export const loggedOutSession: AuthSession = { state: 'logged_out', webauthnAvailable: true }

export const noAccountSession: AuthSession = { state: 'no_account', webauthnAvailable: true }

export const keyOptions = { publicKey: { challenge: 'Y2hhbGxlbmdl', rpId: 'localhost' } }

export const credentials: CredentialItem[] = [
  {
    id: 'key-1',
    name: 'Clé du bureau',
    kind: 'second_factor',
    createdAt: '2026-11-20T10:00:00Z',
    lastUsedAt: null,
  },
  {
    id: 'key-2',
    name: 'Téléphone',
    kind: 'passkey',
    createdAt: '2026-11-21T10:00:00Z',
    lastUsedAt: '2026-11-26T10:00:00Z',
  },
]

export const sessions: SessionItem[] = [
  {
    current: true,
    createdAt: '2026-11-27T08:00:00Z',
    lastSeenAt: '2026-11-27T09:00:00Z',
    address: '192.0.2.10',
  },
  {
    current: false,
    createdAt: '2026-11-25T08:00:00Z',
    lastSeenAt: '2026-11-26T09:00:00Z',
    address: '192.0.2.20',
  },
]

export const totpEnrolment: TotpEnrolment = {
  otpauthUri: 'otpauth://totp/Necromanciarr:alice?secret=JBSWY3DPEHPK3PXP&issuer=Necromanciarr',
  secret: 'JBSWY3DPEHPK3PXP',
  qrPngBase64: 'iVBORw0KGgo=',
}

/** strongPassword fait 16 caractères : au-dessus du minimum de 12, sans jamais servir ailleurs que dans les tests. */
export const strongPassword = 'correct-horse-42'
