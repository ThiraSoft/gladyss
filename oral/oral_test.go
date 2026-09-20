package oral

import (
	"reflect"
	"testing"
)

// Le découpage à la phrase est ce qui fait la latence : attendre la fin d'une
// ligne, c'est attendre la fin d'un paragraphe.
func TestStreamCoupeALaPhraseSansAttendreLaLigne(t *testing.T) {
	s := New(Options{})
	var got []string
	for _, piece := range []string{"Bonj", "our. Comment", " vas-tu ? Il fait 3", ".5 degrés", "... Bref", "!\nEt", " toi"} {
		got = append(got, s.Push(piece)...)
	}
	want := []string{"Bonjour.", "Comment vas-tu ?", "Il fait 3.5 degrés...", "Bref!"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
	if rest := s.Flush(); !reflect.DeepEqual(rest, []string{"Et toi"}) {
		t.Fatalf("Flush() = %#v, want [\"Et toi\"]", rest)
	}
}

// Un point suivi de rien n'est pas encore une fin de phrase : « 3 » pourrait
// devenir « 3.5 », et « fini. » pourrait devenir « fini.. ».
func TestStreamGardeUneFinDePhraseIncertaine(t *testing.T) {
	s := New(Options{})
	if got := s.Push("Déjà fini."); len(got) != 0 {
		t.Fatalf("%q coupé avant de savoir ce qui suit le point", got)
	}
	if got := s.Flush(); !reflect.DeepEqual(got, []string{"Déjà fini."}) {
		t.Fatalf("Flush() = %#v", got)
	}
}

// Les trois barres peuvent arriver en deux morceaux : la bascule doit se faire
// sur la ligne complète, pas sur le chunk.
func TestStreamIgnoreUnBlocDeCodeAChevalSurDeuxChunks(t *testing.T) {
	s := New(Options{Narrator: NewRuleNarrator()})
	var got []string
	got = append(got, s.Push("Voici un résumé clair.\n")...)
	got = append(got, s.Push("``")...)
	got = append(got, s.Push("`go\ncode := 1\n```\n")...)
	got = append(got, s.Push("Fin du propos.\n")...)
	want := []string{"Voici un résumé clair.", "Fin du propos."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
}

// Sans narrateur, le texte passe tel quel : c'est ce que veut un appelant qui
// pose ses propres marques dans la phrase (Avatar et ses tags d'émotion).
func TestStreamSansNarrateurGardeLeTexte(t *testing.T) {
	s := New(Options{})
	got := s.Push("[3] Le fichier /home/x.go change.\n")
	want := []string{"[3] Le fichier /home/x.go change."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
}

// Un bloc de code n'est jamais dit, narrateur ou pas : il s'écrit, il ne se
// prononce pas. C'est la seule chose qu'un appelant sans narrateur perd.
func TestStreamSansNarrateurEcarteQuandMemeLeCode(t *testing.T) {
	s := New(Options{})
	var got []string
	got = append(got, s.Push("Voilà le code.\n```go\nfmt.Println(1)\n```\n")...)
	got = append(got, s.Push("Et voilà.\n")...)
	want := []string{"Voilà le code.", "Et voilà."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
}

// Sous le seuil, le modèle tronque : les énoncés courts attendent la suite.
func TestStreamRegroupeSousLeSeuil(t *testing.T) {
	s := New(Options{Narrator: NewRuleNarrator(), Min: Seuil})
	if got := s.Push("Lumière sur les yeux,\nLe monde réel s'efface,\n"); len(got) != 0 {
		t.Fatalf("énoncés = %#v, want rien tant que le seuil n'est pas atteint", got)
	}
	got := s.Push("L'âme en pixels pur.\n")
	want := []string{"Lumière sur les yeux,\nLe monde réel s'efface,\nL'âme en pixels pur."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
}

// En fin de tour, mieux vaut un énoncé court qu'un énoncé jamais dit.
func TestStreamFlushVideLeRegroupeur(t *testing.T) {
	s := New(Options{Narrator: NewRuleNarrator(), Min: Seuil})
	if got := s.Push("Oui, c'est ça.\n"); len(got) != 0 {
		t.Fatalf("énoncé court émis seul : %#v", got)
	}
	if got := s.Flush(); !reflect.DeepEqual(got, []string{"Oui, c'est ça."}) {
		t.Fatalf("Flush() = %#v, want [\"Oui, c'est ça.\"]", got)
	}
	if got := s.Flush(); len(got) != 0 {
		t.Fatalf("second Flush() = %#v, want rien", got)
	}
}

// Deux tours de suite ne doivent pas se mélanger : Flush remet tout à zéro,
// buffer, bloc de code ouvert et regroupement compris.
func TestStreamFlushRepartDeZero(t *testing.T) {
	s := New(Options{Narrator: NewRuleNarrator(), Min: Seuil})
	s.Push("```\ncode\n")
	s.Flush()
	got := s.Push("Le tour suivant est prononcé en entier, bloc de code refermé.\n")
	want := []string{"Le tour suivant est prononcé en entier, bloc de code refermé."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("énoncés = %#v, want %#v", got, want)
	}
}
