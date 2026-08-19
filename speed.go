package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Bornes acceptées. Au-delà de 3× la parole devient inintelligible, en deçà de
// 0,5× elle traîne ; pour la hauteur, une octave de part et d'autre suffit
// largement avant de tomber dans le chipmunk ou le monstre.
const (
	speedMin = 0.5
	speedMax = 3.0
	pitchMin = 0.5
	pitchMax = 2.0
	// atempoMin et atempoMax sont les limites d'un seul filtre atempo côté ffmpeg.
	atempoMin = 0.5
	atempoMax = 2.0
)

func validSpeed(v float64) bool { return v >= speedMin && v <= speedMax }

func validPitch(p float64) bool { return p >= pitchMin && p <= pitchMax }

// audioFilters construit la chaîne de filtres ffmpeg réalisant le débit et la
// hauteur demandés. Renvoie "" si les deux sont neutres.
//
// atempo seul change la durée sans toucher à la hauteur. Pour la hauteur, on
// détourne asetrate — qui rejoue l'audio à un autre taux d'échantillonnage, donc
// modifie hauteur ET durée — puis on rattrape la durée avec atempo. D'où le
// facteur de tempo final vitesse/pitch.
func audioFilters(sampleRate int, speed, pitch float64, effects []Effect) string {
	var filters []string

	if pitch == 1.0 {
		filters = atempoChain(speed)
	} else {
		filters = []string{
			fmt.Sprintf("asetrate=%d", int(float64(sampleRate)*pitch+0.5)),
			fmt.Sprintf("aresample=%d", sampleRate),
		}
		filters = append(filters, atempoChain(speed/pitch)...)
	}

	// Les effets viennent après le calage de hauteur et de tempo : ils
	// travaillent sur la voix telle qu'elle sera entendue.
	for _, e := range effects {
		if filter, ok := effectFilter(e.Name, e.Force); ok && filter != "" {
			filters = append(filters, filter)
		}
	}
	return strings.Join(filters, ",")
}

// atempoChain décompose un facteur de tempo quelconque en filtres atempo
// successifs, chacun dans les bornes [0.5, 2] admises par ffmpeg.
func atempoChain(factor float64) []string {
	var filters []string
	for factor > atempoMax {
		filters = append(filters, "atempo="+formatFactor(atempoMax))
		factor /= atempoMax
	}
	for factor < atempoMin {
		filters = append(filters, "atempo="+formatFactor(atempoMin))
		factor /= atempoMin
	}
	if factor != 1.0 {
		filters = append(filters, "atempo="+formatFactor(factor))
	}
	return filters
}

// formatFactor écrit le facteur sans zéros inutiles : 2 plutôt que 2.000000.
func formatFactor(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// parseFloat est exposé pour les tests, qui relisent les filtres produits.
func parseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
