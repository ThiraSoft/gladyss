package oral

import "testing"

func TestGroupeurAttendUnEnonceAssezLongPourLeModele(t *testing.T) {
	var g grouper

	if got := g.ajouter("Lumière sur les yeux,", Seuil); got != "" {
		t.Errorf("premier énoncé court émis seul (%q) : le modèle le tronquerait", got)
	}
	if got := g.ajouter("Le monde réel s'efface,", Seuil); got != "" {
		t.Errorf("deux énoncés courts émis trop tôt (%q)", got)
	}
	got := g.ajouter("L'âme en pixels pur.", Seuil)
	want := "Lumière sur les yeux,\nLe monde réel s'efface,\nL'âme en pixels pur."
	if got != want {
		t.Errorf("énoncé regroupé = %q, want %q", got, want)
	}
	if reste := g.vider(); reste != "" {
		t.Errorf("reste après émission = %q, want vide", reste)
	}
}

func TestGroupeurLaisseUnEnonceLongPasserSeul(t *testing.T) {
	var g grouper
	long := "Cet énoncé dépasse à lui seul le seuil, il part donc sans attendre le suivant."
	if got := g.ajouter(long, Seuil); got != long {
		t.Errorf("énoncé long = %q, want %q sans regroupement", got, long)
	}
}

// Min à zéro : chaque énoncé part seul, ce que veut un appelant qui rend
// lui-même chaque phrase (Avatar et sa bouche, une phrase par clip).
func TestGroupeurSansSeuilEmetAussitot(t *testing.T) {
	var g grouper
	if got := g.ajouter("Oui.", 0); got != "Oui." {
		t.Errorf("ajouter() = %q, want \"Oui.\" sans attendre", got)
	}
}

// Un « : » en fin d'énoncé annonce une énumération que le narrateur a rejetée.
// Chaque énoncé étant synthétisé seul, le modèle ne verra jamais la suite :
// l'intonation resterait suspendue.
func TestGroupeurFermeUnDeuxPointsOrphelin(t *testing.T) {
	var g grouper
	g.ajouter("Le compte est juste au bit près :", Seuil)
	if got := g.vider(); got != "Le compte est juste au bit près." {
		t.Errorf("vider() = %q, want \"Le compte est juste au bit près.\"", got)
	}
}

// Le « : » à l'intérieur d'un énoncé introduit bien ce qui suit : il reste.
func TestGroupeurGardeUnDeuxPointsInterne(t *testing.T) {
	var g grouper
	if got := g.ajouter("Le verdict est simple :", Seuil); got != "" {
		t.Fatalf("ajouter() = %q, want \"\" (énoncé encore trop court)", got)
	}
	got := g.ajouter("rien ne se perd entre le service et le lecteur.", Seuil)
	want := "Le verdict est simple :\nrien ne se perd entre le service et le lecteur."
	if got != want {
		t.Errorf("ajouter() = %q, want %q", got, want)
	}
}
