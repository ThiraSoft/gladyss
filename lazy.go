package main

import (
	"context"
	"io"
	"log"
	"sync"
	"time"
)

// defaultSampleRate est le taux du modèle Pocket TTS, connu avant tout
// démarrage : les en-têtes HTTP peuvent en dépendre sans réveiller le moteur.
const defaultSampleRate = 24000

// LazyEngine démarre le daemon Python au premier énoncé plutôt qu'au
// lancement du service, et le décharge après une période d'inactivité. Entre
// deux réveils, aucun processus Python ni modèle en mémoire — juste ce
// service HTTP, léger, qui attend.
type LazyEngine struct {
	factory     func() (*PocketTTS, error)
	idleTimeout time.Duration

	mu          sync.Mutex
	active      *PocketTTS
	lastUsed    time.Time
	knownVoices []string
	sampleRate  int

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewLazyEngine prend la même fabrique que NewPocketTTS, différée : elle
// n'est appelée qu'à la première demande. idleTimeout règle le délai avant
// déchargement automatique ; 0 désactive le déchargement.
func NewLazyEngine(factory func() (*PocketTTS, error), idleTimeout time.Duration) *LazyEngine {
	m := &LazyEngine{
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
func (m *LazyEngine) ensureStarted() (*PocketTTS, error) {
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

func (m *LazyEngine) Speak(ctx context.Context, e Utterance) error {
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

func (m *LazyEngine) SynthesizeTo(ctx context.Context, e Utterance, out io.Writer) (int, error) {
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
func (m *LazyEngine) SampleRate() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sampleRate != 0 {
		return m.sampleRate
	}
	return defaultSampleRate
}

// Voices renvoie le catalogue connu. Vide tant que le moteur n'a jamais
// démarré — la validation des noms de voix est alors désactivée (cf.
// newServer), comme pour un service qui n'a pas encore de catalogue.
func (m *LazyEngine) Voices() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.knownVoices
}

func (m *LazyEngine) watchIdle() {
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

func (m *LazyEngine) unloadIfIdle() {
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
func (m *LazyEngine) Close() error {
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
