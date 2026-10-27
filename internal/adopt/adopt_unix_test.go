//go:build unix

package adopt

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// createFile fixa a permissão do catálogo mesmo com umask restritiva.
func TestCreateFileIgnoresUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	name := filepath.Join(t.TempDir(), "pre-push")
	if err := createFile(name, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("createFile: %v", err)
	}
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("modo = %v (%v), quer 0755", info.Mode().Perm(), err)
	}
}
