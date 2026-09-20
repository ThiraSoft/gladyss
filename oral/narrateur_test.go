package oral

import (
	"reflect"
	"strings"
	"testing"
)

func TestRuleNarrator_PlainProse(t *testing.T) {
	n := NewRuleNarrator()
	got := n.Filter("Bonjour, voici un résumé simple.")
	want := []string{"Bonjour, voici un résumé simple."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter() = %#v, want %#v", got, want)
	}
}

func TestRuleNarrator_RejectCodeAndPaths(t *testing.T) {
	n := NewRuleNarrator()
	cases := []string{
		"Utilise `fmt.Println` ici",         // backtick inline
		"Le fichier est /home/ricardo/x.go", // chemin absolu
		"Vois ./oral/narrateur.go pour ça",  // chemin relatif
		"Doc: https://example.com/page",     // URL
		"$ go test ./...",                   // commande shell
	}
	for _, in := range cases {
		if got := n.Filter(in); len(got) != 0 {
			t.Errorf("Filter(%q) = %#v, want empty", in, got)
		}
	}
}

func TestRuleNarrator_RejectListsAndTables(t *testing.T) {
	n := NewRuleNarrator()
	cases := []string{
		"- premier point",
		"* autre point",
		"+ encore un",
		"1. première étape",
		"| col a | col b |",
		"|---|---|",
	}
	for _, in := range cases {
		if got := n.Filter(in); len(got) != 0 {
			t.Errorf("Filter(%q) = %#v, want empty", in, got)
		}
	}
}

func TestRuleNarrator_DecorativeMarkdown(t *testing.T) {
	n := NewRuleNarrator()
	cases := map[string][]string{
		// Le point final est ajouté par ponctuer() : sans lui, le daemon souderait
		// le fragment à la phrase suivante. Voir
		// TestRuleNarrator_PonctueUneLigneSansPonctuationFinale.
		"# Titre important":       {"Titre important."},
		"## Sous-titre":           {"Sous-titre."},
		"C'est **vraiment** bien": {"C'est vraiment bien."},
		"> une citation":          {"une citation."},
		"---":                     nil, // séparateur pur : rien
		"***":                     nil,
	}
	for in, want := range cases {
		if got := n.Filter(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Filter(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestRuleNarrator_IndentedBulletRejected(t *testing.T) {
	n := NewRuleNarrator()
	cases := []string{
		"   - point indenté",
		"\t- autre point",
		"  * indenté étoile",
	}
	for _, in := range cases {
		if got := n.Filter(in); len(got) != 0 {
			t.Errorf("Filter(%q) = %#v, want empty (indented bullet must be rejected)", in, got)
		}
	}
}

func TestRuleNarrator_CmdReAnchoredAndGoDropped(t *testing.T) {
	n := NewRuleNarrator()
	// Prose containing "go" must NOT be rejected.
	read := []string{
		"Go pour la suite, on enchaîne.",
		"Il faut go ahead avec ce plan.",
		"C'est un go de notre part.",
	}
	for _, in := range read {
		if got := n.Filter(in); len(got) == 0 {
			t.Errorf("Filter(%q) = empty, want non-empty (prose with 'go' must be read)", in)
		}
	}
	// Commands at start of line must still be rejected.
	reject := []string{
		"git commit -m x",
		"npm install",
		"cargo build",
		"$ ls -la",
		"sudo apt update",
	}
	for _, in := range reject {
		if got := n.Filter(in); len(got) != 0 {
			t.Errorf("Filter(%q) = %#v, want empty (command line must be rejected)", in, got)
		}
	}
}

func TestRuleNarrator_PathRe(t *testing.T) {
	n := NewRuleNarrator()
	// Must be rejected.
	rejected := []string{
		"src/audio/player.go change",
		"src/audio/player bare path sans extension",
		"vois ./build.sh pour les détails",
		"../config/settings.yaml ici",
	}
	for _, in := range rejected {
		if got := n.Filter(in); len(got) != 0 {
			t.Errorf("Filter(%q) = %#v, want empty (path must be rejected)", in, got)
		}
	}
	// Must be read.
	read := []string{
		"le ratio A/B est bon",
		"vitesse en km/h mesurée",
		"choix et/ou préférence",
	}
	for _, in := range read {
		if got := n.Filter(in); len(got) == 0 {
			t.Errorf("Filter(%q) = empty, want non-empty (prose token must be read)", in)
		}
	}
}

func TestRuleNarrator_RejectTechnical(t *testing.T) {
	n := NewRuleNarrator()
	long := strings.Repeat("mot ", 60) // > 200 caractères
	if got := n.Filter(long); len(got) != 0 {
		t.Errorf("Filter(long) = %#v, want empty", got)
	}
	dense := "a=b; c<-d; e:=f{g}; h|i&j^k" // forte densité de symboles
	if got := n.Filter(dense); len(got) != 0 {
		t.Errorf("Filter(dense) = %#v, want empty", got)
	}
}

func TestRuleNarrator_PerTurn(t *testing.T) {
	if NewRuleNarrator().PerTurn() {
		t.Errorf("RuleNarrator.PerTurn() = true, want false (filtrage au fil de l'eau)")
	}
}

// Un titre markdown perd son « # » mais n'a pas de ponctuation finale. Le
// daemon fusionne alors le fragment avec la phrase suivante à l'espace
// (LONGUEUR_MIN_SEGMENT), et le modèle lit « Le verdict Nova ne perd rien »
// comme une seule proposition : l'intonation part n'importe où.
func TestRuleNarrator_PonctueUneLigneSansPonctuationFinale(t *testing.T) {
	n := NewRuleNarrator()
	cases := map[string][]string{
		"## Le verdict":            {"Le verdict."},
		"# Titre":                  {"Titre."},
		"Une phrase déjà ponctuée": {"Une phrase déjà ponctuée."},
		"Elle est ponctuée.":       {"Elle est ponctuée."},
		"Vraiment ?":               {"Vraiment ?"},
		"Attention !":              {"Attention !"},
		"Suspendu…":                {"Suspendu…"},
		"Un vers, une virgule,":    {"Un vers, une virgule,"},
	}
	for in, want := range cases {
		if got := n.Filter(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Filter(%q) = %#v, want %#v", in, got, want)
		}
	}
}
