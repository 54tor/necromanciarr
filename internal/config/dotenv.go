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

// loadDotEnvLookup fusionne les .env du dossier de configuration puis du dossier courant (le premier gagne)
// derrière l'environnement réel, qu'un .env ne surcharge jamais.
func loadDotEnvLookup(configDir string, lookupEnv func(string) (string, bool)) (func(string) (string, bool), error) {
	merged := map[string]string{}
	for _, directory := range []string{filepath.Join(configDir, dotEnvFileName), dotEnvFileName} {
		variables, err := readDotEnv(directory)
		if err != nil {
			return nil, err
		}
		for name, value := range variables {
			if _, alreadySet := merged[name]; !alreadySet {
				merged[name] = value
			}
		}
	}
	return func(name string) (string, bool) {
		if value, found := lookupEnv(name); found {
			return value, true
		}
		value, found := merged[name]
		return value, found
	}, nil
}
