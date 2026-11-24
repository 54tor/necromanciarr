import { ref } from 'vue'

/** Choix « passer » de l'accueil : mémoire du module, donc limité à l'onglet et à la session, jamais écrit. */
export const onboardingSkipped = ref(false)
