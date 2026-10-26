//go:build unix

package fmthook

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Um FIFO com nome .go travaria a leitura; a checagem de arquivo regular
// bloqueia antes de abrir.
func TestFIFO(t *testing.T) {
	root := newProject(t, t.TempDir())
	path := filepath.Join(root, "x.go")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("mkfifo indisponível: %v", err)
	}
	done := make(chan int, 1)
	go func() {
		code, _ := run(t, edit(t, root, "Edit", path))
		done <- code
	}()
	select {
	case code := <-done:
		if code != ExitBlock {
			t.Errorf("código = %d, quer %d", code, ExitBlock)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook travou lendo FIFO")
	}
}
