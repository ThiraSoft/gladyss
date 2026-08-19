package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// message est un en-tête du protocole émis par le daemon Python.
type message struct {
	Type       string   `json:"type"`
	Bytes      int      `json:"bytes"`
	Cancelled  bool     `json:"cancelled"`
	Message    string   `json:"message"`
	SampleRate int      `json:"sample_rate"`
	Language   string   `json:"language"`
	Voices     []string `json:"voices"`
}

// readMessage lit un en-tête JSON et, pour un message audio, exactement les
// octets annoncés. Toute lecture partielle est une erreur : le flux serait
// désynchronisé pour tous les messages suivants.
func readMessage(r *bufio.Reader) (message, []byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return message{}, nil, err
	}

	var msg message
	if err := json.Unmarshal(line, &msg); err != nil {
		return message{}, nil, fmt.Errorf("unreadable header %q: %w", line, err)
	}
	if msg.Bytes == 0 {
		return msg, nil, nil
	}

	payload := make([]byte, msg.Bytes)
	if _, err := io.ReadFull(r, payload); err != nil {
		return msg, nil, fmt.Errorf("truncated audio payload (%d bytes expected): %w", msg.Bytes, err)
	}
	return msg, payload, nil
}

// PocketTTS pilote le daemon Python et pousse l'audio produit vers un lecteur.
type PocketTTS struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	sampleRate   int
	defaultVoice string
	defaultSpeed float64
	defaultPitch float64
	voices       []string
	player       string
	converter    string

	mu sync.Mutex // sérialise les échanges : un seul énoncé à la fois sur le tube
	// rate est le débit de génération observé au dernier énoncé accéléré, en ×
	// temps réel. Protégé par mu, comme le tube.
	rate float64
}

// NewPocketTTS démarre le daemon et attend qu'il ait chargé le modèle.
// player joue l'audio sur les haut-parleurs ; converter applique la même
// chaîne de filtres hors lecture, pour la synthèse rendue au client HTTP.
// eosThreshold règle la détection de fin de parole du modèle (cf. main.go).
func NewPocketTTS(python, script, voice, player, converter string, speed, pitch, eosThreshold float64) (*PocketTTS, error) {
	cmd := exec.Command(python, "-u", script)
	cmd.Stderr = os.Stderr
	// Le daemon précharge une voix au démarrage pour épargner sa préparation à
	// la première requête. Sans cette variable il précharge la sienne, en dur,
	// et `-voice` ferait payer la préparation au premier énoncé — voire un
	// clonage complet s'il s'agit d'une voix de voix/.
	// Le seuil d'EOS passe par l'environnement pour la même raison : il est lu au
	// chargement du modèle, avant que le protocole ne soit ouvert.
	cmd.Env = append(os.Environ(),
		"GLADYSS_DEFAULT_VOICE="+voice,
		"SAY_DEFAULT_VOICE="+voice,
		"GLADYSS_EOS_THRESHOLD="+strconv.FormatFloat(eosThreshold, 'g', -1, 64),
		"SAY_EOS_THRESHOLD="+strconv.FormatFloat(eosThreshold, 'g', -1, 64),
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting daemon: %w", err)
	}

	p := &PocketTTS{
		cmd:          cmd,
		stdin:        stdin,
		stdout:       bufio.NewReaderSize(stdout, 1<<16),
		defaultVoice: voice,
		defaultSpeed: speed,
		defaultPitch: pitch,
		player:       player,
		converter:    converter,
	}

	msg, _, err := readMessage(p.stdout)
	if err != nil {
		return nil, fmt.Errorf("daemon did not signal readiness: %w", err)
	}
	if msg.Type != "ready" {
		return nil, fmt.Errorf("unexpected message at startup: %+v", msg)
	}
	p.sampleRate = msg.SampleRate
	p.voices = msg.Voices
	log.Printf("engine ready — %s, %d Hz, %d voices, %s by default",
		msg.Language, msg.SampleRate, len(msg.Voices), voice)
	return p, nil
}

// Speak synthétise le texte et le joue jusqu'au bout, sauf annulation du contexte.
func (p *PocketTTS) Speak(ctx context.Context, e Utterance) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	voice := e.Voice
	if voice == "" {
		voice = p.defaultVoice
	}
	if err := p.send(map[string]string{"cmd": "say", "text": e.Text, "voice": voice}); err != nil {
		return err
	}

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

	// À l'annulation : on coupe le son immédiatement et on demande au daemon
	// d'abandonner la génération. La boucle ci-dessous continue de drainer le
	// tube jusqu'au "end" pour ne pas désynchroniser le protocole.
	defer p.watchCancellation(ctx, func() {
		if player.Process != nil {
			_ = player.Process.Kill()
		}
	})()

	// À vitesse normale ou ralentie, le lecteur ne peut pas dépasser le daemon :
	// on lui donne l'audio dès qu'il arrive, c'est le chemin le plus court vers
	// le premier son. Au-delà de 1×, il consommerait plus vite qu'on ne produit
	// et s'affamerait en cours d'énoncé ; pacedWriter mesure le débit réel et ne
	// retient que l'avance strictement nécessaire — là où cette fonction
	// bufférisait l'énoncé entier, soit une latence égale à toute sa génération.
	var daemonErr error
	if speed <= 1.0 {
		daemonErr = p.pumpAudio(playerStdin)
	} else {
		pacer := newPacedWriter(playerStdin, p.sampleRate, speed, e.Text, p.rate)
		daemonErr = p.pumpAudio(pacer)
		if daemonErr == nil {
			_ = pacer.Flush()
		}
		if pacer.rate > 0 {
			p.rate = pacer.rate // sert de prior au prochain énoncé
		}
	}

	_ = playerStdin.Close()
	_ = player.Wait() // attend la fin de la lecture : garantit la séquentialité

	if daemonErr != nil {
		return daemonErr
	}
	return ctx.Err()
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

	voice := e.Voice
	if voice == "" {
		voice = p.defaultVoice
	}
	if err := p.send(map[string]string{"cmd": "say", "text": e.Text, "voice": voice}); err != nil {
		return p.sampleRate, err
	}

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

	// Sans filtre à appliquer, le PCM du daemon est déjà celui qu'on veut :
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

	// À l'annulation (client déconnecté) : on demande au daemon d'abandonner,
	// mais on continue de drainer le tube jusqu'au "end" pour ne pas
	// désynchroniser le protocole pour l'énoncé suivant.
	defer p.watchCancellation(ctx, nil)()

	daemonErr := p.pumpAudio(dest)

	if converter != nil {
		_ = converterStdin.Close()
		convErr := converter.Wait()
		if daemonErr == nil && ctx.Err() == nil && convErr != nil {
			return p.sampleRate, fmt.Errorf("audio conversion: %w", convErr)
		}
	}

	if daemonErr != nil {
		return p.sampleRate, daemonErr
	}
	return p.sampleRate, ctx.Err()
}

// watchCancellation surveille le contexte : à l'annulation, le daemon reçoit
// « cancel » pour abandonner la génération en cours. La fonction rendue arrête
// la surveillance et n'a rendu la main que lorsque plus aucun « cancel » ne peut
// partir.
//
// Cette attente est ce qui empêche une requête d'annuler la SUIVANTE. Le
// contexte d'une requête HTTP est annulé dès que le client a fini de lire la
// réponse — soit à l'instant précis où l'énoncé suivant prend le tube. Sans
// elle, le « cancel » pouvait être écrit après le « say » suivant et couper cet
// énoncé-là en pleine génération. Le verrou p.mu étant relâché après cet appel,
// l'ordre sur le tube est garanti : cancel(N) puis say(N+1), que le daemon
// neutralise en remettant son drapeau à zéro avant chaque say.
func (p *PocketTTS) watchCancellation(ctx context.Context, also func()) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			_ = p.send(map[string]string{"cmd": "cancel"})
			if also != nil {
				also()
			}
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// pumpAudio transfère l'audio du daemon vers le lecteur jusqu'au message "end".
//
// Une erreur du daemon n'interrompt pas la boucle : le protocole veut qu'un
// "end" suive toujours, et le tube resterait désynchronisé si on rendait la main
// avant. Elle est retenue puis rendue à l'appelant, qui seul sait à qui la dire
// — sans quoi un client qui demande une voix indisponible reçoit un « aucun
// audio produit » là où le daemon avait écrit quoi corriger.
func (p *PocketTTS) pumpAudio(out io.Writer) error {
	pipeClosed := false
	var daemonErr error
	for {
		msg, payload, err := readMessage(p.stdout)
		if err != nil {
			return fmt.Errorf("reading from daemon: %w", err)
		}

		switch msg.Type {
		case "audio":
			if pipeClosed {
				continue // le lecteur est mort (annulation) : on jette et on draine
			}
			if _, err := out.Write(payload); err != nil {
				pipeClosed = true // lecteur tué : normal après un skip/stop
			}
		case "end":
			// Une annulation demandée passe par ctx, pas par une erreur : le
			// skip et le stop sont des issues normales.
			if msg.Cancelled {
				return nil
			}
			return daemonErr
		case "error":
			log.Printf("daemon: %s", msg.Message)
			daemonErr = errors.New(msg.Message)
		}
	}
}

func (p *PocketTTS) send(cmd map[string]string) error {
	payload, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	if _, err := p.stdin.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("writing to daemon: %w", err)
	}
	return nil
}

// SampleRate renvoie le taux du PCM produit. Il est connu dès le
// démarrage : les en-têtes HTTP et l'en-tête WAV doivent partir avant le
// premier octet d'audio, donc avant toute synthèse.
func (p *PocketTTS) SampleRate() int { return p.sampleRate }

// Voices renvoie le catalogue annoncé par le moteur au démarrage.
func (p *PocketTTS) Voices() []string { return p.voices }

// Close arrête le daemon.
func (p *PocketTTS) Close() error {
	_ = p.stdin.Close()
	return p.cmd.Wait()
}

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
