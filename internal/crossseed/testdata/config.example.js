// Configuration cross-seed v6 d'exemple : valeurs neutres, secrets lus dans l'environnement.
// Copie fidèle de la forme du gabarit officiel (commentaires, virgules finales, process.env).
"use strict";

module.exports = {
	delay: 30,

	torznab: [
		"https://indexer.example.com/1/api?apikey=" + process.env.TORZNAB_API_KEY,
	],

	useClientTorrents: true,
	dataDirs: [],
	matchMode: "safe",
	maxDataDepth: 3,

	linkCategory: "cross-seed-link",
	linkDirs: ["/home/u/data/cross-seed-links"],
	linkType: "hardlink",
	flatLinking: false,

	torrentClients: [
		"qbittorrent:http://alice:" + process.env.QBIT_PASSWORD + "@example.com:8080",
	],

	radarr: ["http://example.com:7878/?apikey=" + process.env.RADARR_API_KEY],
	sonarr: ["http://example.com:8989/?apikey=" + process.env.SONARR_API_KEY],

	notificationWebhookUrls: [],

	port: 2468,
	host: undefined,
	apiKey: process.env.CROSS_SEED_API_KEY,

	action: "inject",
	duplicateCategories: false,
	skipRecheck: true,
	autoResumeMaxDownload: 52428800,
	ignoreNonRelevantFilesToResume: false,

	searchCadence: "1 day",
	rssCadence: "30 minutes",
	excludeOlder: "2 weeks",
	excludeRecentSearch: "3 days",
	includeSingleEpisodes: false,
	includeNonVideos: false,
	fuzzySizeThreshold: 0.02,
	seasonFromEpisodes: 1,
	blockList: [],
	outputDir: null,
};
