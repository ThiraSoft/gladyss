// Package oral transforme un texte qui arrive au fil de l'eau en énoncés prêts
// à être prononcés. C'est la moitié texte de la synthèse en streaming : le
// modèle écrit encore la suite pendant que la première phrase est déjà dite.
//
// Trois étages, tous facultatifs sauf le premier :
//
//   - le découpage, qui coupe à la fin de chaque phrase et de chaque ligne, et
//     laisse les blocs de code entiers de côté ;
//   - le narrateur, qui écarte ce qui ne se dit pas (chemins, commandes,
//     listes, tableaux) et nettoie le markdown décoratif ;
//   - le regroupement, qui retient les énoncés trop courts pour être rendus
//     proprement et les émet ensemble.
package oral

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Seuil est la longueur d'énoncé en dessous de laquelle le modèle devient
// imprévisible : mesuré sur « Lumière sur les yeux, » demandée six fois,
// l'audio rendu va de 0,71 s à 2,13 s, alors qu'un énoncé long est stable à
// 4 % près. C'est la valeur à donner à Options.Min pour une lecture parlée.
const Seuil = 60

// htmlRe retire le HTML inline qu'un modèle glisse parfois dans sa prose.
var htmlRe = regexp.MustCompile(`<[^>]+>.*?</[^>]+>|<[^>]+/>`)

// Options règle les étages posés après le découpage.
type Options struct {
	// Narrator écarte les lignes qui ne se disent pas. Nil : tout est dit, ce
	// que veut un appelant qui pose ses propres marques dans la phrase.
	Narrator Narrator
	// Min est la longueur minimale d'un énoncé, en caractères. En dessous,
	// l'énoncé attend le suivant. Zéro : chaque énoncé part seul.
	Min int
}

// Stream accumule le texte reçu et rend les énoncés devenus prononçables.
// Il n'est pas concurrent : un seul appelant à la fois, soit un tour de parole.
type Stream struct {
	opt    Options
	buf    string
	inCode bool
	group  grouper
}

// New construit un flux réglé par o.
func New(o Options) *Stream { return &Stream{opt: o} }

// Push ajoute un morceau de texte et renvoie les énoncés qu'il a terminés.
func (s *Stream) Push(text string) []string {
	s.buf += text
	var out []string
	start := 0
	for i, r := range s.buf {
		cut := -1
		switch {
		case r == '\n':
			cut = i + 1
		case unicode.IsSpace(r) && i > 0:
			// Une fin de phrase ne se reconnaît qu'à ce qui la suit : sans
			// l'espace, « 3 » pourrait devenir « 3.5 » et « Bref.. » « Bref... ».
			prev, _ := utf8.DecodeLastRuneInString(s.buf[:i])
			if finDePhrase(prev) {
				cut = i
			}
		}
		if cut <= start {
			continue
		}
		out = append(out, s.dire(s.buf[start:cut])...)
		start = cut
	}
	s.buf = s.buf[start:]
	return out
}

// Flush rend ce qui reste une fois le texte fini, regroupement compris, et
// remet le flux à neuf pour le tour suivant. Mieux vaut un énoncé court, au
// risque d'être tronqué par le modèle, qu'un énoncé jamais prononcé.
func (s *Stream) Flush() []string {
	out := s.dire(s.buf)
	s.buf, s.inCode = "", false
	if reste := s.group.vider(); reste != "" {
		out = append(out, reste)
	}
	return out
}

// dire fait passer un segment par les trois étages et renvoie ce qui en sort.
func (s *Stream) dire(segment string) []string {
	segment = nettoyer(segment)
	// La bascule se fait sur le segment entier, jamais sur le morceau reçu :
	// les trois barres arrivent souvent coupées en deux.
	if strings.Contains(segment, "```") {
		s.inCode = !s.inCode
		return nil
	}
	if s.inCode {
		return nil
	}

	var lignes []string
	if s.opt.Narrator != nil {
		lignes = s.opt.Narrator.Filter(segment)
	} else if dit := strings.TrimSpace(segment); dit != "" {
		lignes = []string{dit}
	}

	var out []string
	for _, ligne := range lignes {
		if enonce := s.group.ajouter(ligne, s.opt.Min); enonce != "" {
			out = append(out, enonce)
		}
	}
	return out
}

// nettoyer normalise les lignes vides et retire le HTML inline.
func nettoyer(segment string) string {
	for strings.Contains(segment, "\n\n") {
		segment = strings.ReplaceAll(segment, "\n\n", "\n")
	}
	return htmlRe.ReplaceAllString(segment, "")
}

// finDePhrase reconnaît les marques qui ferment une phrase.
func finDePhrase(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '…' }

// grouper retient les énoncés trop courts pour être prononcés seuls. Les
// énoncés gardent leur séparation : le moteur découpe ensuite lui-même par
// phrase, la prosodie reste celle d'un énoncé continu.
type grouper struct {
	attente []string
	taille  int
}

// ajouter empile un énoncé et renvoie ce qu'il y a à dire, ou "" tant que min
// n'est pas atteint.
func (g *grouper) ajouter(enonce string, min int) string {
	g.attente = append(g.attente, enonce)
	g.taille += len([]rune(enonce))
	if g.taille < min {
		return ""
	}
	return g.vider()
}

// vider rend ce qui reste en attente.
func (g *grouper) vider() string {
	if len(g.attente) == 0 {
		return ""
	}
	enonce := strings.Join(g.attente, "\n")
	g.attente, g.taille = nil, 0
	return fermerDeuxPoints(enonce)
}

// fermerDeuxPoints remplace un « : » final par un point. Ce « : » annonçait une
// énumération que le narrateur a rejetée (liste, ligne technique) ; comme chaque
// énoncé est synthétisé seul, le modèle ne verra jamais de suite et laisserait
// l'intonation suspendue.
func fermerDeuxPoints(enonce string) string {
	if !strings.HasSuffix(enonce, ":") {
		return enonce
	}
	return strings.TrimRight(strings.TrimSuffix(enonce, ":"), " ") + "."
}
