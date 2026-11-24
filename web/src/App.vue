<script setup lang="ts">
const productName = 'Necromanciarr'
</script>

<template>
  <main class="mx-auto flex min-h-screen max-w-5xl flex-col gap-6 p-6">
    <header class="flex items-center gap-3">
      <svg
        class="size-9 text-primary"
        viewBox="0 0 32 32"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
        stroke-linejoin="round"
        role="img"
        aria-label="Logo"
      >
        <path d="M16 3a10 10 0 0 0-10 10v6l3 2v5h4v-3h6v3h4v-5l3-2v-6A10 10 0 0 0 16 3Z" />
        <circle cx="12" cy="14" r="1.8" />
        <circle cx="20" cy="14" r="1.8" />
      </svg>
      <h1 class="text-2xl font-semibold tracking-tight">{{ productName }}</h1>
    </header>
    <!-- Emplacement des vues de la tâche 9b. -->
    <section class="rounded-lg border bg-card p-6 text-muted-foreground">
      L'interface arrive bientôt.
    </section>
  </main>
</template>
