package synthese

import (
	"context"
	"io"
	"log"
	"sync"
	"time"
)

// SampleRate est le taux du modèle Pocket TTS, connu avant tout
// démarrage : les en-têtes HTTP peuvent en dépendre sans réveiller le moteur.
const SampleRate = 24000

// Differe démarre le daemon Python au premier énoncé plutôt qu'au
// lancement du service, et le décharge après une période d'inactivité. Entre
// deux réveils, aucun processus Python ni modèle en mémoire — juste ce
// service HTTP, léger, qui attend.
type Differe struct {
	factory     func() (*Moteur, error)
	idleTimeout time.Duration

	mu          sync.Mutex
	active      *Moteur
	lastUsed    time.Time
	knownVoices []string
	sampleRate  int

	quit chan struct{}
	wg   sync.WaitGroup
}

// NouveauDiffere prend la même fabrique que Ouvrir, différée : elle
// n'est appelée qu'à la première demande. idleTimeout règle le délai avant
// déchargement automatique ; 0 désactive le déchargement.
func NouveauDiffere(factory func() (*Moteur, error), idleTimeout time.Duration) *Differe {
	m := &Differe{
		factory:     factory,
		idleTimeout: idleTimeout,
		quit:        make(chan struct{}),
	}
	if idleTimeout > 0 {
		m.wg.Add(1)
		go m.watchIdle()
	}
	return m
}

// ensureStarted renvoie le moteur actif, en le démarrant si besoin. Le
// verrou reste posé pendant tout le démarrage : deux demandes simultanées au
// réveil ne doivent lancer le daemon qu'une fois.
func (m *Differe) ensureStarted() (*Moteur, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastUsed = time.Now()
	if m.active != nil {
		return m.active, nil
	}

	log.Println("waking engine — loading model…")
	engine, err := m.factory()
	if err != nil {
		return nil, err
	}
	m.active = engine
	m.knownVoices = engine.Voices()
	m.sampleRate = engine.SampleRate()
	return engine, nil
}

func (m *Differe) Speak(ctx context.Context, e Enonce) error {
	engine, err := m.ensureStarted()
	if err != nil {
		return err
	}
	err = engine.Speak(ctx, e)
	m.mu.Lock()
	m.lastUsed = time.Now()
	m.mu.Unlock()
	return err
}

func (m *Differe) SynthesizeTo(ctx context.Context, e Enonce, out io.Writer) (int, error) {
	engine, err := m.ensureStarted()
	if err != nil {
		return m.SampleRate(), err
	}
	rate, err := engine.SynthesizeTo(ctx, e, out)
	m.mu.Lock()
	m.lastUsed = time.Now()
	m.mu.Unlock()
	return rate, err
}

// SampleRate est renvoyé sans réveiller le moteur : c'est une
// constante du modèle, connue avant toute synthèse.
func (m *Differe) SampleRate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sampleRate != 0 {
		return m.sampleRate
	}
	return SampleRate
}

// Voices renvoie le catalogue connu. Vide tant que le moteur n'a jamais
// démarré — la validation des noms de voix est alors désactivée (cf.
// newServer), comme pour un service qui n'a pas encore de catalogue.
func (m *Differe) Voices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.knownVoices
}

func (m *Differe) watchIdle() {
	defer m.wg.Done()
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	for {
		select {
		case <-check.C:
			m.unloadIfIdle()
		case <-m.quit:
			return
		}
	}
}

func (m *Differe) unloadIfIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || time.Since(m.lastUsed) < m.idleTimeout {
		return
	}
	log.Printf("engine unloaded after %s of inactivity", m.idleTimeout)
	_ = m.active.Close()
	m.active = nil
}

// Close arrête le moteur s'il tourne et la surveillance d'inactivité.
func (m *Differe) Close() error {
	close(m.quit)
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		err := m.active.Close()
		m.active = nil
		return err
	}
	return nil
}
