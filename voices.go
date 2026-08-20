package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ThiraSoft/golem/pockettts"
)

// voiceCatalog résout un nom de voix vers le fichier d'état que le moteur sait
// charger. Deux sources : le répertoire voix/ du dépôt, et le catalogue publié
// par Kyutai dans le cache Hugging Face. Les voix locales gagnent, pour qu'un
// fichier posé ici puisse recouvrir une voix du catalogue.
type voiceCatalog struct {
	dir  string
	lang pockettts.Language
}

func newVoiceCatalog(dir string, lang pockettts.Language) *voiceCatalog {
	return &voiceCatalog{dir: dir, lang: lang}
}

// voiceSource dit où prendre une voix, et s'il faut l'encoder avant de s'en
// servir. Un état précalculé se lit en quelques millisecondes ; un
// enregistrement coûte quelques secondes, une fois, et le résultat est mis en
// cache à côté de lui.
type voiceSource struct {
	path  string
	clone bool // path est un WAV à encoder, pas un état
}

// cache est l'endroit où l'état encodé d'un WAV est écrit, pour que le
// démarrage suivant le relise au lieu de le recalculer.
func (c *voiceCatalog) cache(name string) string {
	return filepath.Join(c.dir, name+".safetensors")
}

// resolve dit d'où vient la voix.
func (c *voiceCatalog) resolve(name string) (voiceSource, error) {
	if _, err := os.Stat(c.cache(name)); err == nil {
		return voiceSource{path: c.cache(name)}, nil
	}

	// Un enregistrement passe avant le catalogue : quelqu'un qui pose un WAV
	// dans voix/ veut cette voix-là, même si Kyutai en publie une du même nom.
	wav := filepath.Join(c.dir, name+".wav")
	if _, err := os.Stat(wav); err == nil {
		return voiceSource{path: wav, clone: true}, nil
	}

	if p := pockettts.Locate(c.lang.EmbeddingPath(name)); p != "" {
		return voiceSource{path: p}, nil
	}

	return voiceSource{}, fmt.Errorf("unknown voice %q: not in %s, and not in the Pocket TTS catalog for %s",
		name, c.dir, c.lang.Name)
}

// names rend l'union des deux sources, triée et sans doublon. Le service s'en
// sert pour valider une voix demandée et pour répondre à GET /voices.
func (c *voiceCatalog) names() []string {
	seen := map[string]bool{}
	for _, n := range pockettts.LocateVoices(c.lang) {
		seen[n] = true
	}
	for _, ext := range []string{"*.safetensors", "*.wav"} {
		found, _ := filepath.Glob(filepath.Join(c.dir, ext))
		for _, p := range found {
			base := filepath.Base(p)
			seen[strings.TrimSuffix(strings.TrimSuffix(base, ".safetensors"), ".wav")] = true
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
