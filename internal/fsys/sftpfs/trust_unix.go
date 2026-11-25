//go:build unix

package sftpfs

import "syscall"

// noFollowFlag refuse à l'ouverture un knownHosts remplacé par un lien symbolique après son Lstat.
const noFollowFlag = syscall.O_NOFOLLOW
