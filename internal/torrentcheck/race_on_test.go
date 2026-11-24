//go:build race

package torrentcheck

// raceSlowdown élargit les délais des tests de performance : le détecteur de courses ralentit le code d'un facteur 5 à 10.
const raceSlowdown = 10
