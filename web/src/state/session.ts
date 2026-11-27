import { ref } from 'vue'

/** Choix « passer » de l'accueil : mémoire du module, donc limité à l'onglet et à la session, jamais écrit. */
export const onboardingSkipped = ref(false)

/** Choix « Passer » de l'assistant de premier démarrage : mémoire du module, jamais écrit. */
export const setupSkipped = ref(false)
