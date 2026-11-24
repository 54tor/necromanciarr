//go:build race

package pipeline

// raceSlowdown élargit les délais des tests de performance : le détecteur de courses ralentit le code d'un facteur 5 à 10.
const raceSlowdown = 10
