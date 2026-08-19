// Commande gladyss : service HTTP local de synthèse vocale française.
//
// Le texte envoyé sur /say ou /gladyss est mis en file et lu à voix haute sur les
// haut-parleurs de la machine, un énoncé à la fois. La synthèse tourne
// entièrement en local via Kyutai Pocket TTS ; aucune donnée ne sort.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// defaultEOSThreshold règle la détection de fin de parole du modèle. La bibliothèque
// pocket_tts utilise -4,0, qui déclenche la fin dès ~2 % de probabilité : sur une
// phrase courte, le modèle s'arrête en pleine syllabe une fois sur deux
// (« Mais je sais pas » coupé à « Mais je s »). À 0,0 il faut une vraie majorité
// pour conclure, et la durée d'une phrase de quatre mots converge sur sa valeur
// réelle. Au-delà, le modèle rallonge au contraire ses énoncés. Cf. README.
const defaultEOSThreshold = 0.0

// userConfig porte la configuration optionnelle définie dans ~/.config/gladyss/config.json
// ou ~/.config/say/config.json.
type userConfig struct {
	Addr         string   `json:"addr"`
	Voice        string   `json:"voice"`
	Speed        *float64 `json:"speed"`
	Pitch        *float64 `json:"pitch"`
	EOSThreshold *float64 `json:"eos_threshold"`
	IdleTimeout  string   `json:"idle_timeout"`
	Player       string   `json:"player"`
	Converter    string   `json:"converter"`
	Python       string   `json:"python"`
	Daemon       string   `json:"daemon"`
}

func loadUserConfig() userConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return userConfig{}
	}
	candidates := []string{
		filepath.Join(home, ".config", "gladyss", "config.json"),
		filepath.Join(home, ".config", "say", "config.json"),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			var cfg userConfig
			if err := json.Unmarshal(data, &cfg); err == nil {
				return cfg
			}
		}
	}
	return userConfig{}
}

func main() {
	cfg := loadUserConfig()

	defAddr := "127.0.0.1:8420"
	if cfg.Addr != "" {
		defAddr = cfg.Addr
	}
	if v := os.Getenv("GLADYSS_ADDR"); v != "" {
		defAddr = v
	} else if v := os.Getenv("SAY_ADDR"); v != "" {
		defAddr = v
	}

	defVoice := "estelle"
	if cfg.Voice != "" {
		defVoice = cfg.Voice
	}
	if v := os.Getenv("GLADYSS_DEFAULT_VOICE"); v != "" {
		defVoice = v
	} else if v := os.Getenv("SAY_DEFAULT_VOICE"); v != "" {
		defVoice = v
	} else if v := os.Getenv("GLADYSS_VOICE"); v != "" {
		defVoice = v
	} else if v := os.Getenv("SAY_VOICE"); v != "" {
		defVoice = v
	}

	defSpeed := 1.0
	if cfg.Speed != nil {
		defSpeed = *cfg.Speed
	}
	if v := os.Getenv("GLADYSS_SPEED"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defSpeed = f
		}
	} else if v := os.Getenv("SAY_SPEED"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defSpeed = f
		}
	}

	defPitch := 1.0
	if cfg.Pitch != nil {
		defPitch = *cfg.Pitch
	}
	if v := os.Getenv("GLADYSS_PITCH"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defPitch = f
		}
	} else if v := os.Getenv("SAY_PITCH"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defPitch = f
		}
	}

	defEOS := defaultEOSThreshold
	if cfg.EOSThreshold != nil {
		defEOS = *cfg.EOSThreshold
	}
	if v := os.Getenv("GLADYSS_EOS_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defEOS = f
		}
	} else if v := os.Getenv("SAY_EOS_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			defEOS = f
		}
	}

	defIdleTimeout := 15 * time.Minute
	if cfg.IdleTimeout != "" {
		if d, err := time.ParseDuration(cfg.IdleTimeout); err == nil {
			defIdleTimeout = d
		}
	}

	defPython := ".venv/bin/python"
	if cfg.Python != "" {
		defPython = cfg.Python
	}
	defDaemon := "tts_daemon.py"
	if cfg.Daemon != "" {
		defDaemon = cfg.Daemon
	}
	defPlayer := "ffplay"
	if cfg.Player != "" {
		defPlayer = cfg.Player
	}
	defConverter := "ffmpeg"
	if cfg.Converter != "" {
		defConverter = cfg.Converter
	}

	addr := flag.String("addr", defAddr, "listen address")
	voice := flag.String("voice", defVoice, "voice: the 26 from the Pocket TTS catalog or a cloned voice from voix/ (see README)")
	python := flag.String("python", defPython, "daemon's Python interpreter")
	script := flag.String("daemon", defDaemon, "synthesis daemon script")
	player := flag.String("player", defPlayer, "audio player receiving PCM on stdin")
	converter := flag.String("converter", defConverter, "converter applying filters outside playback (/v1/audio/speech)")
	speed := flag.Float64("speed", defSpeed, "default speech rate (0.5 to 3.0)")
	pitch := flag.Float64("pitch", defPitch, "default voice pitch (0.5 to 2.0)")
	eosThreshold := flag.Float64("eos-threshold", defEOS,
		"model end-of-speech detection threshold: higher means it keeps going longer (see README)")
	idleTimeout := flag.Duration("idle-timeout", defIdleTimeout,
		"idle delay before the model is unloaded (0 to never unload)")
	flag.Parse()

	// Les chemins par défaut sont relatifs au binaire : le service reste
	// lançable depuis n'importe quel répertoire.
	root := binaryDir()
	pythonPath := resolve(root, *python)
	scriptPath := resolve(root, *script)

	if !validSpeed(*speed) {
		log.Fatalf("speed %v out of bounds: expected between %v and %v", *speed, speedMin, speedMax)
	}
	if !validPitch(*pitch) {
		log.Fatalf("pitch %v out of bounds: expected between %v and %v", *pitch, pitchMin, pitchMax)
	}

	// Le daemon Python et son modèle ne démarrent qu'au premier énoncé — le
	// service reste léger tant que personne ne parle.
	engine := NewLazyEngine(func() (*PocketTTS, error) {
		return NewPocketTTS(pythonPath, scriptPath, *voice, *player, *converter,
			*speed, *pitch, *eosThreshold)
	}, *idleTimeout)
	defer engine.Close()

	controller := NewController(engine)
	controller.Start()

	server := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(newServer(controller, engine, *voice, engine.Voices, *speed, *pitch)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("listening on http://%s — POST /say, /v1/audio/speech, /skip, /stop, GET /queue, /voices", *addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-stop
	log.Println("shutdown requested…")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	controller.Close()
	log.Println("stopped")
}

// logRequests trace chaque requête, sans le corps.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func binaryDir() string {
	path, err := os.Executable()
	if err != nil {
		return "."
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Dir(path)
}

// resolve rend un chemin relatif absolu par rapport à la racine du projet.
func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	// Un chemin relatif existant depuis le répertoire courant a la priorité :
	// c'est le cas quand on lance `go run .` depuis les sources.
	if _, err := os.Stat(path); err == nil {
		abs, err := filepath.Abs(path)
		if err == nil {
			return abs
		}
	}
	return filepath.Join(root, path)
}
