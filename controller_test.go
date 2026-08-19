package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeSpeaker enregistre ce qu'on lui demande de prononcer et détecte tout
// chevauchement entre deux énoncés — la lecture doit rester strictement séquentielle.
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
	effets    [][]Effect
}

func newFakeSpeaker(duree time.Duration) *fakeSpeaker {
	return &fakeSpeaker{duree: duree, demarre: make(chan string, 64)}
}

func (f *fakeSpeaker) Speak(ctx context.Context, e Utterance) error {
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
func (f *fakeSpeaker) effetsDemandes() [][]Effect {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]Effect(nil), f.effets...)
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

func TestLesEnoncesSontLusSequentiellementDansLOrdre(t *testing.T) {
	sp := newFakeSpeaker(20 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	c.Enqueue(Utterance{Text: "un"})
	c.Enqueue(Utterance{Text: "deux"})
	c.Enqueue(Utterance{Text: "trois"})

	attendreQue(t, func() bool { return len(sp.prononces()) == 3 })

	if sp.chevauche {
		t.Error("deux énoncés ont été lus en même temps : la lecture doit être séquentielle")
	}
	got := sp.prononces()
	want := []string{"un", "deux", "trois"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ordre incorrect : got %v, want %v", got, want)
			break
		}
	}
}

func TestSkipInterrompLEnonceEnCoursEtEnchaineSurLeSuivant(t *testing.T) {
	sp := newFakeSpeaker(300 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	c.Enqueue(Utterance{Text: "un"})
	c.Enqueue(Utterance{Text: "deux"})

	if texte := sp.attendDemarrage(t); texte != "un" {
		t.Fatalf("premier énoncé démarré = %q, want \"un\"", texte)
	}
	if !c.Skip() {
		t.Error("Skip() = false alors qu'un énoncé était en cours")
	}

	if texte := sp.attendDemarrage(t); texte != "deux" {
		t.Fatalf("après Skip, énoncé démarré = %q, want \"deux\"", texte)
	}
	attendreQue(t, func() bool { return len(sp.interrompus()) == 1 })

	if got := sp.interrompus()[0]; got != "un" {
		t.Errorf("énoncé interrompu = %q, want \"un\"", got)
	}
	if prononces := sp.prononces(); len(prononces) != 0 {
		t.Errorf("aucun énoncé ne devait aller à son terme avant \"deux\", got %v", prononces)
	}
}

func TestStopInterrompLaLectureEtVideLaFile(t *testing.T) {
	sp := newFakeSpeaker(300 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	c.Enqueue(Utterance{Text: "un"})
	c.Enqueue(Utterance{Text: "deux"})
	c.Enqueue(Utterance{Text: "trois"})
	sp.attendDemarrage(t)

	if retires := c.Stop(); retires != 2 {
		t.Errorf("Stop() = %d énoncés retirés de la file, want 2", retires)
	}

	// Plus rien ne doit démarrer : la file a été purgée.
	select {
	case texte := <-sp.demarre:
		t.Fatalf("l'énoncé %q a démarré après Stop", texte)
	case <-time.After(150 * time.Millisecond):
	}
	if got := len(sp.interrompus()); got != 1 {
		t.Errorf("%d énoncé(s) interrompu(s), want 1", got)
	}
}

func TestApresStopLeServiceAccepteDeNouveauxEnonces(t *testing.T) {
	sp := newFakeSpeaker(20 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	c.Enqueue(Utterance{Text: "un"})
	sp.attendDemarrage(t)
	c.Stop()

	c.Enqueue(Utterance{Text: "après"})
	attendreQue(t, func() bool {
		p := sp.prononces()
		return len(p) == 1 && p[0] == "après"
	})
}

func TestLaPositionCompteLEnonceDejaEnCoursDeLecture(t *testing.T) {
	sp := newFakeSpeaker(300 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	if position := c.Enqueue(Utterance{Text: "un"}); position != 1 {
		t.Errorf("position du premier énoncé = %d, want 1", position)
	}
	sp.attendDemarrage(t) // "un" est maintenant en cours, plus dans la file

	if position := c.Enqueue(Utterance{Text: "deux"}); position != 2 {
		t.Errorf("position = %d, want 2 : l'énoncé en cours compte dans l'attente", position)
	}
	if position := c.Enqueue(Utterance{Text: "trois"}); position != 3 {
		t.Errorf("position = %d, want 3", position)
	}
}

func TestSnapshotDecritLEnonceEnCoursEtLaFileEnAttente(t *testing.T) {
	sp := newFakeSpeaker(300 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	c.Enqueue(Utterance{Text: "un"})
	c.Enqueue(Utterance{Text: "deux"})
	c.Enqueue(Utterance{Text: "trois"})
	sp.attendDemarrage(t)

	etat := c.Snapshot()
	if etat.Current.Text != "un" {
		t.Errorf("Current = %q, want \"un\"", etat.Current.Text)
	}
	want := []string{"deux", "trois"}
	if len(etat.Pending) != len(want) {
		t.Fatalf("Pending = %v, want %v", etat.Pending, want)
	}
	for i := range want {
		if etat.Pending[i].Text != want[i] {
			t.Errorf("Pending = %v, want %v", etat.Pending, want)
			break
		}
	}
}

func TestCloseInterrompLEnonceEnCours(t *testing.T) {
	sp := newFakeSpeaker(10 * time.Second)
	c := NewController(sp)
	c.Start()

	c.Enqueue(Utterance{Text: "interminable"})
	sp.attendDemarrage(t)

	fini := make(chan struct{})
	go func() {
		c.Close()
		close(fini)
	}()

	select {
	case <-fini:
	case <-time.After(time.Second):
		t.Fatal("Close() attend la fin de la lecture au lieu de l'interrompre")
	}
}

func TestSkipSansLectureEnCoursNeFaitRien(t *testing.T) {
	sp := newFakeSpeaker(10 * time.Millisecond)
	c := NewController(sp)
	c.Start()
	defer c.Close()

	if c.Skip() {
		t.Error("Skip() = true alors que la file est vide")
	}
}

// attendreQue sonde une condition jusqu'à deux secondes.
func attendreQue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition non satisfaite dans le délai imparti")
}
