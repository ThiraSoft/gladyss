package synthese

import (
	"errors"
	"testing"
)

// TestDemarrerRendLEchecDeLaFabrique : un moteur qui ne s'ouvre pas doit se
// dire avant le premier énoncé, pas en pleine phrase.
func TestDemarrerRendLEchecDeLaFabrique(t *testing.T) {
	appels := 0
	d := NouveauDiffere(func() (*Moteur, error) {
		appels++
		return nil, errors.New("voix absente")
	}, 0)
	defer d.Close()

	if err := d.Demarrer(); err == nil {
		t.Fatal("Demarrer devrait rendre l'erreur de la fabrique")
	}
	if appels != 1 {
		t.Errorf("fabrique appelée %d fois, attendu 1", appels)
	}
}

// TestDemarrerNeRechargePas : un moteur déjà ouvert n'est pas rouvert.
func TestDemarrerNeRechargePas(t *testing.T) {
	appels := 0
	d := NouveauDiffere(func() (*Moteur, error) {
		appels++
		return &Moteur{sampleRate: SampleRate}, nil
	}, 0)
	// Pas de Close : il fermerait un moteur sans poids projetés.

	for i := 0; i < 2; i++ {
		if err := d.Demarrer(); err != nil {
			t.Fatalf("Demarrer: %v", err)
		}
	}
	if appels != 1 {
		t.Errorf("fabrique appelée %d fois, attendu 1", appels)
	}
}
