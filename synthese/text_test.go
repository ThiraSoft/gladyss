package synthese

import "testing"

// TestCleanRetireLesEmoji : le tokenizer ne sait pas les lire, et ils ne se
// prononcent pas. La règle vient d'avatar, qui l'avait ajoutée de son côté.
func TestCleanRetireLesEmoji(t *testing.T) {
	cas := []struct{ in, want string }{
		{"Bonjour 👋", "Bonjour"},
		{"C'est parti 🚀 on y va", "Cest parti on y va"},
		{"Rien à retirer ici", "Rien à retirer ici"},
	}
	for _, c := range cas {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, attendu %q", c.in, got, c.want)
		}
	}
}

// TestClean reprend les cas de avatar/internal/voice/voice_test.go (le
// fichier s'appelait clean_test.go dans le brief, mais chez avatar ces cas
// vivent dans voice_test.go). Ils couvrent l'emoji, l'emphase markdown et la
// translittération, en une seule passe.
func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"L'entrée « libre » 🙂":   "Lentrée libre",
		"C'est *vraiment* bien…": "Cest vraiment bien...",
		"Il fait 20 °C.":         "Il fait 20 degrés Celsius.",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, attendu %q", in, got, want)
		}
	}
}
