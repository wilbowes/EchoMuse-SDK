package echomuse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidFileModeRejectsShellSyntax(t *testing.T) {
	for _, mode := range []string{"600", "0644", "000", "7777"} {
		if !validFileMode(mode) {
			t.Errorf("validFileMode(%q) = false", mode)
		}
	}
	for _, mode := range []string{"", "8", "600;id", "600 1", "-rw", "12345"} {
		if validFileMode(mode) {
			t.Errorf("validFileMode(%q) = true", mode)
		}
	}
}

func TestWriteCredentialsTightensExistingPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := WriteCredentials(dir, "secret", nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("token mode = %04o, want 0600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("credential directory mode = %04o, want 0700", got)
	}
}
