package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// FSProviderTypeSFTP est le type des fournisseurs joints par SFTP, nommés dans les chemins sftp://<nom>/….
	FSProviderTypeSFTP = "sftp"
	// SFTPExecAuto, SFTPExecOff et SFTPExecRequired sont les valeurs de exec (exécution distante en lecture seule).
	SFTPExecAuto     = "auto"
	SFTPExecOff      = "off"
	SFTPExecRequired = "required"

	defaultSFTPPort                  = "22"
	defaultSFTPMaxConcurrentRequests = 16
	// maxSFTPConcurrentRequests borne la mémoire des lectures en vol (256 × 32 Kio = 8 Mio par fichier).
	maxSFTPConcurrentRequests  = 256
	defaultSFTPTimeout         = 30 * time.Second
	defaultSFTPKeepalive       = 15 * time.Second
	defaultSFTPExecHashTimeout = 30 * time.Minute
)

var sftpExecModes = []string{SFTPExecAuto, SFTPExecOff, SFTPExecRequired}

// pureVariableReference reconnaît une valeur faite d'une seule référence ${NOM}, sans texte autour.
var pureVariableReference = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// sftpProviderFile est la forme brute d'une entrée de type sftp. Mounts n'existe que pour refuser la clé avec un
// message utile ; les durées sont des textes Go (« 30s ») ; les pointeurs distinguent une clé absente de 0.
type sftpProviderFile struct {
	Type                  string          `json:"type"`
	Name                  string          `json:"name"`
	Address               string          `json:"address"`
	User                  string          `json:"user"`
	IdentityFile          string          `json:"identityFile"`
	KnownHosts            string          `json:"knownHosts"`
	Agent                 bool            `json:"agent"`
	Password              string          `json:"password"`
	IdentityPassphrase    string          `json:"identityPassphrase"`
	ReadBytesPerSecond    int64           `json:"readBytesPerSecond"`
	MaxConcurrentRequests *int            `json:"maxConcurrentRequests"`
	Timeout               string          `json:"timeout"`
	Keepalive             string          `json:"keepalive"`
	Exec                  string          `json:"exec"`
	ExecHashTimeout       string          `json:"execHashTimeout"`
	Mounts                json.RawMessage `json:"mounts"`
}

// parseSFTPProvider décode une entrée sftp (clés inconnues refusées), garde les secrets littéraux, interpole les
// références ${VAR}, puis applique défauts et règles. Les erreurs enveloppent ErrUsage et ne citent jamais un secret.
func parseSFTPProvider(rawProvider json.RawMessage, lookup func(string) (string, bool)) (FSProvider, error) {
	provider, err := decodeSFTPProvider(rawProvider, lookup)
	if err != nil {
		return FSProvider{}, fmt.Errorf("%w: type sftp: %w", ErrUsage, err)
	}
	return provider, nil
}

func decodeSFTPProvider(rawProvider json.RawMessage, lookup func(string) (string, bool)) (FSProvider, error) {
	var rawFile sftpProviderFile
	decoder := json.NewDecoder(bytes.NewReader(rawProvider))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return FSProvider{}, err
	}
	if rawFile.Mounts != nil {
		return FSProvider{}, errors.New("mounts does not apply: name sftp://<name>/<absolute path> locations instead")
	}
	if err := keepLiteralSecrets(&rawFile); err != nil {
		return FSProvider{}, err
	}
	if err := interpolateValue(reflect.ValueOf(&rawFile).Elem(), "", lookup); err != nil {
		return FSProvider{}, err
	}
	return buildSFTPProvider(rawFile)
}

// keepLiteralSecrets garde tel quel un secret littéral, venu scellé de la base, et laisse interpoler une seule
// référence ${VAR} ; un mélange de texte et de références est refusé.
func keepLiteralSecrets(rawFile *sftpProviderFile) error {
	secrets := []struct {
		key   string
		value *string
	}{{"password", &rawFile.Password}, {"identityPassphrase", &rawFile.IdentityPassphrase}}
	for _, secret := range secrets {
		isReference := pureVariableReference.MatchString(*secret.value)
		if isReference {
			continue
		}
		if strings.Contains(*secret.value, "${") {
			return fmt.Errorf("%s must be a literal value or a single ${VARIABLE} reference", secret.key)
		}
		*secret.value = strings.ReplaceAll(*secret.value, "$", "$$")
	}
	return nil
}

func buildSFTPProvider(rawFile sftpProviderFile) (FSProvider, error) {
	if !fsProviderNamePattern.MatchString(rawFile.Name) {
		return FSProvider{}, fmt.Errorf("name %q is required and must match %s", rawFile.Name, fsProviderNamePattern)
	}
	address, err := normalizeSFTPAddress(rawFile.Address)
	if err != nil {
		return FSProvider{}, err
	}
	provider := FSProvider{
		Type: rawFile.Type, Name: rawFile.Name, Address: address, User: rawFile.User, IdentityFile: rawFile.IdentityFile,
		KnownHosts: rawFile.KnownHosts, Agent: rawFile.Agent, Password: rawFile.Password,
		IdentityPassphrase: rawFile.IdentityPassphrase, ReadBytesPerSecond: rawFile.ReadBytesPerSecond,
		MaxConcurrentRequests: defaultSFTPMaxConcurrentRequests, Exec: SFTPExecAuto,
	}
	if err := checkSFTPIdentity(provider); err != nil {
		return FSProvider{}, err
	}
	if err := applySFTPLimits(&provider, rawFile); err != nil {
		return FSProvider{}, err
	}
	return provider, nil
}

// normalizeSFTPAddress accepte « hôte », « hôte:port », « [ipv6] » et « [ipv6]:port » et rend toujours hôte:port.
// Un caractère de contrôle n'est jamais recopié dans l'erreur.
func normalizeSFTPAddress(rawAddress string) (string, error) {
	if strings.Contains(rawAddress, "://") {
		return "", errors.New("address is a host or host:port, not a URL")
	}
	if strings.IndexFunc(rawAddress, unicode.IsControl) >= 0 {
		return "", errors.New("address must not contain control characters")
	}
	host, port, err := net.SplitHostPort(rawAddress)
	if err != nil {
		host, port = rawAddress, defaultSFTPPort
		isBracketed := strings.HasPrefix(rawAddress, "[") && strings.HasSuffix(rawAddress, "]")
		if isBracketed {
			host = rawAddress[1 : len(rawAddress)-1]
		}
		if strings.Contains(host, ":") && !isBracketed {
			return "", fmt.Errorf("address %q: write an IPv6 host between brackets ([::1] or [::1]:22)", rawAddress)
		}
	}
	portNumber, portErr := strconv.Atoi(port)
	isValidPort := portErr == nil && portNumber > 0 && portNumber <= 65535
	// , * ? ! sont des motifs de known_hosts : sftp trust écrirait sinon une épingle générique.
	if host == "" || strings.ContainsAny(host, "/@[] \t,*?!") || !isValidPort {
		return "", fmt.Errorf("address %q is required as host or host:port (port 1-65535)", rawAddress)
	}
	return net.JoinHostPort(host, port), nil
}

// checkSFTPIdentity : utilisateur, fichier known_hosts et au moins une méthode d'authentification. L'agent n'est
// cherché (SSH_AUTH_SOCK) qu'à la connexion ; l'existence des fichiers relève de doctor.
func checkSFTPIdentity(provider FSProvider) error {
	if provider.User == "" {
		return errors.New("user is required")
	}
	if provider.KnownHosts == "" {
		return errors.New("knownHosts is required: the host key is always checked")
	}
	if err := checkAbsoluteFilePath("knownHosts", provider.KnownHosts); err != nil {
		return err
	}
	if provider.IdentityFile != "" {
		if err := checkAbsoluteFilePath("identityFile", provider.IdentityFile); err != nil {
			return err
		}
	}
	if provider.IdentityPassphrase != "" && provider.IdentityFile == "" {
		return errors.New("identityPassphrase needs identityFile")
	}
	if provider.IdentityFile == "" && !provider.Agent && provider.Password == "" {
		return errors.New("one authentication method is required: identityFile, agent or password")
	}
	return nil
}

func checkAbsoluteFilePath(key, path string) error {
	hasParentSegment := slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "..")
	if !filepath.IsAbs(path) || hasParentSegment || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%s must be an absolute local path without \"..\"", key)
	}
	return nil
}

// applySFTPLimits lit débit, requêtes en vol, délais et mode exec ; 0 ou absent pour readBytesPerSecond garde le
// débit global.
func applySFTPLimits(provider *FSProvider, rawFile sftpProviderFile) error {
	if rawFile.ReadBytesPerSecond < 0 {
		return errors.New("readBytesPerSecond must not be negative (0 or absent keeps the global limit)")
	}
	if rawFile.MaxConcurrentRequests != nil {
		requested := *rawFile.MaxConcurrentRequests
		if requested <= 0 || requested > maxSFTPConcurrentRequests {
			return fmt.Errorf("maxConcurrentRequests must be between 1 and %d", maxSFTPConcurrentRequests)
		}
		provider.MaxConcurrentRequests = *rawFile.MaxConcurrentRequests
	}
	if rawFile.Exec != "" {
		if !slices.Contains(sftpExecModes, rawFile.Exec) {
			return fmt.Errorf("exec: unknown mode %q (allowed: %s)", rawFile.Exec, strings.Join(sftpExecModes, ", "))
		}
		provider.Exec = rawFile.Exec
	}
	durations := []struct {
		key, raw string
		target   *time.Duration
		fallback time.Duration
	}{
		{"timeout", rawFile.Timeout, &provider.Timeout, defaultSFTPTimeout},
		{"keepalive", rawFile.Keepalive, &provider.Keepalive, defaultSFTPKeepalive},
		{"execHashTimeout", rawFile.ExecHashTimeout, &provider.ExecHashTimeout, defaultSFTPExecHashTimeout},
	}
	for _, duration := range durations {
		parsed, err := parsePositiveDuration(duration.key, duration.raw, duration.fallback)
		if err != nil {
			return err
		}
		*duration.target = parsed
	}
	return nil
}

func parsePositiveDuration(key, raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive Go duration", key, raw)
	}
	return parsed, nil
}

// sftpSecrets rend le mot de passe et la passphrase pour KnownSecrets ; une valeur courte n'est masquée que dans
// les URL, comme une valeur de trackers, pour ne pas couper des mots ordinaires.
func sftpSecrets(provider FSProvider) []string {
	var secrets []string
	for _, secret := range []string{provider.Password, provider.IdentityPassphrase} {
		switch {
		case secret == "":
		case len(secret) < MinKnownSecretLength:
			secrets = append(secrets, urlOnlySecret(secret))
		default:
			secrets = append(secrets, secret)
		}
	}
	return secrets
}

// SFTPAddressKey rend la clé de comparaison d'une adresse sftp : hôte en minuscules sans point final, port explicite
// (22 par défaut), adresse IP sous sa forme canonique. Deux fournisseurs de même clé joignent le même serveur et
// partagent leurs racines de bibliothèque ; un nom DNS et son adresse IP ne sont pas rapprochés.
func SFTPAddressKey(rawAddress string) (string, error) {
	address, err := normalizeSFTPAddress(rawAddress)
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return "", fmt.Errorf("address %q has no host", rawAddress)
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, port), nil
}

// IsLoopbackSFTPAddress dit si l'adresse vise la machine elle-même (localhost, 127.0.0.0/8, ::1) : un tel volume et
// un chemin local peuvent désigner le même disque sans que le registre le voie. Adresse invalide : false.
func IsLoopbackSFTPAddress(rawAddress string) bool {
	key, err := SFTPAddressKey(rawAddress)
	if err != nil {
		return false
	}
	host, _, err := net.SplitHostPort(key)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
