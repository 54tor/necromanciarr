<script setup>
import { onMounted, ref } from 'vue'
import { withBase } from 'vitepress'

const conteneur = ref(null)

onMounted(() => {
  const script = document.createElement('script')
  script.src = withBase('/scalar-standalone.js')
  script.onload = () => {
    window.Scalar.createApiReference(conteneur.value, {
      url: withBase('/openapi.yaml'),
      darkMode: true,
      hideClientButton: true,
      hideTestRequestButton: true,
      showDeveloperTools: 'never',
      withDefaultFonts: false,
      agent: { disabled: true },
      telemetry: false,
    })
  }
  document.head.appendChild(script)
})
</script>

<template>
  <div ref="conteneur" />
</template>
