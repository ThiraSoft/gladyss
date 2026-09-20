// Package oral — narrateur.go : le narrateur décide quelles lignes d'une
// réponse méritent d'être lues à voix haute. RuleNarrator applique des règles ;
// une autre implémentation (un modèle, par exemple) pourra le remplacer
// derrière la même interface sans toucher au découpage.
package oral

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	maxLineLen    = 200  // au-delà : probablement technique, non oral
	maxSymbolFrac = 0.30 // fraction de symboles non alphanumériques tolérée
)

var (
	pathRe    = regexp.MustCompile(`(^|\s)(\.{0,2}/[\w./-]+|[\w.-]+/[\w.-]+/[\w./-]*|[\w-]+/[\w./-]+\.\w+)`)
	urlRe     = regexp.MustCompile(`https?://`)
	cmdRe     = regexp.MustCompile(`^(\$ |sudo |git |npm |cargo )`)
	numListRe = regexp.MustCompile(`^\d+[.)]\s`)
	sepRe     = regexp.MustCompile(`^(---+|\*\*\*+|___+)$`) // ---, ***, ___
	emphRe    = regexp.MustCompile(`(\*\*|__|\*|_)`)
)

// Narrator décide, à partir du texte reçu, quelles lignes lire à voix haute.
// Retour vide = rien à lire pour l'instant.
type Narrator interface {
	Filter(text string) []string
	// PerTurn indique si le narrateur préfère recevoir le tour complet (true)
	// plutôt que des fragments en streaming (false).
	PerTurn() bool
}

// RuleNarrator filtre par règles, ligne par ligne.
type RuleNarrator struct{}

// NewRuleNarrator construit le narrateur par règles.
func NewRuleNarrator() *RuleNarrator { return &RuleNarrator{} }

// PerTurn : le narrateur par règles filtre ligne par ligne en streaming.
func (r *RuleNarrator) PerTurn() bool { return false }

// Filter découpe text en lignes, nettoie le markdown décoratif, rejette les
// lignes non orales (code, chemins, listes, lignes techniques) et renvoie les
// lignes restantes prêtes à lire.
func (r *RuleNarrator) Filter(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// Les marqueurs de liste et le code sont jugés avant la décoration :
		// celle-ci retire les « * », qui ne distingueraient plus une puce
		// d'une emphase.
		if rejectRaw(trimmed) {
			continue
		}
		clean := decorate(trimmed)
		if clean == "" || reject(clean) {
			continue
		}
		// La ponctuation vient après le rejet : elle est une finition orale, et
		// juger la densité technique d'une ligne à laquelle on vient d'ajouter un
		// point ferait basculer les cas limites de symbolFraction.
		out = append(out, ponctuer(clean))
	}
	return out
}

// decorate retire la syntaxe markdown décorative (titres, emphase, citation) et
// renvoie le texte lisible ; un séparateur pur devient "".
func decorate(line string) string {
	if sepRe.MatchString(line) {
		return ""
	}
	line = strings.TrimLeft(line, "#")       // titres
	line = strings.TrimLeft(line, ">")       // citations
	line = emphRe.ReplaceAllString(line, "") // gras/italique
	return strings.TrimSpace(line)
}

// ponctuationsFinales : les fins de ligne qui donnent au modèle une frontière.
// Le moteur découpe sur « [.!?…:;] » puis fusionne à l'espace tout segment plus
// court que sa longueur minimale ; une ligne sans ponctuation se soude donc à
// la suivante, et « Le verdict » + « Rien ne se perd. » devient une proposition
// unique que le modèle intonne n'importe comment.
const ponctuationsFinales = ".!?…:;,"

// ponctuer donne un point aux lignes qui n'ont pas de ponctuation finale. Les
// titres markdown sont le cas courant : « ## Le verdict » perd son croisillon et
// arrive nu.
func ponctuer(line string) string {
	if line == "" {
		return line
	}
	lettres := []rune(line)
	if strings.ContainsRune(ponctuationsFinales, lettres[len(lettres)-1]) {
		return line
	}
	return line + "."
}

// rejectRaw juge la ligne sous sa forme brute, avant décoration, là où « * »
// distingue encore une puce d'une emphase.
func rejectRaw(line string) bool {
	return strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") ||
		strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "|") ||
		numListRe.MatchString(line)
}

// reject indique si une ligne nettoyée (après décoration) ne doit pas être lue.
func reject(line string) bool {
	if strings.Contains(line, "`") {
		return true
	}
	if urlRe.MatchString(line) || pathRe.MatchString(line) || cmdRe.MatchString(line) {
		return true
	}
	if len([]rune(line)) > maxLineLen {
		return true
	}
	return symbolFraction(line) > maxSymbolFrac
}

// symbolFraction renvoie la part de caractères non alphanumériques et non
// espaces dans line (heuristique de densité technique).
func symbolFraction(line string) float64 {
	var symbols, total int
	for _, r := range line {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			symbols++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(symbols) / float64(total)
}
