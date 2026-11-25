package sftpfs

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// statFieldCount : %d, %i, %h, %s et %Y, séparés par une tabulation.
	statFieldCount = 5
	// maxDecimalDigits : un uint64 tient en 20 chiffres décimaux.
	maxDecimalDigits = 20
	// maxModTimeSeconds : 31 décembre 9999 ; au-delà, la date est absurde.
	maxModTimeSeconds = 253402300799
	// hashSeparator sépare l'empreinte du chemin dans une sortie de sha1sum -z et md5sum -z.
	hashSeparator = "  "
)

// AllowExecRoots ajoute des racines (dossiers de liens, racines torrent de ce volume) sous lesquelles Exec admet un
// chemin, en plus de Deps.LibraryRoots et des racines d'écriture ouvertes par ce volume. Chaque racine doit être un
// chemin POSIX absolu et canonique, autre que « / » (qui admettrait tout chemin) ; sinon rien n'est ajouté.
func (volume *Volume) AllowExecRoots(roots ...string) error {
	for _, root := range roots {
		if !isAdmissibleRoot(root) {
			return fmt.Errorf("sftpfs: exec root %q: %w: canonical absolute POSIX path other than / required", root, fs.ErrInvalid)
		}
	}
	for _, root := range roots {
		volume.allowExecRoot(root)
	}
	return nil
}

func (volume *Volume) allowExecRoot(root string) {
	volume.execRootsMutex.Lock()
	defer volume.execRootsMutex.Unlock()
	if isAdmissibleRoot(root) && !slices.Contains(volume.execRoots, root) {
		volume.execRoots = append(volume.execRoots, root)
	}
}

func (volume *Volume) execRootList() []string {
	volume.execRootsMutex.Lock()
	defer volume.execRootsMutex.Unlock()
	roots := slices.Clone(volume.execRoots)
	for _, libraryRoot := range volume.deps.LibraryRoots {
		if isAdmissibleRoot(libraryRoot) {
			roots = append(roots, libraryRoot)
		}
	}
	return roots
}

// admitPaths exige de chaque chemin la forme canonique et une racine du volume, segment par segment ; l'erreur cite
// le rang, jamais le chemin.
func (volume *Volume) admitPaths(paths []string) error {
	roots := volume.execRootList()
	for pathIndex, candidate := range paths {
		isUnderRoot := slices.ContainsFunc(roots, func(root string) bool { return isWithin(candidate, root) })
		if !isCanonicalRemotePath(candidate) || !isUnderRoot {
			return fmt.Errorf("%w: path %d", ErrExecPathOutsideRoots, pathIndex)
		}
	}
	return nil
}

// isAdmissibleRoot : une racine canonique autre que « / ».
func isAdmissibleRoot(root string) bool { return isCanonicalRemotePath(root) && root != "/" }

// isCanonicalRemotePath : absolu, égal à son path.Clean (donc sans « .. », « . » ni « // »), sans NUL, au plus
// maxNameBytes octets.
func isCanonicalRemotePath(candidate string) bool {
	return path.IsAbs(candidate) && path.Clean(candidate) == candidate && len(candidate) <= maxNameBytes &&
		!strings.ContainsRune(candidate, 0)
}

// parseRecords coupe la sortie en enregistrements terminés par NUL, exactement un par chemin dans l'ordre d'envoi ;
// tout écart rejette le lot entier.
func parseRecords(command CommandID, paths []string, stdout []byte) ([]ExecResult, error) {
	records, err := splitRecords(stdout, len(paths))
	if err != nil {
		return nil, err
	}
	results := make([]ExecResult, 0, len(paths))
	for pathIndex, remotePath := range paths {
		result, err := parseRecord(command, remotePath, records[pathIndex])
		if err != nil {
			return nil, fmt.Errorf("%w: record %d", err, pathIndex)
		}
		results = append(results, result)
	}
	return results, nil
}

func splitRecords(stdout []byte, expectedCount int) ([][]byte, error) {
	if len(stdout) == 0 || stdout[len(stdout)-1] != 0 {
		return nil, fmt.Errorf("%w: output does not end with a NUL", ErrExecMalformedOutput)
	}
	records := bytes.Split(stdout[:len(stdout)-1], []byte{0})
	if len(records) != expectedCount {
		return nil, fmt.Errorf("%w: %d records for %d paths", ErrExecMalformedOutput, len(records), expectedCount)
	}
	return records, nil
}

func parseRecord(command CommandID, remotePath string, record []byte) (ExecResult, error) {
	if algorithm, isHash := hashAlgorithms[command]; isHash {
		return parseHashRecord(remotePath, record, algorithm.hexLength)
	}
	return parseStatRecord(remotePath, string(record))
}

// parseStatRecord lit « %d\t%i\t%h\t%s\t%Y » : cinq champs de chiffres seuls, au moins un lien, une date plausible.
func parseStatRecord(remotePath, record string) (ExecResult, error) {
	fields := strings.Split(record, "\t")
	if len(fields) != statFieldCount || slices.ContainsFunc(fields, isNotDecimal) {
		return ExecResult{}, fmt.Errorf("%w: stat record is not five decimal fields", ErrExecMalformedOutput)
	}
	device, deviceErr := strconv.ParseUint(fields[0], 10, 64)
	inode, inodeErr := strconv.ParseUint(fields[1], 10, 64)
	links, linksErr := strconv.ParseUint(fields[2], 10, 64)
	size, sizeErr := strconv.ParseInt(fields[3], 10, 64)
	modTime, modTimeErr := strconv.ParseInt(fields[4], 10, 64)
	hasParseError := deviceErr != nil || inodeErr != nil || linksErr != nil || sizeErr != nil || modTimeErr != nil
	if hasParseError || links == 0 || modTime > maxModTimeSeconds {
		return ExecResult{}, fmt.Errorf("%w: stat field out of range", ErrExecMalformedOutput)
	}
	return ExecResult{
		Path: remotePath, Device: device, Inode: inode, Links: links, Size: size, ModTime: time.Unix(modTime, 0),
	}, nil
}

func isNotDecimal(field string) bool {
	if field == "" || len(field) > maxDecimalDigits {
		return true
	}
	return strings.ContainsFunc(field, func(character rune) bool { return character < '0' || character > '9' })
}

// parseHashRecord lit « <hexadécimal minuscule>  <chemin> » et exige le chemin envoyé, octet pour octet.
func parseHashRecord(remotePath string, record []byte, hexLength int) (ExecResult, error) {
	expectedTail := hashSeparator + remotePath
	isWellFormed := len(record) == hexLength+len(expectedTail) && string(record[hexLength:]) == expectedTail &&
		isLowercaseHex(record[:hexLength])
	if !isWellFormed {
		return ExecResult{}, fmt.Errorf("%w: hash record does not match the path sent", ErrExecMalformedOutput)
	}
	return ExecResult{Path: remotePath, Hash: string(record[:hexLength])}, nil
}

func isLowercaseHex(digits []byte) bool {
	for _, digit := range digits {
		isHexDigit := (digit >= '0' && digit <= '9') || (digit >= 'a' && digit <= 'f')
		if !isHexDigit {
			return false
		}
	}
	return true
}
