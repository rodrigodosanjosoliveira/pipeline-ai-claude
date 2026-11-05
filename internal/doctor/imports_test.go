package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// setConfig troca (ou, com value nil, remove) uma chave da configuração gerada.
func setConfig(t *testing.T, root, key string, value any) {
	t.Helper()
	p := filepath.Join(root, config.FileName)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(m, key)
	} else {
		m[key] = value
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// addSurface acrescenta padrões a security_surface_paths da config gerada.
func addSurface(t *testing.T, root string, patterns ...string) {
	t.Helper()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	setConfig(t, root, "security_surface_paths", append(cfg.SecuritySurfacePaths, patterns...))
}

// goFile grava um arquivo Go com os imports dados, criando o diretório.
func goFile(t *testing.T, root, rel, pkg string, imports ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\nimport (\n")
	for _, imp := range imports {
		b.WriteString("\t_ \"" + imp + "\"\n")
	}
	b.WriteString(")\n")
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckImports(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		status Status
		msg    []string // em ordem
		noMsg  []string
		action []string
	}{
		{name: "projeto gerado não importa nada sensível", status: StatusOK, msg: []string{"nenhum import sensível fora de security_surface_paths"}},
		{
			name: "pgx em internal/migrate fora da superfície avisa",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/migrate/migrate.go", "migrate", "context", "github.com/jackc/pgx/v5")
			},
			status: StatusWarn,
			msg:    []string{"internal/migrate (github.com/jackc/pgx/v5)"},
			noMsg:  []string{"context"},
			action: []string{`"internal/migrate/"`, "security_surface_paths", "security_surface_imports"},
		},
		{
			name: "pgx em internal/migrate dentro da superfície não avisa",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/migrate/migrate.go", "migrate", "github.com/jackc/pgx/v5")
				addSurface(t, root, "internal/migrate/")
			},
			status: StatusOK,
		},
		{
			name: "os/exec em cmd avisa, em ordem de diretório",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/migrate/migrate.go", "migrate", "database/sql")
				goFile(t, root, "cmd/biblioteca-bairro/run.go", "main", "os/exec", "os")
			},
			status: StatusWarn,
			msg:    []string{"cmd/biblioteca-bairro (os/exec)", "internal/migrate (database/sql)"},
			noMsg:  []string{"os,", "(os)"},
			action: []string{`"cmd/biblioteca-bairro/"`, `"internal/migrate/"`},
		},
		{
			name: "vários imports sensíveis no mesmo diretório aparecem juntos e sem repetição",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/acervo/repo.go", "acervo", "github.com/lib/pq", "database/sql")
				goFile(t, root, "internal/acervo/tx.go", "acervo", "database/sql")
			},
			status: StatusWarn,
			msg:    []string{"internal/acervo (database/sql, github.com/lib/pq)"},
		},
		{
			name: "import só em _test.go não avisa",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/acervo/repo_test.go", "acervo", "database/sql", "os/exec")
			},
			status: StatusOK,
		},
		{
			name: "vendor, testdata, ponto e sublinhado ficam de fora",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "vendor/example.com/driver/d.go", "driver", "database/sql")
				goFile(t, root, "internal/acervo/testdata/gerador.go", "main", "os/exec")
				goFile(t, root, ".cache/x.go", "x", "os/exec")
				goFile(t, root, "internal/_antigo/x.go", "x", "os/exec")
			},
			status: StatusOK,
		},
		{
			name: "prefixo é de caminho, não de texto",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/acervo/repo.go", "acervo", "github.com/lib/pqx", "os/execabs")
			},
			status: StatusOK,
		},
		{
			name: "security_surface_imports substitui a lista padrão",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/migrate/migrate.go", "migrate", "github.com/jackc/pgx/v5")
				goFile(t, root, "internal/notifica/smtp.go", "notifica", "net/smtp")
				setConfig(t, root, "security_surface_imports", []string{"net/smtp"})
			},
			status: StatusWarn,
			msg:    []string{"internal/notifica (net/smtp)"},
			noMsg:  []string{"internal/migrate", "pgx"},
		},
		{
			name: "arquivo na raiz sugere o próprio arquivo",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "tools.go", "main", "os/exec")
			},
			status: StatusWarn,
			msg:    []string{". (os/exec)"},
			action: []string{`"tools.go"`},
		},
		{
			name: "arquivo coberto um a um não avisa",
			mutate: func(t *testing.T, root string) {
				goFile(t, root, "internal/acervo/repo.go", "acervo", "database/sql")
				addSurface(t, root, "internal/acervo/repo.go")
			},
			status: StatusOK,
		},
		{
			name: "arquivo .go ilegível avisa com o caminho e não derruba o resto",
			mutate: func(t *testing.T, root string) {
				writeRel(t, root, "cmd/biblioteca-bairro/quebrado.go", "package main\n\nimport (\n\t\"os/exec\"\n")
				goFile(t, root, "internal/migrate/migrate.go", "migrate", "database/sql")
			},
			status: StatusWarn,
			msg:    []string{"internal/migrate (database/sql)", "cmd/biblioteca-bairro/quebrado.go"},
			action: []string{`"internal/migrate/"`, "corrija"},
		},
		{
			name:   "config ausente",
			mutate: func(t *testing.T, root string) { removeRel(t, root, config.FileName) },
			status: StatusWarn,
			msg:    []string{"não verificado"},
			action: []string{"configuração"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := scaffolded(t)
			if tt.mutate != nil {
				tt.mutate(t, root)
			}
			checks := collect(root, healthyEnv().env())
			c := find(t, checks, checkImports)
			if c.Status != tt.status {
				t.Fatalf("status = %s, quero %s (%s → %s)", c.Status, tt.status, c.Message, c.Action)
			}
			rest := c.Message
			for _, s := range tt.msg {
				i := strings.Index(rest, s)
				if i < 0 {
					t.Errorf("mensagem %q sem %q (ou fora de ordem)", c.Message, s)
					continue
				}
				rest = rest[i+len(s):]
			}
			for _, s := range tt.noMsg {
				if strings.Contains(c.Message, s) {
					t.Errorf("mensagem %q com %q", c.Message, s)
				}
			}
			for _, s := range tt.action {
				if !strings.Contains(c.Action, s) {
					t.Errorf("ação %q sem %q", c.Action, s)
				}
			}
			if tt.status == StatusOK && c.Action != "" {
				t.Errorf("ok com ação %q", c.Action)
			}
		})
	}
}

// TestCheckImportsAviso: a checagem nunca vira erro (TD-44) nem muda o código
// de saída do doctor.
func TestCheckImportsAviso(t *testing.T) {
	root := scaffolded(t)
	goFile(t, root, "internal/migrate/migrate.go", "migrate", "github.com/jackc/pgx/v5")
	writeRel(t, root, "internal/migrate/quebrado.go", "não é Go")
	c := find(t, collect(root, healthyEnv().env()), checkImports)
	if c.Status != StatusWarn {
		t.Errorf("status = %s, quero aviso", c.Status)
	}
}

// TestCheckImportsOrdem: a checagem roda depois de subagentes e antes de
// binário.
func TestCheckImportsOrdem(t *testing.T) {
	checks := collect(scaffolded(t), healthyEnv().env())
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	got := strings.Join(names, ",")
	if !strings.Contains(got, checkAgents+","+checkImports+","+checkBinary) {
		t.Errorf("ordem = %s", got)
	}
}
