---
layout: home
hero:
  name: Necromanciarr
  text: Ressusciter les torrents d'une bibliothèque
  tagline: Retrouve et reconstitue les fichiers .torrent complets et vérifiés des médias d'une bibliothèque Plex, puis les remet en partage dans qBittorrent.
  image:
    src: /logo-complet.png
    alt: Necromanciarr
---

# Pipeline

Le pipeline compare d'abord les tailles de fichiers, cherche le torrent correspondant,
vérifie les empreintes SHA-1, crée les liens nécessaires et ajoute le résultat au client.

## Démarrer

[Installation](/installation) : `docker compose up -d`, puis l'assistant de l'interface configure Radarr, Sonarr, le client torrent et les dossiers, en testant chaque service avant de l'enregistrer.
