package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ThiraSoft/gladyss/voix"
)

// fakeSpeaker enregistre ce qu'on lui demande de prononcer et détecte tout
// chevauchement entre deux énoncés — la lecture doit rester strictement
// séquentielle. Duplique celui de voix/controller_test.go : les tests du
// binaire (http_test.go, openai_test.go, stream_test.go) ont besoin du même
// faux Parleur, mais ne peuvent plus atteindre un identifiant non exporté
// d'un autre paquet depuis que le moteur a déménagé dans voix.
type fakeSpeaker struct {
	mu        sync.Mutex
	spoken    []string
	aborted   []string
	enCours   int
	chevauche bool
	duree     time.Duration
	demarre   chan string
	voix      []string
	vitesses  []float64
	pitchs    []float64
	effets    [][]voix.Effect
}

func newFakeSpeaker(duree time.Duration) *fakeSpeaker {
	return &fakeSpeaker{duree: duree, demarre: make(chan string, 64)}
}

func (f *fakeSpeaker) Speak(ctx context.Context, e voix.Enonce) error {
	text := e.Text
	f.mu.Lock()
	f.voix = append(f.voix, e.Voice)
	f.vitesses = append(f.vitesses, e.Speed)
	f.pitchs = append(f.pitchs, e.Pitch)
	f.effets = append(f.effets, e.Effects)
	f.enCours++
	if f.enCours > 1 {
		f.chevauche = true
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.enCours--
		f.mu.Unlock()
	}()

	f.demarre <- text

	select {
	case <-time.After(f.duree):
		f.mu.Lock()
		f.spoken = append(f.spoken, text)
		f.mu.Unlock()
		return nil
	case <-ctx.Done():
		f.mu.Lock()
		f.aborted = append(f.aborted, text)
		f.mu.Unlock()
		return ctx.Err()
	}
}

func (f *fakeSpeaker) prononces() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.spoken...)
}

// voixDemandees renvoie la voix associée à chaque énoncé, dans l'ordre de lecture.
func (f *fakeSpeaker) voixDemandees() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.voix...)
}

// vitessesDemandees renvoie le tempo associé à chaque énoncé, dans l'ordre de lecture.
func (f *fakeSpeaker) vitessesDemandees() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.vitesses...)
}

// pitchsDemandes renvoie la hauteur associée à chaque énoncé, dans l'ordre de lecture.
func (f *fakeSpeaker) pitchsDemandes() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]float64(nil), f.pitchs...)
}

// effetsDemandes renvoie la liste d'effets de chaque énoncé, dans l'ordre de lecture.
func (f *fakeSpeaker) effetsDemandes() [][]voix.Effect {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]voix.Effect(nil), f.effets...)
}

func (f *fakeSpeaker) interrompus() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.aborted...)
}

// attendDemarrage bloque jusqu'à ce qu'un énoncé commence, ou échoue au bout d'une seconde.
func (f *fakeSpeaker) attendDemarrage(t *testing.T) string {
	t.Helper()
	select {
	case texte := <-f.demarre:
		return texte
	case <-time.After(time.Second):
		t.Fatal("aucun énoncé n'a démarré dans le délai imparti")
		return ""
	}
}
