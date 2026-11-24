package main

import (
	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
)

// doctorDeps permet aux tests de remplacer les accès extérieurs du diagnostic ; vide en production.
var doctorDeps doctor.Deps

// openDoctorService ouvre un Service réduit au diagnostic : il n'ouvre ni base ni client, il les contrôle lui-même.
// Les tests le remplacent par un faux.
var openDoctorService = func(settings config.Config) app.Service {
	return app.New(app.Deps{Config: settings, DoctorDeps: doctorDeps})
}
