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

// resolve rend le chemin du .safetensors de la voix.
func (c *voiceCatalog) resolve(name string) (string, error) {
	local := filepath.Join(c.dir, name+".safetensors")
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}

	if p := pockettts.Locate(c.lang.EmbeddingPath(name)); p != "" {
		return p, nil
	}

	// Un WAV seul est une voix à cloner, et le clonage demande l'encodeur Mimi,
	// que golem n'a pas encore. Le dire vaut mieux que « voix inconnue » : le
	// fichier est bien là, c'est l'outil qui manque.
	wav := filepath.Join(c.dir, name+".wav")
	if _, err := os.Stat(wav); err == nil {
		return "", fmt.Errorf("voice %q has only %s: cloning from sound is not implemented yet, "+
			"provide %s instead", name, wav, local)
	}

	return "", fmt.Errorf("unknown voice %q: not in %s, and not in the Pocket TTS catalog for %s",
		name, c.dir, c.lang.Name)
}

// names rend l'union des deux sources, triée et sans doublon. Le service s'en
// sert pour valider une voix demandée et pour répondre à GET /voices.
func (c *voiceCatalog) names() []string {
	seen := map[string]bool{}
	for _, n := range pockettts.LocateVoices(c.lang) {
		seen[n] = true
	}
	found, _ := filepath.Glob(filepath.Join(c.dir, "*.safetensors"))
	for _, p := range found {
		seen[strings.TrimSuffix(filepath.Base(p), ".safetensors")] = true
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
