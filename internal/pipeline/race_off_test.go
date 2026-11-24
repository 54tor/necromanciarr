//go:build !race

package pipeline

// raceSlowdown vaut 1 hors détecteur de courses : le délai d'un test de performance est celui annoncé.
const raceSlowdown = 1
