// fusion mélange deux voix clonées sans repasser par l'audio.
//
// Une voix Pocket TTS est l'état du transformer après l'écoute d'un extrait :
// pour chaque couche, les clés et valeurs d'attention de chaque instant, clés
// déjà tournées par RoPE à leur position. Position 0 : le début d'écoute, puis
// une position par trame de 80 ms.
//
//	go run ./cmd/fusion -a voix/gladyss.safetensors -b voix/gladyss2.safetensors \
//	    -na 200 -nb 300 -o voix/mix.safetensors
//
// Deux modes :
//
//   - concat : A puis B, comme si le modèle avait écouté les deux extraits à la
//     suite. Les clés de B sont tournées du décalage, ce qui les place exactement
//     à leur nouvelle position. Seule approximation : les couches profondes de B
//     n'ont jamais vu A. Le dosage se règle par le nombre de trames gardées.
//   - interp : alpha·A + (1-alpha)·B, position par position, sur la longueur
//     commune. Les positions concordent, pas le contenu : c'est un essai.
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/ThiraSoft/golem/nn"
)

const (
	couches   = 24
	tetes     = 16
	dimTete   = 64
	maxPeriod = 10000
	// LoadVoice alloue 1024 positions, voix comprise, et une génération en
	// ajoute jusqu'à ~300 : au-delà de 700, la voix ne laisse plus la place.
	maxPositions = 700
)

// couche est l'état d'une couche : K puis V, chacun positions×têtes×dim.
type couche struct {
	k, v [][]float32 // une ligne par position, têtes×dim valeurs
}

type voix []couche

func main() {
	a := flag.String("a", "", "première voix (.safetensors)")
	b := flag.String("b", "", "seconde voix (.safetensors)")
	sortie := flag.String("o", "", "voix produite (.safetensors)")
	mode := flag.String("mode", "concat", "concat ou interp")
	na := flag.Int("na", 0, "concat : trames gardées de A, depuis le début (0 = toutes)")
	nb := flag.Int("nb", 0, "concat : trames gardées de B, depuis le début (0 = toutes)")
	alpha := flag.Float64("alpha", 0.5, "interp : part de A")
	flag.Parse()
	if *a == "" || *b == "" || *sortie == "" {
		flag.Usage()
		os.Exit(2)
	}

	va, err := lire(*a)
	quitter(err)
	vb, err := lire(*b)
	quitter(err)

	var res voix
	switch *mode {
	case "concat":
		res, err = concat(va, vb, *na, *nb)
	case "interp":
		res, err = interp(va, vb, float32(*alpha))
	default:
		err = fmt.Errorf("mode inconnu : %s", *mode)
	}
	quitter(err)
	quitter(ecrire(*sortie, res))
	n := len(res[0].k)
	fmt.Printf("%s : %d positions, %.1f s d'écoute\n", *sortie, n, float64(n-1)*0.08)
}

func quitter(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fusion:", err)
		os.Exit(1)
	}
}

// garder réduit la voix au début d'écoute et à ses n premières trames.
func garder(v voix, n int) voix {
	total := len(v[0].k) - 1
	if n <= 0 || n > total {
		n = total
	}
	out := make(voix, len(v))
	for i, c := range v {
		out[i] = couche{k: c.k[:n+1], v: c.v[:n+1]}
	}
	return out
}

func concat(a, b voix, na, nb int) (voix, error) {
	a, b = garder(a, na), garder(b, nb)
	// Le début d'écoute de B est retiré : le modèle écoute en continu, il n'y a
	// qu'un début. La trame p de B (p >= 1) prend la position len(A)+p-1.
	decalage := len(a[0].k) - 1
	total := len(a[0].k) + len(b[0].k) - 1
	if total > maxPositions {
		return nil, fmt.Errorf("%d positions : au-delà de %d, la génération manque de place ; réduire -na ou -nb",
			total, maxPositions)
	}
	out := make(voix, couches)
	for i := range out {
		k := append([][]float32{}, a[i].k...)
		v := append([][]float32{}, a[i].v...)
		for p := 1; p < len(b[i].k); p++ {
			k = append(k, tourner(b[i].k[p], decalage))
			v = append(v, b[i].v[p])
		}
		out[i] = couche{k: k, v: v}
	}
	return out, nil
}

// tourner décale une clé de delta positions. RoPE applique une rotation par
// paire de dimensions, d'angle proportionnel à la position : deux rotations
// s'ajoutent, tourner de delta une clé prise à p la met exactement à p+delta.
func tourner(k []float32, delta int) []float32 {
	out := append([]float32(nil), k...)
	for t := 0; t < tetes; t++ {
		nn.ApplyRoPE(out[t*dimTete:(t+1)*dimTete], delta, maxPeriod)
	}
	return out
}

func interp(a, b voix, alpha float32) (voix, error) {
	n := min(len(a[0].k), len(b[0].k))
	out := make(voix, couches)
	mix := func(x, y []float32) []float32 {
		r := make([]float32, len(x))
		for j := range x {
			r[j] = alpha*x[j] + (1-alpha)*y[j]
		}
		return r
	}
	for i := range out {
		c := couche{k: make([][]float32, n), v: make([][]float32, n)}
		for p := 0; p < n; p++ {
			c.k[p] = mix(a[i].k[p], b[i].k[p])
			c.v[p] = mix(a[i].v[p], b[i].v[p])
		}
		out[i] = c
	}
	return out, nil
}

// entete décrit un tenseur dans l'en-tête safetensors.
type entete struct {
	Dtype   string   `json:"dtype"`
	Shape   []int    `json:"shape"`
	Offsets [2]int64 `json:"data_offsets"`
}

func lire(chemin string) (voix, error) {
	raw, err := os.ReadFile(chemin)
	if err != nil {
		return nil, err
	}
	if len(raw) < 8 {
		return nil, fmt.Errorf("%s : fichier tronqué", chemin)
	}
	n := binary.LittleEndian.Uint64(raw[:8])
	var h map[string]json.RawMessage
	if err := json.Unmarshal(raw[8:8+n], &h); err != nil {
		return nil, fmt.Errorf("%s : %w", chemin, err)
	}
	donnees := raw[8+n:]
	tenseur := func(nom string) (entete, []byte, error) {
		var e entete
		m, ok := h[nom]
		if !ok {
			return e, nil, fmt.Errorf("%s : pas de %s", chemin, nom)
		}
		if err := json.Unmarshal(m, &e); err != nil {
			return e, nil, err
		}
		return e, donnees[e.Offsets[0]:e.Offsets[1]], nil
	}

	v := make(voix, couches)
	for i := range v {
		prefixe := fmt.Sprintf("transformer.layers.%d.self_attn/", i)
		_, off, err := tenseur(prefixe + "offset")
		if err != nil {
			return nil, err
		}
		position := int(binary.LittleEndian.Uint64(off))
		e, cache, err := tenseur(prefixe + "cache")
		if err != nil {
			return nil, err
		}
		if e.Dtype != "F32" || len(e.Shape) != 5 || e.Shape[0] != 2 || e.Shape[3] != tetes || e.Shape[4] != dimTete {
			return nil, fmt.Errorf("%s : cache %s %v inattendu", chemin, e.Dtype, e.Shape)
		}
		stockees := e.Shape[2]
		if position > stockees {
			return nil, fmt.Errorf("%s : offset %d au-delà des %d positions", chemin, position, stockees)
		}
		ligne := tetes * dimTete
		lignes := func(bloc int) [][]float32 {
			out := make([][]float32, position)
			for p := range out {
				base := (bloc*stockees + p) * ligne * 4
				r := make([]float32, ligne)
				for j := range r {
					r[j] = math.Float32frombits(binary.LittleEndian.Uint32(cache[base+4*j:]))
				}
				out[p] = r
			}
			return out
		}
		v[i] = couche{k: lignes(0), v: lignes(1)}
	}
	return v, nil
}

func ecrire(chemin string, v voix) error {
	n := len(v[0].k)
	ligne := tetes * dimTete
	tailleCache := int64(2 * n * ligne * 4)
	h := map[string]entete{}
	var pos int64
	for i := range v {
		prefixe := fmt.Sprintf("transformer.layers.%d.self_attn/", i)
		h[prefixe+"cache"] = entete{"F32", []int{2, 1, n, tetes, dimTete}, [2]int64{pos, pos + tailleCache}}
		pos += tailleCache
		h[prefixe+"offset"] = entete{"I64", []int{1}, [2]int64{pos, pos + 8}}
		pos += 8
	}
	js, err := json.Marshal(h)
	if err != nil {
		return err
	}
	// L'en-tête est complété d'espaces jusqu'à un multiple de 8, comme le fait
	// la bibliothèque de référence.
	for len(js)%8 != 0 {
		js = append(js, ' ')
	}
	buf := make([]byte, 0, 8+len(js)+int(pos))
	buf = binary.LittleEndian.AppendUint64(buf, uint64(len(js)))
	buf = append(buf, js...)

	// Par couche : le cache (K puis V), puis l'offset, dans l'ordre des offsets.
	for _, c := range v {
		for _, bloc := range [][][]float32{c.k, c.v} {
			for _, r := range bloc {
				for _, x := range r {
					buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(x))
				}
			}
		}
		buf = binary.LittleEndian.AppendUint64(buf, uint64(n))
	}
	return os.WriteFile(chemin, buf, 0o644)
}
