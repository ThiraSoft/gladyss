package main

import "encoding/binary"

// wavHeaderSize est la taille de l'en-tête RIFF/WAVE canonique pour du PCM.
const wavHeaderSize = 44

// wrapWav préfixe du PCM signé 16 bits little-endian mono d'un en-tête WAV
// complet, tailles réelles renseignées.
//
// On l'écrit à la main plutôt que de demander du WAV à ffmpeg : sur un tube,
// ffmpeg ne peut pas revenir écrire les tailles une fois l'audio produit et
// laisse des champs à 0xFFFFFFFF, que les décodeurs stricts refusent — dont
// decodeAudioData des navigateurs, qui est justement au bout de la chaîne.
func wrapWav(pcm []byte, sampleRate int) []byte {
	const channels, bits = 1, 16
	const alignment = channels * bits / 8

	out := make([]byte, wavHeaderSize, wavHeaderSize+len(pcm))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm))) // taille du fichier - 8
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16) // taille du bloc fmt
	binary.LittleEndian.PutUint16(out[20:], 1)  // 1 = PCM entier non compressé
	binary.LittleEndian.PutUint16(out[22:], channels)
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:], uint32(sampleRate*alignment)) // octets par seconde
	binary.LittleEndian.PutUint16(out[32:], alignment)
	binary.LittleEndian.PutUint16(out[34:], bits)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	return append(out, pcm...)
}

// unknownSize est la valeur conventionnelle des champs de taille d'un WAV
// dont on ne connaît pas encore la longueur — celle qu'écrit ffmpeg sur un tube.
const unknownSize = 0xFFFFFFFF

// streamingWavHeader produit l'en-tête d'un WAV dont la longueur est inconnue,
// pour un envoi au fil de l'eau. Les tailles sont fausses par construction : les
// décodeurs tolérants lisent jusqu'à la fin du flux, les stricts refusent. Un
// client qui veut du streaming propre demande plutôt "pcm" et connaît déjà le
// taux par l'en-tête X-Sample-Rate.
func streamingWavHeader(sampleRate int) []byte {
	header := wrapWav(nil, sampleRate)
	binary.LittleEndian.PutUint32(header[4:], unknownSize)
	binary.LittleEndian.PutUint32(header[40:], unknownSize)
	return header
}
