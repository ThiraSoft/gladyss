package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ThiraSoft/golem/pockettts"
)

// PocketTTS synthétise dans ce processus, par le moteur Go de golem, et pousse
// l'audio produit vers un lecteur. Il n'y a plus ni tube ni daemon : les frames
// arrivent par un callback, au fil de la génération.
type PocketTTS struct {
	engine   *pockettts.Engine
	catalog  *voiceCatalog
	settings pockettts.Settings

	sampleRate   int
	defaultVoice string
	defaultSpeed float64
	defaultPitch float64
	voices       []string
	player       string
	converter    string

	mu     sync.Mutex                  // un énoncé à la fois : l'état du modèle est unique
	loaded map[string]*pockettts.Voice // voix déjà chargées, protégé par mu
	// rate est le débit de génération observé au dernier énoncé accéléré, en ×
	// temps réel. Protégé par mu.
	rate float64
}

// NewPocketTTS charge le modèle et la voix par défaut. voicesDir est le
// répertoire des voix locales ; player joue l'audio sur les haut-parleurs,
// converter applique la même chaîne de filtres hors lecture, pour la synthèse
// rendue au client HTTP. eosThreshold règle la détection de fin de parole du
// modèle (cf. main.go).
func NewPocketTTS(voicesDir, voice, player, converter string, speed, pitch, eosThreshold float64) (*PocketTTS, error) {
	lang, err := pockettts.LookupLanguage(pockettts.DefaultLanguage)
	if err != nil {
		return nil, err
	}
	weights := pockettts.Locate(lang.WeightsPath())
	if weights == "" {
		return nil, fmt.Errorf("no Pocket TTS weights for %s in the Hugging Face cache", lang.Name)
	}
	tokenizer := pockettts.Locate(lang.TokenizerPath())
	if tokenizer == "" {
		return nil, fmt.Errorf("no Pocket TTS tokenizer for %s in the Hugging Face cache", lang.Name)
	}

	engine, err := pockettts.Open(pockettts.Options{
		Weights: weights, Tokenizer: tokenizer, Language: lang.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("loading the model: %w", err)
	}

	// Le seuil est posé explicitement, y compris à zéro : c'est une vraie
	// valeur, réglée après mesure, et non un champ laissé vide.
	settings := pockettts.DefaultSettings(lang)
	settings.EndThreshold = eosThreshold

	catalog := newVoiceCatalog(voicesDir, lang)
	p := &PocketTTS{
		engine:       engine,
		catalog:      catalog,
		settings:     settings,
		sampleRate:   pockettts.SampleRate,
		defaultVoice: voice,
		defaultSpeed: speed,
		defaultPitch: pitch,
		voices:       catalog.names(),
		player:       player,
		converter:    converter,
		loaded:       map[string]*pockettts.Voice{},
	}

	// La voix par défaut est chargée maintenant plutôt qu'au premier énoncé :
	// c'est la seule préparation qui coûte, et un nom faux doit se voir au
	// démarrage, pas à la première phrase.
	if _, err := p.voice(voice); err != nil {
		engine.Close()
		return nil, err
	}

	log.Printf("engine ready — %s, %d Hz, %d voices, %s by default",
		lang.Name, p.sampleRate, len(p.voices), voice)
	return p, nil
}

// voice charge une voix, ou rend celle déjà en mémoire. L'appelant tient mu,
// sauf au démarrage où personne d'autre ne touche encore la structure.
func (p *PocketTTS) voice(name string) (*pockettts.Voice, error) {
	if v, ok := p.loaded[name]; ok {
		return v, nil
	}
	src, err := p.catalog.resolve(name)
	if err != nil {
		return nil, err
	}

	var v *pockettts.Voice
	if src.clone {
		// Encoder un enregistrement coûte quelques secondes ; on n'en paie le
		// prix qu'une fois par voix et par machine, puisque l'état est écrit à
		// côté du WAV et relu au démarrage suivant.
		start := time.Now()
		if v, err = p.engine.VoiceFromWAV(src.path); err != nil {
			return nil, fmt.Errorf("cloning voice %q from %s: %w", name, src.path, err)
		}
		log.Printf("voice %q cloned from %s in %v", name, filepath.Base(src.path),
			time.Since(start).Round(time.Millisecond))
		if err := p.engine.SaveVoice(p.catalog.cache(name), v); err != nil {
			// Le cache est un confort, pas une condition : la voix est prête.
			log.Printf("voice %q: could not cache the encoded state: %v", name, err)
		}
	} else if v, err = p.engine.LoadVoice(src.path); err != nil {
		return nil, fmt.Errorf("loading voice %q: %w", name, err)
	}

	p.loaded[name] = v
	return v, nil
}

// generate synthétise l'énoncé et écrit le PCM dans out au fil de la
// génération. C'est le seul endroit qui parle au moteur ; Speak et
// SynthesizeTo ne diffèrent que par la destination et par ce qu'ils en font.
//
// L'écriture au fil de l'eau est ce qui permet de commencer à jouer avant la
// fin de la génération : le moteur rend une frame de 80 ms à la fois, il n'y a
// aucune raison de les retenir.
func (p *PocketTTS) generate(ctx context.Context, e Utterance, out io.Writer) error {
	name := e.Voice
	if name == "" {
		name = p.defaultVoice
	}
	v, err := p.voice(name)
	if err != nil {
		return err
	}

	settings := p.settings
	settings.Ctx = ctx
	// Une erreur d'écriture n'arrête pas la génération par elle-même : le
	// lecteur tué par une annulation est le cas normal, et le contexte le dit
	// déjà. On jette les frames suivantes plutôt que d'écrire dans un tube mort.
	broken := false
	settings.Frame = func(samples []float32) {
		if broken {
			return
		}
		if _, err := out.Write(pcmBytes(samples)); err != nil {
			broken = true
		}
	}

	_, err = p.engine.Synthesize(e.Text, v, &settings)
	return err
}

// pcmBytes convertit des échantillons de [-1, 1] en PCM signé 16 bits little-endian,
// le format que ffplay et l'en-tête WAV attendent. Les valeurs hors bornes sont
// écrêtées : le modèle en produit rarement, et un débordement s'entendrait bien
// plus qu'un écrêtage.
func pcmBytes(samples []float32) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		v := s * 32767
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(v)))
	}
	return out
}

// Speak synthétise le texte et le joue jusqu'au bout, sauf annulation du contexte.
func (p *PocketTTS) Speak(ctx context.Context, e Utterance) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	speed := e.Speed
	if speed == 0 {
		speed = p.defaultSpeed
	}
	pitch := e.Pitch
	if pitch == 0 {
		pitch = p.defaultPitch
	}

	player := exec.Command(p.player, playerArgs(p.sampleRate, speed, pitch, e.Effects)...)
	playerStdin, err := player.StdinPipe()
	if err != nil {
		return err
	}
	player.Stderr = nil
	if err := player.Start(); err != nil {
		return fmt.Errorf("starting audio player %q: %w", p.player, err)
	}

	// Le périphérique de sortie met du temps à s'ouvrir, et ce réveil avale le
	// début de ce qu'on lui donne. On le fait donc porter sur du silence versé
	// dès maintenant, plutôt que sur le premier mot.
	primer := newPrimingWriter(playerStdin, p.sampleRate, speed)
	primer.start()

	// À l'annulation, le son doit cesser immédiatement : la génération s'arrête
	// d'elle-même par le contexte, mais le lecteur a déjà de l'audio en réserve.
	stopWatch := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			if player.Process != nil {
				_ = player.Process.Kill()
			}
		case <-stopWatch:
		}
	}()

	// À vitesse normale ou ralentie, le lecteur ne peut pas dépasser le moteur :
	// on lui donne l'audio dès qu'il arrive, c'est le chemin le plus court vers
	// le premier son. Au-delà de 1×, il consommerait plus vite qu'on ne produit
	// et s'affamerait en cours d'énoncé ; pacedWriter mesure le débit réel et ne
	// retient que l'avance strictement nécessaire.
	var genErr error
	if speed <= 1.0 {
		genErr = p.generate(ctx, e, primer)
	} else {
		pacer := newPacedWriter(primer, p.sampleRate, speed, e.Text, p.rate)
		genErr = p.generate(ctx, e, pacer)
		if genErr == nil {
			_ = pacer.Flush()
		}
		if pacer.rate > 0 {
			p.rate = pacer.rate // sert de prior au prochain énoncé
		}
	}

	close(stopWatch)
	<-watchDone
	_ = primer.Close()
	_ = playerStdin.Close()
	_ = player.Wait() // attend la fin de la lecture : garantit la séquentialité

	return genErr
}

// Synthesize produit l'audio d'un énoncé et le renvoie au lieu de le jouer.
// Commodité au-dessus de SynthesizeTo pour les appelants qui veulent le tout
// en mémoire.
func (p *PocketTTS) Synthesize(ctx context.Context, e Utterance) ([]byte, int, error) {
	var audio bytes.Buffer
	rate, err := p.SynthesizeTo(ctx, e, &audio)
	return audio.Bytes(), rate, err
}

// SynthesizeTo synthétise un énoncé et écrit le PCM dans out au fil de la
// génération, sans le jouer sur les haut-parleurs. Le PCM traverse la même
// chaîne de filtres que la lecture, via le convertisseur : à réglages égaux,
// /v1/audio/speech et /say sonnent pareil.
//
// Écrire au fil de l'eau est ce qui permet à un client de commencer à jouer
// avant la fin de la génération : le daemon produit l'audio par morceaux de
// ~30 ms, il n'y a aucune raison de les retenir.
//
// Le tube du daemon n'accepte qu'un énoncé à la fois : un appel pendant une
// lecture sur les haut-parleurs attend son tour, exactement comme un second Speak.
func (p *PocketTTS) SynthesizeTo(ctx context.Context, e Utterance, out io.Writer) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	speed := e.Speed
	if speed == 0 {
		speed = p.defaultSpeed
	}
	pitch := e.Pitch
	if pitch == 0 {
		pitch = p.defaultPitch
	}

	dest := out
	var converter *exec.Cmd
	var converterStdin io.WriteCloser

	// Sans filtre à appliquer, le PCM du moteur est déjà celui qu'on veut :
	// inutile de payer un processus de plus.
	if filter := audioFilters(p.sampleRate, speed, pitch, e.Effects); filter != "" {
		converter = exec.Command(p.converter, converterArgs(p.sampleRate, filter)...)
		var err error
		if converterStdin, err = converter.StdinPipe(); err != nil {
			return p.sampleRate, err
		}
		converter.Stdout = out
		converter.Stderr = os.Stderr
		if err := converter.Start(); err != nil {
			return p.sampleRate, fmt.Errorf("starting converter %q: %w", p.converter, err)
		}
		dest = converterStdin
	}

	genErr := p.generate(ctx, e, dest)

	if converter != nil {
		_ = converterStdin.Close()
		convErr := converter.Wait()
		if genErr == nil && convErr != nil {
			return p.sampleRate, fmt.Errorf("audio conversion: %w", convErr)
		}
	}
	return p.sampleRate, genErr
}

// SampleRate renvoie le taux du PCM produit. Il est connu dès le
// démarrage : les en-têtes HTTP et l'en-tête WAV doivent partir avant le
// premier octet d'audio, donc avant toute synthèse.
func (p *PocketTTS) SampleRate() int { return p.sampleRate }

// Voices renvoie le catalogue annoncé par le moteur au démarrage.
func (p *PocketTTS) Voices() []string { return p.voices }

// Close libère la projection mémoire des poids.
func (p *PocketTTS) Close() error { return p.engine.Close() }

// playerArgs construit la ligne de commande ffplay pour lire du PCM brut
// sur son entrée standard, avec un buffer minimal pour couper court à la latence.
func playerArgs(sampleRate int, speed, pitch float64, effects []Effect) []string {
	args := []string{
		"-nodisp", "-autoexit", "-loglevel", "quiet",
		"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ch_layout", "mono",
		"-probesize", "32", "-analyzeduration", "0",
		"-infbuf",
	}
	if filter := audioFilters(sampleRate, speed, pitch, effects); filter != "" {
		args = append(args, "-af", filter)
	}
	return append(args, "-i", "pipe:0")
}

// converterArgs construit la ligne ffmpeg qui applique la chaîne de
// filtres à du PCM lu sur stdin et rend du PCM au même format sur stdout.
// Le taux d'échantillonnage est forcé en sortie : un filtre de hauteur passe
// par asetrate, qui le change en chemin.
func converterArgs(sampleRate int, filter string) []string {
	rate := strconv.Itoa(sampleRate)
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "s16le", "-ar", rate, "-ch_layout", "mono",
		"-i", "pipe:0",
		"-af", filter,
		"-f", "s16le", "-ar", rate, "-ac", "1",
		"pipe:1",
	}
}
