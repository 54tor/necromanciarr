package hashsource

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
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

// byVolume route chaque chemin vers la source de son volume.
type byVolume struct {
	local, remote HashSource
}

// ByVolume choisit la source par volume : un chemin sftp:// va à remote (SFTPExec, qui ne répond que pour un volume
// dont la sonde annonce RemoteHash), tout autre chemin à local (montages rclone). Une source nil répond toujours
// Available=false, sans erreur : le fichier sera relu.
func ByVolume(local, remote HashSource) HashSource {
	return byVolume{local: local, remote: remote}
}

func (sources byVolume) Hash(ctx context.Context, path string, algorithm Algorithm) (Result, error) {
	source := sources.local
	if strings.HasPrefix(path, "sftp://") {
		source = sources.remote
	}
	if source == nil {
		return Result{}, nil
	}
	return source.Hash(ctx, path, algorithm)
}
