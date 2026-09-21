package synthese

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// depotFactice sert l'API Hugging Face en petit : l'arbre, la révision et les
// fichiers. Les contenus sont des octets reconnaissables, pas de vrais poids.
func depotFactice(t *testing.T, contenus map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/kyutai/pocket-tts-without-voice-cloning", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sha": "0123456789abcdef0123456789abcdef01234567"})
	})
	mux.HandleFunc("/api/models/kyutai/pocket-tts-without-voice-cloning/tree/main/", func(w http.ResponseWriter, r *http.Request) {
		prefixe := strings.TrimPrefix(r.URL.Path, "/api/models/kyutai/pocket-tts-without-voice-cloning/tree/main/")
		var arbre []map[string]any
		for chemin, contenu := range contenus {
			if filepath.Dir(chemin) != strings.TrimSuffix(prefixe, "/") {
				continue
			}
			arbre = append(arbre, map[string]any{"path": chemin, "size": len(contenu), "type": "file"})
		}
		json.NewEncoder(w).Encode(arbre)
	})
	mux.HandleFunc("/kyutai/pocket-tts-without-voice-cloning/resolve/", func(w http.ResponseWriter, r *http.Request) {
		chemin := r.URL.Path[strings.Index(r.URL.Path, "/resolve/")+len("/resolve/"):]
		chemin = chemin[strings.Index(chemin, "/")+1:] // saute le sha
		contenu, ok := contenus[chemin]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(contenu))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

var contenusDeTest = map[string]string{
	"languages/french_24l/model.safetensors":           "des poids",
	"languages/french_24l/tokenizer.model":             "un tokenizer",
	"languages/french_24l/embeddings/mary.safetensors": "une voix",
}

func snapshot(home string) string {
	return filepath.Join(home, ".cache/huggingface/hub/models--kyutai--pocket-tts-without-voice-cloning/snapshots/0123456789abcdef0123456789abcdef01234567")
}

// TestAssurerTelechargeCeQuiManque : cache vide, tout doit descendre.
func TestAssurerTelechargeCeQuiManque(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := depotFactice(t, contenusDeTest)
	hfBase = s.URL
	t.Cleanup(func() { hfBase = "https://huggingface.co" })

	var vus []string
	if err := Assurer(context.Background(), func(p Progression) {
		if p.Recu == p.Total {
			vus = append(vus, p.Fichier)
		}
	}); err != nil {
		t.Fatalf("Assurer: %v", err)
	}

	for chemin, contenu := range contenusDeTest {
		b, err := os.ReadFile(filepath.Join(snapshot(home), chemin))
		if err != nil {
			t.Fatalf("%s: %v", chemin, err)
		}
		if string(b) != contenu {
			t.Errorf("%s = %q, attendu %q", chemin, b, contenu)
		}
	}
	if len(vus) != len(contenusDeTest) {
		t.Errorf("progression vue pour %d fichiers, attendu %d", len(vus), len(contenusDeTest))
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(snapshot(home))), "refs/main")); err != nil || len(b) != 40 {
		t.Errorf("refs/main non écrit : %v %q", err, b)
	}
}

// TestAssurerSauteCeQuiEstLa : un cache complet ne doit déclencher aucun
// téléchargement, même avec le réseau disponible.
func TestAssurerSauteCeQuiEstLa(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := depotFactice(t, contenusDeTest)
	hfBase = s.URL
	t.Cleanup(func() { hfBase = "https://huggingface.co" })

	if err := Assurer(context.Background(), nil); err != nil {
		t.Fatalf("premier Assurer: %v", err)
	}
	var second []string
	if err := Assurer(context.Background(), func(p Progression) { second = append(second, p.Fichier) }); err != nil {
		t.Fatalf("second Assurer: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("le second passage a retéléchargé %v", second)
	}
}

// TestAssurerNeLaissePasDeFichierTronque : une coupure en plein transfert doit
// laisser un .part, jamais un fichier que le passage suivant prendrait pour
// complet.
func TestAssurerNeLaissePasDeFichierTronque(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/models/kyutai/pocket-tts-without-voice-cloning", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"sha": "0123456789abcdef0123456789abcdef01234567"})
	})
	mux.HandleFunc("/api/models/kyutai/pocket-tts-without-voice-cloning/tree/main/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"path": "languages/french_24l/model.safetensors", "size": 1000, "type": "file"},
		})
	})
	mux.HandleFunc("/kyutai/pocket-tts-without-voice-cloning/resolve/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("début seulement"))
		// La connexion se ferme ici : le corps est plus court que l'annonce.
	})
	s := httptest.NewServer(mux)
	defer s.Close()
	hfBase = s.URL
	t.Cleanup(func() { hfBase = "https://huggingface.co" })

	if err := Assurer(context.Background(), nil); err == nil {
		t.Fatal("Assurer devrait signaler le transfert incomplet")
	}
	if _, err := os.Stat(filepath.Join(snapshot(home), "languages/french_24l/model.safetensors")); !os.IsNotExist(err) {
		t.Error("un fichier tronqué a été gardé sous son nom définitif")
	}
}

// TestAssurerHorsLigneAvecCachePourvu : Hugging Face injoignable n'est pas une
// erreur si tout est déjà là.
func TestAssurerHorsLigneAvecCachePourvu(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(snapshot(home), "languages/french_24l/embeddings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for chemin, contenu := range contenusDeTest {
		if err := os.WriteFile(filepath.Join(snapshot(home), chemin), []byte(contenu), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hfBase = "http://127.0.0.1:1" // rien n'écoute
	t.Cleanup(func() { hfBase = "https://huggingface.co" })

	if err := Assurer(context.Background(), nil); err != nil {
		t.Errorf("cache pourvu et réseau coupé : Assurer = %v, attendu nil", err)
	}
}
