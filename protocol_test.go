package main

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestLireMessageSepareLEnteteDeLaChargeBinaire(t *testing.T) {
	// Deux messages audio collés : le second ne doit pas être avalé par le premier.
	var flux bytes.Buffer
	flux.WriteString(`{"type":"audio","bytes":4}` + "\n")
	flux.Write([]byte{0x01, 0x02, 0x03, 0x04})
	flux.WriteString(`{"type":"audio","bytes":2}` + "\n")
	flux.Write([]byte{0x05, 0x06})
	flux.WriteString(`{"type":"end","cancelled":true}` + "\n")

	r := bufio.NewReader(&flux)

	msg, charge, err := readMessage(r)
	if err != nil {
		t.Fatalf("readMessage() erreur = %v", err)
	}
	if msg.Type != "audio" {
		t.Errorf("type = %q, want \"audio\"", msg.Type)
	}
	if !bytes.Equal(charge, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("charge = %v, want [1 2 3 4]", charge)
	}

	_, charge, err = readMessage(r)
	if err != nil {
		t.Fatalf("second message : erreur = %v", err)
	}
	if !bytes.Equal(charge, []byte{0x05, 0x06}) {
		t.Errorf("charge = %v, want [5 6] — le flux est désynchronisé", charge)
	}

	msg, _, err = readMessage(r)
	if err != nil {
		t.Fatalf("troisième message : erreur = %v", err)
	}
	if msg.Type != "end" || !msg.Cancelled {
		t.Errorf("msg = %+v, want end/cancelled=true", msg)
	}
}

func TestLireMessageSignaleUneChargeTronquee(t *testing.T) {
	flux := strings.NewReader(`{"type":"audio","bytes":8}` + "\n" + "abc")

	if _, _, err := readMessage(bufio.NewReader(flux)); err == nil {
		t.Error("readMessage() = nil, want une erreur sur charge tronquée")
	}
}

func TestPomperAudioRendLErreurDuDaemon(t *testing.T) {
	// Le daemon refuse la voix, puis clôt l'énoncé comme le veut le protocole.
	// L'appelant doit récupérer le message : c'est lui qui contient la marche à
	// suivre, et le client HTTP n'a rien d'autre à se mettre sous la dent.
	flux := strings.NewReader(
		`{"type":"error","message":"voix indisponible : clonage indisponible"}` + "\n" +
			`{"type":"end","cancelled":false}` + "\n")

	p := &PocketTTS{stdout: bufio.NewReader(flux)}

	err := p.pumpAudio(io.Discard)
	if err == nil {
		t.Fatal("pumpAudio() = nil, want l'erreur remontée du daemon")
	}
	if !strings.Contains(err.Error(), "clonage indisponible") {
		t.Errorf("err = %q, want le message du daemon", err)
	}
}

func TestPomperAudioDrainLeTubeApresUneErreur(t *testing.T) {
	// Rendre la main dès le message d'erreur laisserait l'audio déjà émis et le
	// « end » dans le tube : tous les énoncés suivants seraient décalés.
	var flux bytes.Buffer
	flux.WriteString(`{"type":"error","message":"échec de synthèse"}` + "\n")
	flux.WriteString(`{"type":"audio","bytes":2}` + "\n")
	flux.Write([]byte{0x07, 0x08})
	flux.WriteString(`{"type":"end","cancelled":false}` + "\n")
	flux.WriteString(`{"type":"start"}` + "\n") // début de l'énoncé suivant

	r := bufio.NewReader(&flux)
	p := &PocketTTS{stdout: r}

	var recu bytes.Buffer
	if err := p.pumpAudio(&recu); err == nil {
		t.Fatal("pumpAudio() = nil, want une erreur")
	}
	if !bytes.Equal(recu.Bytes(), []byte{0x07, 0x08}) {
		t.Errorf("audio pompé = %v, want [7 8]", recu.Bytes())
	}

	msg, _, err := readMessage(r)
	if err != nil {
		t.Fatalf("message suivant : %v", err)
	}
	if msg.Type != "start" {
		t.Errorf("type = %q, want \"start\" — le tube est désynchronisé", msg.Type)
	}
}

func TestPomperAudioNeFaitPasDUneAnnulationUneErreur(t *testing.T) {
	// Un skip ou un stop passe par le contexte, pas par une erreur : la remonter
	// ferait répondre 500 à une interruption demandée par l'utilisateur.
	flux := strings.NewReader(
		`{"type":"error","message":"génération interrompue"}` + "\n" +
			`{"type":"end","cancelled":true}` + "\n")

	p := &PocketTTS{stdout: bufio.NewReader(flux)}

	if err := p.pumpAudio(io.Discard); err != nil {
		t.Errorf("pumpAudio() = %v, want nil sur un end annulé", err)
	}
}
