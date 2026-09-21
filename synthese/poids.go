package synthese

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThiraSoft/golem/pockettts"
)

// depot est le dépôt public de Kyutai : les poids du catalogue, sans le
// clonage de voix. Celui qui porte le clonage est sous conditions, cf.
// install.sh.
const depot = "kyutai/pocket-tts-without-voice-cloning"

// hfBase est l'adresse de Hugging Face, en variable de paquet pour que les
// tests la détournent vers un serveur local.
var hfBase = "https://huggingface.co"

// fichierDistant est une entrée de l'arbre du dépôt, telle que rendue par
// l'API Hugging Face.
type fichierDistant struct {
	Chemin string `json:"path"`
	Taille int64  `json:"size"`
	Type   string `json:"type"`
}

// Progression décrit l'avancement du téléchargement d'un fichier du lot.
type Progression struct {
	Fichier string // chemin du fichier dans le dépôt
	Recu    int64  // octets reçus pour ce fichier
	Total   int64  // taille annoncée, 0 si inconnue
	Index   int    // rang du fichier dans le lot, à partir de 1
	Nombre  int    // nombre de fichiers du lot
}

// cache rend le répertoire racine du cache Hugging Face pour le dépôt de
// gladyss, sans révision.
func cache() string {
	return filepath.Join(os.Getenv("HOME"), ".cache/huggingface/hub/models--kyutai--pocket-tts-without-voice-cloning")
}

// Poids dit si les poids et le tokenizer de la langue par défaut sont dans le
// cache Hugging Face.
func Poids() bool {
	lang, err := pockettts.LookupLanguage(pockettts.DefaultLanguage)
	if err != nil {
		return false
	}
	return pockettts.Locate(lang.WeightsPath()) != "" && pockettts.Locate(lang.TokenizerPath()) != ""
}

// lister rend le contenu d'un répertoire du dépôt, tel que rendu par l'API
// Hugging Face.
func lister(ctx context.Context, repertoire string) ([]fichierDistant, error) {
	url := fmt.Sprintf("%s/api/models/%s/tree/main/%s", hfBase, depot, repertoire)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lister %s: %s", repertoire, resp.Status)
	}
	var arbre []fichierDistant
	if err := json.NewDecoder(resp.Body).Decode(&arbre); err != nil {
		return nil, err
	}
	return arbre, nil
}

// revision rend le sha de la révision courante du dépôt.
func revision(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/api/models/%s", hfBase, depot)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("revision: %s", resp.Status)
	}
	var info struct {
		Sha string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if info.Sha == "" {
		return "", fmt.Errorf("revision: sha absent de la réponse")
	}
	return info.Sha, nil
}

// deja_la dit si un fichier du dépôt est déjà présent dans le cache, non
// vide.
func dejaLa(cible string) bool {
	fi, err := os.Stat(cible)
	return err == nil && fi.Size() > 0
}

// recuperer télécharge un fichier du dépôt vers cible, en passant par un
// fichier temporaire : une coupure laisse un .part, jamais un fichier tronqué
// que le prochain passage prendrait pour complet.
func recuperer(ctx context.Context, sha, chemin, cible string, taille int64, avancement func(recu int64)) error {
	if err := os.MkdirAll(filepath.Dir(cible), 0o755); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s/resolve/%s/%s", hfBase, depot, sha, chemin)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("téléchargement de %s: %s", chemin, resp.Status)
	}

	partiel := cible + ".part"
	f, err := os.Create(partiel)
	if err != nil {
		return err
	}

	attendu := taille
	if resp.ContentLength > 0 {
		attendu = resp.ContentLength
	}

	recu, copieErr := copierAvecProgression(ctx, f, resp.Body, avancement)
	fermeErr := f.Close()
	if copieErr == nil {
		copieErr = fermeErr
	}
	if copieErr != nil {
		os.Remove(partiel)
		return fmt.Errorf("téléchargement de %s: %w", chemin, copieErr)
	}
	if attendu > 0 && recu != attendu {
		os.Remove(partiel)
		return fmt.Errorf("téléchargement de %s incomplet : %d octets reçus sur %d attendus", chemin, recu, attendu)
	}
	return os.Rename(partiel, cible)
}

// copierAvecProgression copie src vers dst en appelant avancement à chaque
// tranche lue, et s'interrompt si ctx est annulé.
func copierAvecProgression(ctx context.Context, dst io.Writer, src io.Reader, avancement func(recu int64)) (int64, error) {
	var total int64
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
			if avancement != nil {
				avancement(total)
			}
		}
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
	}
}

// Assurer garantit que les poids, le tokenizer et les voix du catalogue sont
// dans le cache, en téléchargeant ce qui manque depuis le dépôt public de
// Kyutai. progression est appelée au fil du transfert ; elle peut être nil.
// Un cache déjà complet ne déclenche aucun appel réseau.
func Assurer(ctx context.Context, progression func(Progression)) error {
	lang, err := pockettts.LookupLanguage(pockettts.DefaultLanguage)
	if err != nil {
		return err
	}

	// Ce que le dépôt publie : le modèle et son tokenizer d'un côté, les voix
	// du catalogue de l'autre.
	racine := "languages/" + lang.Name
	principal, errPrincipal := lister(ctx, racine)
	voix, errVoix := lister(ctx, racine+"/embeddings")

	if errPrincipal != nil && errVoix != nil {
		// Hors ligne : un cache déjà pourvu reste utilisable, sans faire
		// échouer l'appelant.
		if Poids() {
			return nil
		}
		return fmt.Errorf("hugging face injoignable : impossible de lire le contenu de %s: %w", depot, errPrincipal)
	}

	var publies []fichierDistant
	for _, f := range principal {
		if f.Type == "file" && (strings.HasSuffix(f.Chemin, ".safetensors") || strings.HasSuffix(f.Chemin, ".model")) {
			publies = append(publies, f)
		}
	}
	publies = append(publies, voix...)

	racineCache := cache()

	// Fichiers manquants du cache, toutes révisions confondues (comme
	// pockettts.Locate cherche).
	var manquants []fichierDistant
	for _, f := range publies {
		if pockettts.Locate(f.Chemin) == "" {
			manquants = append(manquants, f)
		}
	}

	if len(manquants) == 0 {
		return nil
	}

	sha, err := revision(ctx)
	if err != nil {
		return fmt.Errorf("impossible de lire la révision de %s: %w", depot, err)
	}

	snapshotDir := filepath.Join(racineCache, "snapshots", sha)

	for i, f := range manquants {
		cible := filepath.Join(snapshotDir, filepath.FromSlash(f.Chemin))
		if dejaLa(cible) {
			continue
		}
		index := i + 1
		nombre := len(manquants)
		err := recuperer(ctx, sha, f.Chemin, cible, f.Taille, func(recu int64) {
			if progression != nil {
				progression(Progression{
					Fichier: f.Chemin,
					Recu:    recu,
					Total:   f.Taille,
					Index:   index,
					Nombre:  nombre,
				})
			}
		})
		if err != nil {
			return err
		}
	}

	// refs/main est ce que le cache Hugging Face garde pour savoir quelle
	// révision il tient : l'écrire évite qu'un `hf download` ultérieur
	// reparte de zéro.
	refs := filepath.Join(racineCache, "refs")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(refs, "main"), []byte(sha), 0o644); err != nil {
		return err
	}

	return nil
}
