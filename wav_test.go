package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEnveloppeWavDecritLesTaillesReelles(t *testing.T) {
	pcm := bytes.Repeat([]byte{0x11, 0x22}, 100) // 100 échantillons 16 bits
	sortie := wrapWav(pcm, 24000)

	if len(sortie) != wavHeaderSize+len(pcm) {
		t.Fatalf("taille = %d, want %d", len(sortie), wavHeaderSize+len(pcm))
	}
	if taille := binary.LittleEndian.Uint32(sortie[4:]); taille != uint32(36+len(pcm)) {
		t.Errorf("taille RIFF = %d, want %d — un décodeur strict refuserait", taille, 36+len(pcm))
	}
	if taille := binary.LittleEndian.Uint32(sortie[40:]); taille != uint32(len(pcm)) {
		t.Errorf("taille du bloc data = %d, want %d", taille, len(pcm))
	}
	if !bytes.Equal(sortie[wavHeaderSize:], pcm) {
		t.Error("les données audio ont été altérées")
	}
}

func TestEnveloppeWavDecritDuPcm16MonoAuBonTaux(t *testing.T) {
	sortie := wrapWav([]byte{0, 0, 0, 0}, 16000)

	if format := binary.LittleEndian.Uint16(sortie[20:]); format != 1 {
		t.Errorf("format = %d, want 1 (PCM entier)", format)
	}
	if canaux := binary.LittleEndian.Uint16(sortie[22:]); canaux != 1 {
		t.Errorf("canaux = %d, want 1", canaux)
	}
	if taux := binary.LittleEndian.Uint32(sortie[24:]); taux != 16000 {
		t.Errorf("taux = %d, want 16000", taux)
	}
	if debit := binary.LittleEndian.Uint32(sortie[28:]); debit != 32000 {
		t.Errorf("octets par seconde = %d, want 32000", debit)
	}
	if alignement := binary.LittleEndian.Uint16(sortie[32:]); alignement != 2 {
		t.Errorf("alignement = %d, want 2", alignement)
	}
	if bits := binary.LittleEndian.Uint16(sortie[34:]); bits != 16 {
		t.Errorf("bits = %d, want 16", bits)
	}
}
