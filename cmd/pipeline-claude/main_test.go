package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "sem argumentos é uso inválido", args: nil, wantCode: exitBlocked, wantStderr: "Uso: pipeline-claude"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "pipeline-claude "},
		{name: "--version", args: []string{"--version"}, wantCode: exitOK, wantStdout: "pipeline-claude "},
		{name: "help", args: []string{"help"}, wantCode: exitOK, wantStdout: "Códigos de saída"},
		{name: "comando desconhecido", args: []string{"xpto"}, wantCode: exitBlocked, wantStderr: `comando desconhecido "xpto"`},
		// Falha fechada: enquanto o hook não existe, o binário bloqueia.
		{name: "hook não implementado bloqueia", args: []string{"hook"}, wantCode: exitBlocked, wantStderr: "Fase 1"},
		{name: "init não implementado", args: []string{"init"}, wantCode: exitBlocked, wantStderr: "Fase 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Fatalf("código = %d, quer %d (stderr: %q)", got, tt.wantCode, stderr.String())
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, quer conter %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, quer conter %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestPlannedCoversUsage(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for cmd := range planned {
		if !strings.Contains(buf.String(), cmd) {
			t.Errorf("comando previsto %q não aparece na ajuda", cmd)
		}
	}
}
