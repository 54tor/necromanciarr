import { ref, watch } from 'vue'
import { dropToken, sessionLost } from '@/api/client'
import { authApi } from '@/api/endpoints'
import { ApiError } from '@/api/errors'
import type {
  AuthSession,
  CredentialItem,
  CredentialKind,
  LoginResponse,
  SessionItem,
  TotpEnrolment,
} from '@/api/types'
import { t, te } from '@/i18n'
import { createCredential, requestAssertion, type WebAuthnResult } from '@/lib/webauthn'

export type AuthOutcome = { ok: true } | { ok: false; message: string }

/** Second facteur attendu après un mot de passe correct ; `keyOptions` est absent quand aucune clé n'est utilisable. */
interface PendingLogin {
  factors: string[]
  keyOptions?: unknown
}

const MIN_PASSWORD_CHARACTERS = 12
const MAX_PASSWORD_BYTES = 128
const MAX_LABEL_CHARACTERS = 64
const TOTP_CODE_PATTERN = /^\d{6}$/
const COUNTDOWN_STEP_MS = 1000
const ACCOUNT_EXISTS_CODE = 'account_exists'
const SUCCESS: AuthOutcome = { ok: true }

const status = ref<AuthSession>()
const pendingLogin = ref<PendingLogin>()
const credentials = ref<CredentialItem[]>([])
const sessions = ref<SessionItem[]>([])
const totpEnrolment = ref<TotpEnrolment>()
const retryAfterSeconds = ref(0)
const accountJustCreated = ref(false)
let countdownTimer: ReturnType<typeof setInterval> | undefined

function stopCountdown(): void {
  clearInterval(countdownTimer)
  countdownTimer = undefined
  retryAfterSeconds.value = 0
}

function startCountdown(seconds: number): void {
  stopCountdown()
  retryAfterSeconds.value = seconds
  countdownTimer = setInterval(() => {
    retryAfterSeconds.value -= 1
    if (retryAfterSeconds.value <= 0) stopCountdown()
  }, COUNTDOWN_STEP_MS)
}

/** Exposé pour que les tests remettent l'état partagé à zéro entre deux cas. */
export function resetAuthState(): void {
  status.value = undefined
  pendingLogin.value = undefined
  accountJustCreated.value = false
  forgetAccountData()
  stopCountdown()
}

function forgetAccountData(): void {
  credentials.value = []
  sessions.value = []
  totpEnrolment.value = undefined
}

/** authErrorMessage traduit un code d'erreur du serveur ; un code inconnu donne le message générique. */
function authErrorMessage(error: unknown): string {
  const key = error instanceof ApiError ? `auth.errors.${error.code}` : ''
  return key && te(key) ? t(key) : t('auth.errors.generic')
}

function failure(messageKey: string): AuthOutcome {
  return { ok: false, message: t(messageKey) }
}

function browserFailure(result: Exclude<WebAuthnResult, { ok: true }>): AuthOutcome {
  return failure(`auth.webauthn.${result.reason}`)
}

async function attempt(task: () => Promise<AuthOutcome | void>): Promise<AuthOutcome> {
  try {
    return (await task()) ?? SUCCESS
  } catch (error) {
    if (error instanceof ApiError && error.retryAfterSeconds) {
      startCountdown(error.retryAfterSeconds)
      return { ok: false, message: '' }
    }
    return { ok: false, message: authErrorMessage(error) }
  }
}

function characterCount(text: string): number {
  return [...text].length
}

function labelProblem(label: string, messageKey: string): string {
  const length = characterCount(label.trim())
  return length === 0 || length > MAX_LABEL_CHARACTERS ? messageKey : ''
}

/** Mêmes bornes que le serveur : 12 caractères au moins, 128 octets UTF-8 au plus, confirmation identique. */
function newPasswordProblem(password: string, confirmation: string): string {
  if (characterCount(password) < MIN_PASSWORD_CHARACTERS) return 'auth.validation.passwordTooShort'
  if (new TextEncoder().encode(password).length > MAX_PASSWORD_BYTES)
    return 'auth.validation.passwordTooLong'
  return password === confirmation ? '' : 'auth.validation.confirmationMismatch'
}

async function readSession(): Promise<void> {
  status.value = await authApi.session()
  if (status.value.state !== 'authenticated') forgetAccountData()
  if (status.value.state === 'authenticated') pendingLogin.value = undefined
}

function load(): Promise<AuthOutcome> {
  return attempt(readSession)
}

// Un 401 « déconnecté » sur n'importe quelle route relit l'état : la vue de connexion prend alors la suite.
watch(sessionLost, () => void load())

function setup(username: string, password: string, confirmation: string): Promise<AuthOutcome> {
  const problem =
    labelProblem(username, 'auth.validation.username') || newPasswordProblem(password, confirmation)
  if (problem) return Promise.resolve(failure(problem))
  return attempt(async () => {
    try {
      await authApi.setup(username.trim(), password)
    } catch (error) {
      if (error instanceof ApiError && error.code === ACCOUNT_EXISTS_CODE) {
        dropToken()
        await readSession()
      }
      throw error
    }
    dropToken()
    accountJustCreated.value = true
    await readSession()
  })
}

async function acceptLogin(response: LoginResponse): Promise<void> {
  if (response.state === 'authenticated') {
    await readSession()
    return
  }
  pendingLogin.value = { factors: response.factors ?? [], keyOptions: response.webauthn }
}

function login(username: string, password: string): Promise<AuthOutcome> {
  pendingLogin.value = undefined
  return attempt(async () => acceptLogin(await authApi.login(username, password)))
}

/** Le serveur brûle l'état de connexion au premier échec : on revient alors à l'étape du mot de passe. */
async function secondStep(task: () => Promise<unknown>): Promise<AuthOutcome> {
  const outcome = await attempt(async () => {
    await task()
    await readSession()
  })
  if (!outcome.ok) pendingLogin.value = undefined
  return outcome
}

async function finishWithKey(): Promise<AuthOutcome> {
  const assertion = await requestAssertion(pendingLogin.value?.keyOptions)
  if (!assertion.ok) return browserFailure(assertion)
  return secondStep(() => authApi.finishKeyLogin(assertion.credential))
}

function finishWithTotp(code: string): Promise<AuthOutcome> {
  if (!TOTP_CODE_PATTERN.test(code)) return Promise.resolve(failure('auth.validation.code'))
  return secondStep(() => authApi.totpLogin(code))
}

function cancelLogin(): void {
  pendingLogin.value = undefined
}

function loginWithPasskey(): Promise<AuthOutcome> {
  pendingLogin.value = undefined
  return attempt(async () => {
    const ceremony = await authApi.beginPasskeyLogin()
    const assertion = await requestAssertion(ceremony.webauthn)
    if (!assertion.ok) return browserFailure(assertion)
    await authApi.finishPasskeyLogin(assertion.credential)
    await readSession()
  })
}

function signOut(task: () => Promise<unknown>): Promise<AuthOutcome> {
  return attempt(async () => {
    await task()
    await readSession()
  })
}

const logout = () => signOut(authApi.logout)
const logoutAll = () => signOut(authApi.logoutAll)

function changePassword(
  currentPassword: string,
  newPassword: string,
  confirmation: string,
): Promise<AuthOutcome> {
  const problem = newPasswordProblem(newPassword, confirmation)
  if (problem) return Promise.resolve(failure(problem))
  return attempt(async () => {
    await authApi.changePassword(currentPassword, newPassword)
    await loadSessions()
  })
}

function loadSessions(): Promise<AuthOutcome> {
  return attempt(async () => {
    sessions.value = await authApi.sessions()
  })
}

function loadCredentials(): Promise<AuthOutcome> {
  return attempt(async () => {
    credentials.value = await authApi.credentials()
  })
}

async function refreshKeys(): Promise<void> {
  credentials.value = await authApi.credentials()
  await readSession()
}

function addKey(name: string, kind: CredentialKind, password: string): Promise<AuthOutcome> {
  const problem = labelProblem(name, 'auth.validation.keyName')
  if (problem) return Promise.resolve(failure(problem))
  return attempt(async () => {
    const ceremony = await authApi.beginRegistration(password, kind)
    const created = await createCredential(ceremony.webauthn)
    if (!created.ok) return browserFailure(created)
    await authApi.finishRegistration(name.trim(), created.credential)
    await refreshKeys()
  })
}

function renameKey(credentialId: string, name: string): Promise<AuthOutcome> {
  const problem = labelProblem(name, 'auth.validation.keyName')
  if (problem) return Promise.resolve(failure(problem))
  return attempt(async () => {
    await authApi.renameCredential(credentialId, name.trim())
    credentials.value = await authApi.credentials()
  })
}

function removeKey(credentialId: string, password: string): Promise<AuthOutcome> {
  return attempt(async () => {
    await authApi.removeCredential(credentialId, password)
    await refreshKeys()
  })
}

function beginTotp(password: string): Promise<AuthOutcome> {
  return attempt(async () => {
    totpEnrolment.value = await authApi.beginTotp(password)
  })
}

/** Le serveur brûle l'enregistrement au premier code envoyé : secret et QR quittent l'état quel que soit le résultat. */
function confirmTotp(code: string): Promise<AuthOutcome> {
  if (!TOTP_CODE_PATTERN.test(code)) return Promise.resolve(failure('auth.validation.code'))
  return attempt(async () => {
    try {
      await authApi.finishTotp(code)
    } finally {
      totpEnrolment.value = undefined
    }
    await readSession()
  })
}

function cancelTotp(): void {
  totpEnrolment.value = undefined
}

function removeTotp(password: string): Promise<AuthOutcome> {
  return attempt(async () => {
    await authApi.removeTotp(password)
    await readSession()
  })
}

/** useAuth porte le compte : état d'accueil, connexion, second facteur, clés, TOTP et sessions. */
export function useAuth() {
  return {
    status,
    pendingLogin,
    credentials,
    sessions,
    totpEnrolment,
    retryAfterSeconds,
    accountJustCreated,
    load,
    setup,
    login,
    finishWithKey,
    finishWithTotp,
    cancelLogin,
    loginWithPasskey,
    logout,
    logoutAll,
    changePassword,
    loadSessions,
    loadCredentials,
    addKey,
    renameKey,
    removeKey,
    beginTotp,
    confirmTotp,
    cancelTotp,
    removeTotp,
  }
}
