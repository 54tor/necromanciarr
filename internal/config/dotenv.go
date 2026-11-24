package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const dotEnvFileName = ".env"

// readDotEnv lit un fichier KEY=VALUE (commentaires #, préfixe export, guillemets simples ou doubles) ;
// un fichier absent n'est pas une erreur. Une bibliothèque n'apporterait que le multiligne, inutile ici.
func readDotEnv(path string) (map[string]string, error) {
	file, err := os.Open(path) // #nosec G304 -- chemin dérivé du dossier de configuration de l'opérateur
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dotEnvFileName, err)
	}
	defer func() { _ = file.Close() }()

	variables := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, hasAssignment := strings.Cut(line, "=")
		if line == "" || strings.HasPrefix(line, "#") || !hasAssignment {
			continue
		}
		variables[strings.TrimSpace(name)] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", dotEnvFileName, err)
	}
	return variables, nil
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}

// loadDotEnvLookup lit le .env du dossier de configuration, ou à défaut celui du dossier courant (fromWorkingDir le
// signale : un .env laissé dans le dossier d'où l'on lance la commande serait sinon lu sans le savoir), derrière
// l'environnement réel, qu'un .env ne surcharge jamais.
func loadDotEnvLookup(configDir string, lookupEnv func(string) (string, bool)) (lookup func(string) (string, bool), fromWorkingDir bool, err error) {
	variables, err := readDotEnv(filepath.Join(configDir, dotEnvFileName))
	if err != nil {
		return nil, false, err
	}
	if variables == nil && !sameFile(filepath.Join(configDir, dotEnvFileName), dotEnvFileName) {
		if variables, err = readDotEnv(dotEnvFileName); err != nil {
			return nil, false, err
		}
		fromWorkingDir = variables != nil
	}
	return func(name string) (string, bool) {
		if value, found := lookupEnv(name); found {
			return value, true
		}
		value, found := variables[name]
		return value, found
	}, fromWorkingDir, nil
}

// sameFile dit si deux chemins désignent le même dossier de .env (dossier de configuration = dossier courant).
func sameFile(left, right string) bool {
	absoluteLeft, leftErr := filepath.Abs(left)
	absoluteRight, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && absoluteLeft == absoluteRight
}
