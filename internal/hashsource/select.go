package hashsource

import (
	"context"
	"net/http"
	"os/exec"
	"time"
)

// RcloneOptions règle NewRclone : RCURL (API --rc) prime sur Binary ; sans l'un ni l'autre, le binaire « rclone » du
// PATH est essayé.
type RcloneOptions struct {
	RCURL      string
	Binary     string
	Mounts     []Mount
	Timeout    time.Duration
	HTTPClient *http.Client
}

// NewRclone choisit l'adaptateur : API rc si une URL est configurée, sinon binaire (configuré ou trouvé dans le PATH),
// sinon une source qui répond toujours Available=false, sans erreur.
func NewRclone(options RcloneOptions) (HashSource, error) {
	if options.RCURL != "" {
		return NewRcloneRC(RcOptions{URL: options.RCURL, Mounts: options.Mounts, Timeout: options.Timeout, HTTPClient: options.HTTPClient})
	}
	binary := options.Binary
	if binary == "" {
		if found, err := exec.LookPath(defaultRcloneBinary); err == nil {
			binary = found
		}
	}
	if binary == "" {
		return unavailableSource{}, nil
	}
	return NewRcloneExec(ExecOptions{Binary: binary, Mounts: options.Mounts, Timeout: options.Timeout})
}

// unavailableSource répond qu'aucune empreinte n'est disponible : une absence normale, jamais un échec.
type unavailableSource struct{}

func (unavailableSource) Hash(context.Context, string, Algorithm) (Result, error) {
	return Result{}, nil
}
