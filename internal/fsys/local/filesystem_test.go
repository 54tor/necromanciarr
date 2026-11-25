package local

import (
	"strings"
	"testing"
)

func TestFilesystemKind_classifiesStatfsMagic(t *testing.T) {
	cases := []struct {
		name          string
		magic         uint32
		expectedValue FilesystemKind
	}{
		{"fuse", 0x65735546, Fuse},
		{"nfs", 0x6969, Network},
		{"smb", 0x517b, Network},
		{"cifs", 0xff534d42, Network},
		{"smb2", 0xfe534d42, Network},
		{"ext4", 0xef53, Local},
		{"tmpfs", 0x01021994, Local},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if gotValue := kindFromMagic(testCase.magic); gotValue != testCase.expectedValue {
				t.Fatalf("gotValue %v, expectedValue %v", gotValue, testCase.expectedValue)
			}
		})
	}
}

func TestFilesystemKind_realCallOnTempDir(t *testing.T) {
	gotValue, err := FilesystemKindOf(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(gotValue.String()) == "" {
		t.Fatal("empty filesystem kind name")
	}
}

func TestFilesystemKind_classifiesNetworkMagicNumbers(t *testing.T) {
	networkMagics := map[string]uint32{
		"9p": 0x01021997, "ceph": 0x00c36400, "afs": 0x5346414f, "kafs": 0x6b414653, "lustre": 0x0bd00bd0,
		"gfs2": 0x01161970, "ocfs2": 0x7461636f, "coda": 0x73757245, "ncp": 0x564c, "orangefs": 0x20030528,
		"nfs": 0x6969, "smb": 0x517b, "cifs": 0xff534d42, "smb2": 0xfe534d42,
	}
	for name, magic := range networkMagics {
		if gotValue := kindFromMagic(magic); gotValue != Network {
			t.Fatalf("%s (%#x): gotValue %v, expectedValue network", name, magic, gotValue)
		}
	}
}
