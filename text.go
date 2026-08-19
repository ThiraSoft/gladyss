package main

import (
	"regexp"
	"strings"
)

// Le modèle tokenise le texte avec un SentencePiece de 4 000 tokens seulement.
// Tout caractère absent de ce vocabulaire est découpé en octets UTF-8 bruts
// (« l’entrée » devient ▁l <0xE2> <0x80> <0x99> ent r é e), séquences que le
// modèle n'a jamais vues proprement à l'entraînement : il hache la syllabe ou
// avale le mot. Le trou est large en français — toutes les majuscules
// accentuées, ë î ï ù û ÿ œ æ, les guillemets, les parenthèses, les points de
// suspension, les espaces insécables.
//
// replacements ramène ces caractères dans le vocabulaire, famille par famille.
// L'ordre des paires compte : à une position donnée, strings.Replacer retient la
// première qui correspond — d'où « n° » avant « ° ».
// sentinel marque une espace née du nettoyage, par opposition à celles du
// texte d'origine. Elle disparaît devant une ponctuation — « 12 € , » n'aurait
// pas de sens — et devient une espace ordinaire partout ailleurs. Un caractère
// nul ne peut pas venir du texte : il est retiré de l'entrée par précaution.
const sentinel = "\x00"

var replacements = strings.NewReplacer(
	// Apostrophes : retirées, les deux morceaux soudés. « l'entrée » devient
	// « lentrée », qui se prononce pareil ; insérer une espace ferait lire deux
	// mots. Toutes les variantes y passent, y compris la typographique des
	// traitements de texte, qui est le pire cas — trois octets bruts en plein mot.
	"'", "", "’", "", "‘", "", "‛", "", "ʼ", "", "ʹ", "", "′", "", "´", "", "`", "", "＇", "",

	// Majuscules accentuées : minusculisées. La minuscule correspondante est
	// dans le vocabulaire et se prononce à l'identique — la casse ne s'entend pas.
	"À", "à", "Â", "â", "Ä", "ä", "É", "é", "È", "è", "Ê", "ê",
	"Ô", "ô", "Ö", "ö", "Ç", "ç",

	// Lettres absentes du vocabulaire, dans les deux casses : translittérées
	// vers la graphie sans diacritique, qui garde la prononciation attendue
	// (« maître » → « maitre », « sûr » → « sur », « où » → « ou »). Seul « ï »
	// est un compromis : « naïf » → « naif » perd le tréma, mais un tréma en
	// octets bruts coûtait le mot entier.
	"Ë", "E", "Î", "I", "Ï", "I", "Ù", "U", "Û", "U", "Ü", "U", "Ÿ", "Y",
	"ë", "e", "î", "i", "ï", "i", "ù", "u", "û", "u", "ü", "u", "ÿ", "y",
	"Œ", "Oe", "Æ", "Ae", "œ", "oe", "æ", "ae",

	// Points de suspension : le caractère unique est hors vocabulaire, les trois
	// points y sont. Le daemon découpe les phrases sur « . » : la segmentation
	// est préservée.
	"…", "...",

	// Ponctuation qui ne se prononce pas : effacée derrière une sentinelle, pour
	// ne pas souder les mots qu'elle séparait (« km/h » doit rester deux mots).
	"«", sentinel, "»", sentinel, "“", sentinel, "”", sentinel, "„", sentinel, "‟", sentinel,
	"(", sentinel, ")", sentinel, "[", sentinel, "]", sentinel, "{", sentinel, "}", sentinel,
	"•", sentinel, "·", sentinel, "†", sentinel, "‡", sentinel, "¶", sentinel,
	"©", sentinel, "®", sentinel, "™", sentinel,
	"/", sentinel, "\\", sentinel, "|", sentinel, "~", sentinel, "^", sentinel,
	"<", sentinel, ">", sentinel, "#", sentinel,

	// Symboles à lecture univoque : dits en mots, comme les lirait quelqu'un.
	// « n° » d'abord, sinon « n° 5 » sortirait en « n degrés 5 ».
	"n°", "numéro"+sentinel, "N°", "numéro"+sentinel,
	"°C", sentinel+"degrés Celsius", "°F", sentinel+"degrés Fahrenheit",
	"°", sentinel+"degrés"+sentinel,
	"€", sentinel+"euros"+sentinel, "£", sentinel+"livres"+sentinel,
	"¥", sentinel+"yens"+sentinel, "§", sentinel+"paragraphe"+sentinel,
	"×", sentinel+"fois"+sentinel, "÷", sentinel+"divisé par"+sentinel,
	"±", sentinel+"plus ou moins"+sentinel, "≠", sentinel+"différent de"+sentinel,
	"≈", sentinel+"environ"+sentinel, "≤", sentinel+"inférieur ou égal à"+sentinel,
	"≥", sentinel+"supérieur ou égal à"+sentinel, "+", sentinel+"plus"+sentinel,
	"=", sentinel+"égale"+sentinel, "@", sentinel+"arobase"+sentinel,

	// Espaces exotiques : l'insécable est partout en français (avant « : ; ! ? »,
	// dans « 10 000 ») et hors vocabulaire. Ramenées à l'espace ordinaire ;
	// la largeur nulle, elle, ne sépare rien et disparaît.
	" ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ", " ",
	"​", "",
)

var (
	// Les retours à la ligne sont épargnés : le daemon s'en sert pour découper
	// les phrases, les écraser changerait la prosodie.
	horizontalSpaces = regexp.MustCompile(`[^\S\n]+`)
	lineEdgeSpaces   = regexp.MustCompile(`(?m)^ +| +$`)

	// Une sentinelle au contact d'une ponctuation s'efface, avec l'espace que le
	// caractère disparu laissait derrière lui — « à savoir « ceci ». » ne doit
	// pas rendre « ceci . ». Ailleurs la sentinelle sépare deux mots : elle
	// devient une espace.
	uselessSentinel   = regexp.MustCompile("[^\\S\n]*\x00+[^\\S\n]*([,.;:!?])")
	remainingSentinel = regexp.MustCompile("\x00+")
)

// cleanText réécrit le texte dans ce que le tokenizer du modèle sait lire.
//
// Rien de ce qui s'entend n'est perdu : les caractères retirés sont ceux qui ne
// se prononcent pas, les autres sont translittérés vers une graphie de même
// prononciation ou dits en mots. Le texte rendu est donc celui qui sera
// réellement prononcé — c'est lui que /say renvoie au client.
func cleanText(text string) string {
	text = strings.ReplaceAll(text, sentinel, "")
	text = replacements.Replace(text)
	text = uselessSentinel.ReplaceAllString(text, "$1")
	text = remainingSentinel.ReplaceAllString(text, " ")
	text = horizontalSpaces.ReplaceAllString(text, " ")
	text = lineEdgeSpaces.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}
