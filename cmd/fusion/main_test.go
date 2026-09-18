package main

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/ThiraSoft/golem/nn"
)

// Une clé tournée à p puis décalée de delta doit valoir la clé tournée à p+delta.
func TestTournerDecaleLaPosition(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	brute := make([]float32, tetes*dimTete)
	for i := range brute {
		brute[i] = r.Float32()*2 - 1
	}
	at := func(p int) []float32 {
		k := append([]float32(nil), brute...)
		for h := 0; h < tetes; h++ {
			nn.ApplyRoPE(k[h*dimTete:(h+1)*dimTete], p, maxPeriod)
		}
		return k
	}
	got, want := tourner(at(37), 312), at(349)
	for i := range got {
		if math.Abs(float64(got[i]-want[i])) > 1e-4 {
			t.Fatalf("composante %d : %v, attendu %v", i, got[i], want[i])
		}
	}
}

func voixFactice(n int, graine float32) voix {
	v := make(voix, couches)
	for i := range v {
		c := couche{k: make([][]float32, n), v: make([][]float32, n)}
		for p := 0; p < n; p++ {
			c.k[p] = make([]float32, tetes*dimTete)
			c.v[p] = make([]float32, tetes*dimTete)
			for j := range c.k[p] {
				c.k[p][j] = graine + float32(i*1000+p) + float32(j)/1e4
				c.v[p][j] = -c.k[p][j]
			}
		}
		v[i] = c
	}
	return v
}

func TestEcrireLireAllerRetour(t *testing.T) {
	v := voixFactice(5, 0.5)
	chemin := filepath.Join(t.TempDir(), "v.safetensors")
	if err := ecrire(chemin, v); err != nil {
		t.Fatal(err)
	}
	lu, err := lire(chemin)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v {
		for p := range v[i].k {
			for j := range v[i].k[p] {
				if lu[i].k[p][j] != v[i].k[p][j] || lu[i].v[p][j] != v[i].v[p][j] {
					t.Fatalf("couche %d position %d diffère", i, p)
				}
			}
		}
	}
}

func TestConcatRetireLeDebutDeB(t *testing.T) {
	a, b := voixFactice(11, 0), voixFactice(21, 7)
	res, err := concat(a, b, 4, 6)
	if err != nil {
		t.Fatal(err)
	}
	// début + 4 trames de A, puis 6 trames de B sans son début
	if n := len(res[0].k); n != 11 {
		t.Fatalf("%d positions, attendu 11", n)
	}
	if res[3].v[5][0] != b[3].v[1][0] {
		t.Fatal("la première trame de B n'est pas à la suite de A")
	}
	if _, err := concat(voixFactice(400, 0), voixFactice(400, 0), 0, 0); err == nil {
		t.Fatal("799 positions acceptées")
	}
}
