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
	maxLineLen    = 200  // au-delà, la ligne est coupée en morceaux plus courts
	maxSymbolFrac = 0.30 // fraction de symboles non alphanumériques tolérée
)

var (
	pathRe    = regexp.MustCompile(`(^|\s)(\.{0,2}/[\w./-]+|[\w.-]+/[\w.-]+/[\w./-]*|[\w-]+/[\w./-]+\.\w+)`)
	urlRe     = regexp.MustCompile(`https?://`)
	cmdRe     = regexp.MustCompile(`^(\$ |sudo |git |npm |cargo )`)
	numListRe = regexp.MustCompile(`^\d+[.)]\s`)
	sepRe     = regexp.MustCompile(`^(---+|\*\*\*+|___+)$`) // ---, ***, ___
	emphRe    = regexp.MustCompile(`(\*\*|__|\*|_)`)
	lienRe    = regexp.MustCompile(`\[([^\]]+)\]\([^()\s]+\)`)
	codeRe    = regexp.MustCompile("`([^`]*)`")
	motRe     = regexp.MustCompile(`^[\pL\pN][\pL\pN_.-]{0,39}$`)
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
		// Liens et code inline passent avant la décoration : celle-ci prend
		// le « _ » d'un identifiant pour une emphase.
		clean, ok := codeInline(lienRe.ReplaceAllString(trimmed, "$1"))
		if !ok {
			continue
		}
		clean = decorate(clean)
		if clean == "" || reject(clean) {
			continue
		}
		// La ponctuation vient après le rejet : elle est une finition orale, et
		// juger la densité technique d'une ligne à laquelle on vient d'ajouter un
		// point ferait basculer les cas limites de symbolFraction.
		for _, morceau := range couper(clean, maxLineLen) {
			out = append(out, ponctuer(morceau))
		}
	}
	return out
}

// codeInline remplace chaque `code` qui se dit comme un mot (un identifiant,
// un nom de fichier) par son contenu, « _ » changé en espace. ok est faux dès
// qu'un passage ne se dit pas (une commande, un appel, un chemin) : la ligne
// entière est alors écartée, comme avant.
func codeInline(line string) (string, bool) {
	ok := true
	line = codeRe.ReplaceAllStringFunc(line, func(m string) string {
		mot := strings.Trim(m, "`")
		if !motRe.MatchString(mot) {
			ok = false
			return m
		}
		return strings.ReplaceAll(mot, "_", " ")
	})
	return line, ok
}

// couper découpe une ligne trop longue pour être dite d'un trait. La coupe
// tombe de préférence après une virgule, un point-virgule ou un deux-points,
// à défaut entre deux mots. Le moteur ne découpe lui-même que sur la
// ponctuation forte, une proposition interminable lui arriverait entière.
func couper(line string, max int) []string {
	var out []string
	for {
		r := []rune(line)
		if len(r) <= max {
			return append(out, line)
		}
		cut := -1
		for i := max; i > 0; i-- {
			if r[i] == ' ' && strings.ContainsRune(",;:", r[i-1]) {
				cut = i
				break
			}
		}
		if cut < 0 {
			for i := max; i > 0; i-- {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
		}
		if cut < 0 {
			cut = max
		}
		out = append(out, strings.TrimSpace(string(r[:cut])))
		line = strings.TrimSpace(string(r[cut:]))
	}
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
