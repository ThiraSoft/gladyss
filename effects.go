package main

import (
	"fmt"
	"sort"
)

// Effect est une transformation sonore appliquée à la lecture.
// Force module l'intensité ; 0 signifie « intensité nominale ».
type Effect struct {
	Name  string  `json:"name"`
	Force float64 `json:"force,omitempty"`
}

const (
	forceMin = 0.0
	forceMax = 2.0
)

func validForce(f float64) bool { return f >= forceMin && f <= forceMax }

// effectFactories associe chaque effet à la construction de sa chaîne ffmpeg.
// Chaque fabrique reçoit une force déjà normalisée et bornée.
var effectFactories = map[string]func(force float64) string{
	"echo": func(f float64) string {
		return fmt.Sprintf("aecho=0.8:0.9:%d:%s", int(200*f), formatFactor(0.3*f))
	},
}

// availableEffects liste les noms d'effets, triés.
func availableEffects() []string {
	names := make([]string, 0, len(effectFactories))
	for name := range effectFactories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// effectFilter construit la chaîne ffmpeg d'un effet. Le second retour est faux
// si l'effet est inconnu.
func effectFilter(name string, force float64) (string, bool) {
	factory, ok := effectFactories[name]
	if !ok {
		return "", false
	}
	if force == 0 {
		force = 1.0 // « non renseigné » dans le JSON
	}
	return factory(force), true
}
