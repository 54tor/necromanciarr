//go:build !unix

package sftpfs

// noFollowFlag : Windows n'a pas d'O_NOFOLLOW ; le contrôle os.SameFile après ouverture reste la garde.
const noFollowFlag = 0
