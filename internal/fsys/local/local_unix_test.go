//go:build unix

package local

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"
	"time"
)

// fakeInfo porte un Stat_t choisi : seul Sys sert à crossesMount.
type fakeInfo struct{ stat *syscall.Stat_t }

func (fakeInfo) Name() string       { return "fake" }
func (fakeInfo) Size() int64        { return 0 }
func (fakeInfo) Mode() fs.FileMode  { return fs.ModeDir }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return true }
func (info fakeInfo) Sys() any {
	if info.stat == nil {
		return nil
	}
	return info.stat
}

func TestCrossesMount_refusesAnotherDeviceOrAnUnknownIdentity(t *testing.T) {
	rootInfo := fakeInfo{stat: &syscall.Stat_t{Dev: 1, Ino: 10}}
	cases := map[string]fs.FileInfo{
		"other device":     fakeInfo{stat: &syscall.Stat_t{Dev: 2, Ino: 11}},
		"unknown identity": fakeInfo{},
	}
	for name, childInfo := range cases {
		gotValue, err := crossesMount(rootInfo, childInfo, nil)
		if err != nil || !gotValue {
			t.Errorf("%s: crossesMount = %v, %v, want true", name, gotValue, err)
		}
	}
}

func TestClassifyLinkError_mapsKernelRefusals(t *testing.T) {
	cases := map[syscall.Errno]error{
		syscall.EEXIST: ErrDestinationExists,
		syscall.EXDEV:  ErrCrossDevice,
		syscall.EPERM:  ErrLinkRefused,
		syscall.EMLINK: ErrLinkRefused,
	}
	for errno, expectedValue := range cases {
		if gotValue := ClassifyLinkError(errno); !errors.Is(gotValue, expectedValue) || !errors.Is(gotValue, errno) {
			t.Errorf("ClassifyLinkError(%v) = %v, want %v wrapping the cause", errno, gotValue, expectedValue)
		}
	}
	gotValue := ClassifyLinkError(syscall.EIO)
	isClassified := errors.Is(gotValue, ErrLinkRefused) || errors.Is(gotValue, ErrCrossDevice) || errors.Is(gotValue, ErrDestinationExists)
	if !errors.Is(gotValue, syscall.EIO) || isClassified {
		t.Errorf("ClassifyLinkError(EIO) = %v, want it unchanged", gotValue)
	}
	if gotValue := ClassifyLinkError(nil); gotValue != nil {
		t.Errorf("ClassifyLinkError(nil) = %v, want nil", gotValue)
	}
}
