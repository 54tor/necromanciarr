package sftptest

import (
	"bytes"
	"crypto/md5"  // #nosec G501 -- empreinte attendue de md5sum, jamais une protection
	"crypto/sha1" // #nosec G505 -- empreinte attendue de sha1sum, jamais une protection
	"encoding/hex"
	"fmt"
	"hash"
	"strings"

	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

// gnuExitPartial : code de sortie de GNU xargs quand une commande lancée a échoué (fichier disparu).
const gnuExitPartial = 123

// gnuCommandNotFound : code de sortie d'un shell pour une ligne inconnue.
const gnuCommandNotFound = 127

// Lignes que le faux hôte GNU reconnaît : copies des argv de sftpfs, recomposées ici pour ne rien exposer de la
// table ; une dérive fait échouer les tests qui exigent RemoteStat.
var (
	gnuStatLine = mustJoinLine("xargs", "-0", "-r", "stat", "--printf=%d\t%i\t%h\t%s\t%Y\\0", "--")
	gnuSHA1Line = mustJoinLine("xargs", "-0", "-r", "sha1sum", "-z", "--")
	gnuMD5Line  = mustJoinLine("xargs", "-0", "-r", "md5sum", "-z", "--")
	gnuNice     = mustJoinLine("nice", "-n", "10", "ionice", "-c3")
	gnuVersions = map[string]string{
		mustJoinLine("stat", "--version"):    "stat (GNU coreutils) 9.5\n",
		mustJoinLine("xargs", "--version"):   "xargs (GNU findutils) 4.10.0\n",
		mustJoinLine("sha1sum", "--version"): "sha1sum (GNU coreutils) 9.5\n",
		mustJoinLine("md5sum", "--version"):  "md5sum (GNU coreutils) 9.5\n",
		mustJoinLine("nice", "--version"):    "nice (GNU coreutils) 9.5\n",
		mustJoinLine("ionice", "--version"):  "ionice from util-linux 2.40\n",
	}
)

func mustJoinLine(arguments ...string) string {
	line, err := shellquote.Join(arguments)
	if err != nil {
		panic(err)
	}
	return line
}

// WithGNUExec sert exec comme un hôte GNU au shell de connexion POSIX : sondes de version, stat et empreintes en lots
// NUL calculés sur le dossier servi (périphérique, inode et liens réels sous Unix). Toute autre ligne : code 127.
func WithGNUExec() Option {
	return func(current *settings) { current.gnuExec = true }
}

// WithExecOutputFilter réécrit la sortie standard de chaque commande servie (sortie hostile, inode nul, date décalée).
func WithExecOutputFilter(filter func(command string, stdout []byte) []byte) Option {
	return func(current *settings) { current.execOutputFilter = filter }
}

// BeforeExec appelle hook avec chaque ligne exec acceptée, avant de la servir (fichier qui disparaît en plein lot).
func BeforeExec(hook func(command string)) Option {
	return func(current *settings) { current.beforeExec = hook }
}

// gnuExec répond à une ligne reçue.
func (server *Server) gnuExec(command string, stdin []byte) ([]byte, []byte, int) {
	if command == shellquote.ShellSemanticsProbe {
		return []byte(shellquote.ShellSemanticsExpected), nil, 0
	}
	if version, known := gnuVersions[command]; known {
		return []byte(version), nil, 0
	}
	unprefixed := strings.TrimPrefix(command, gnuNice+" ")
	records := strings.Split(string(stdin), "\x00")
	paths := records[:len(records)-1]
	switch {
	case command == gnuStatLine:
		return server.gnuAnswer(paths, server.gnuStatRecord)
	case unprefixed == gnuSHA1Line:
		return server.gnuAnswer(paths, server.gnuHashRecord(sha1.New)) // #nosec G401 -- voir l'import
	case unprefixed == gnuMD5Line:
		return server.gnuAnswer(paths, server.gnuHashRecord(md5.New)) // #nosec G401 -- voir l'import
	}
	return nil, []byte("sh: command not found\n"), gnuCommandNotFound
}

// gnuAnswer écrit un enregistrement par chemin ; un chemin illisible est sauté avec le code 123, comme GNU xargs.
func (server *Server) gnuAnswer(paths []string, record func(remotePath string) (string, error)) ([]byte, []byte, int) {
	var stdout, stderr bytes.Buffer
	exit := 0
	for _, remotePath := range paths {
		line, err := record(remotePath)
		if err != nil {
			fmt.Fprintf(&stderr, "cannot read '%s'\n", remotePath)
			exit = gnuExitPartial
			continue
		}
		stdout.WriteString(line)
	}
	return stdout.Bytes(), stderr.Bytes(), exit
}

func (server *Server) gnuStatRecord(remotePath string) (string, error) {
	info, err := server.root.Lstat(servedName(remotePath))
	if err != nil {
		return "", err
	}
	identity := identityOf(info, remotePath)
	return fmt.Sprintf("%d\t%d\t%d\t%d\t%d\x00", identity.device, identity.inode, identity.links, info.Size(), info.ModTime().Unix()), nil
}

func (server *Server) gnuHashRecord(newHash func() hash.Hash) func(remotePath string) (string, error) {
	return func(remotePath string) (string, error) {
		content, err := server.root.ReadFile(servedName(remotePath))
		if err != nil {
			return "", err
		}
		hasher := newHash()
		hasher.Write(content)
		return hex.EncodeToString(hasher.Sum(nil)) + "  " + remotePath + "\x00", nil
	}
}

// servedName rend le nom relatif au dossier servi d'un chemin distant absolu (« / » : le dossier lui-même).
func servedName(remotePath string) string {
	relative := strings.TrimPrefix(remotePath, "/")
	if relative == "" {
		return "."
	}
	return relative
}

// fileIdentity : ce que stat %d, %i et %h rendent.
type fileIdentity struct {
	device, inode, links uint64
}
