// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/config/config_test.go @ 0eac73a (MIT).
// Alterações: casos originais mantidos, adaptados ao novo formato (TD-07, TD-09,
// TD-14) com comentário onde o comportamento mudou; novos casos para os campos e
// validações do .pipeline-claude.json; alvo de fuzz FuzzDecode.

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// validBody era "desktop" no original; a v1 só aceita api + postgres (TD-07).
const validBody = `{
  "version": 1,
  "project_type": "api",
  "database": "postgres",
  "git_provider": "github",
  "protected_paths": ["internal/", "cmd/", "frontend/src/"],
  "editor_agents": ["dev-implementation", "test-writer"],
  "security_surface_paths": ["internal/auth/", "go.mod"]
}`

// baseFields é a configuração mínima válida usada pelos casos de validação.
func baseFields() map[string]any {
	return map[string]any{
		"version":         1,
		"project_type":    "api",
		"database":        "postgres",
		"git_provider":    "github",
		"protected_paths": []any{"internal/", "docs-ai/"},
	}
}

// bodyWith devolve a configuração mínima com as chaves de over sobrescritas.
// Valor nil remove a chave.
func bodyWith(t *testing.T, over map[string]any) string {
	t.Helper()
	m := baseFields()
	for k, v := range over {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func loadBody(t *testing.T, body string) *Config {
	t.Helper()
	dir := t.TempDir()
	writeConfig(t, dir, body)
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestFindSobeAteAraiz(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, validBody)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := Find(deep)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := filepath.Join(root, FileName)
	if got != want {
		t.Errorf("Find = %q, quer %q", got, want)
	}
}

func TestFindSemConfig(t *testing.T) {
	// t.TempDir() fica sob /tmp; se existir um .pipeline-claude.json acima, o
	// teste perde o sentido. Checamos o tipo de erro, não a ausência absoluta.
	dir := t.TempDir()
	if _, err := Find(dir); err == nil {
		t.Skip("existe um " + FileName + " acima de " + dir + "; nada a testar")
	} else if !errors.Is(err, ErrNotFound) {
		t.Errorf("erro = %v, quer ErrNotFound", err)
	}
}

func TestLoadSemConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := Find(dir); err == nil {
		t.Skip("existe um " + FileName + " acima de " + dir + "; nada a testar")
	}
	if _, err := Load(dir); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load erro = %v, quer ErrNotFound", err)
	}
}

// Diferente do original, que seguia subindo: um diretório com o nome do arquivo
// é configuração ilegível, e subir poderia carregar a configuração de outro
// projeto (falha fechada, TD-08).
func TestFindDiretorioComNomeDoArquivo(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, validBody)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(sub, FileName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := Find(sub)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Find erro = %v, quer erro diferente de ErrNotFound", err)
	}
}

// Erro de acesso ao procurar o arquivo não pode virar "não encontrado" nem
// pular para a configuração de um diretório acima (falha fechada, TD-08).
func TestFindSemPermissao(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissão de diretório não se aplica neste ambiente")
	}
	root := t.TempDir()
	writeConfig(t, root, validBody)
	locked := filepath.Join(root, "trancado")
	deep := filepath.Join(locked, "a")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, err := Find(deep)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Find erro = %v, quer erro de acesso", err)
	}
}

func TestLoadRoot(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, validBody)
	deep := filepath.Join(root, "x", "y")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	c, err := Load(deep)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Root() != root {
		t.Errorf("Root = %q, quer %q", c.Root(), root)
	}
}

func TestLoadValidacao(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string // trecho esperado na mensagem; vazio = sem erro
	}{
		// Casos do original.
		{name: "config válida", body: validBody},
		{
			// Original: "config válida cli". A v1 só aceita api (TD-07).
			name:    "project_type cli fora da v1",
			body:    bodyWith(t, map[string]any{"project_type": "cli"}),
			wantErr: "project_type",
		},
		{
			name: "enabled_modules válidos",
			body: bodyWith(t, map[string]any{"enabled_modules": []any{"data-model", "release-ops"}}),
		},
		{
			name:    "enabled_modules com módulo inválido",
			body:    bodyWith(t, map[string]any{"enabled_modules": []any{"modulo-inexistente"}}),
			wantErr: "enabled_modules",
		},
		{
			name:    "enabled_modules com item vazio",
			body:    bodyWith(t, map[string]any{"enabled_modules": []any{""}}),
			wantErr: "enabled_modules",
		},
		{
			name:    "project_type ausente",
			body:    bodyWith(t, map[string]any{"project_type": nil}),
			wantErr: "project_type",
		},
		{
			name:    "project_type inválido",
			body:    bodyWith(t, map[string]any{"project_type": "mobile"}),
			wantErr: "project_type",
		},
		{
			name:    "protected_paths vazio",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{}}),
			wantErr: "protected_paths",
		},
		{
			name:    "version futura",
			body:    bodyWith(t, map[string]any{"version": 99}),
			wantErr: "version",
		},
		{
			name:    "JSON malformado",
			body:    `{"version":1,`,
			wantErr: "JSON inválido",
		},

		// Original assumia version 1 quando ausente; agora é obrigatória (TD-14).
		{
			name:    "version ausente",
			body:    bodyWith(t, map[string]any{"version": nil}),
			wantErr: "version",
		},
		{
			name:    "campo desconhecido",
			body:    bodyWith(t, map[string]any{"protect_paths": []any{"x/"}}),
			wantErr: "protect_paths",
		},
		{
			name:    "campo desconhecido aninhado",
			body:    bodyWith(t, map[string]any{"report_schema": map[string]any{"section_level": 2, "extra": 1}}),
			wantErr: "extra",
		},
		{
			name:    "conteúdo depois do objeto",
			body:    bodyWith(t, nil) + ` {}`,
			wantErr: "JSON inválido",
		},
		{
			name:    "JSON não objeto",
			body:    `[1,2]`,
			wantErr: "JSON inválido",
		},
		{
			name:    "JSON null",
			body:    `null`,
			wantErr: "version",
		},
		{
			name:    "database ausente",
			body:    bodyWith(t, map[string]any{"database": nil}),
			wantErr: "database",
		},
		{
			name:    "database não suportado",
			body:    bodyWith(t, map[string]any{"database": "mysql"}),
			wantErr: "database",
		},
		{
			name: "git_provider gitlab",
			body: bodyWith(t, map[string]any{"git_provider": "gitlab"}),
		},
		{
			name:    "git_provider ausente",
			body:    bodyWith(t, map[string]any{"git_provider": nil}),
			wantErr: "git_provider",
		},
		{
			name:    "git_provider não suportado",
			body:    bodyWith(t, map[string]any{"git_provider": "bitbucket"}),
			wantErr: "git_provider",
		},
		{
			name:    "protected_paths ausente",
			body:    bodyWith(t, map[string]any{"protected_paths": nil}),
			wantErr: "protected_paths",
		},

		// base_ref vai como argumento do git; hífen inicial viraria opção.
		{
			name:    "base_ref começando com hífen",
			body:    bodyWith(t, map[string]any{"base_ref": "--output=/tmp/x"}),
			wantErr: "base_ref",
		},
		{
			name:    "base_ref com espaço",
			body:    bodyWith(t, map[string]any{"base_ref": "origin/main extra"}),
			wantErr: "base_ref",
		},

		// Caminhos.
		{
			name:    "caminho absoluto",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{"/etc/"}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho com unidade do Windows",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{"C:/x/"}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho com barra invertida",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{`internal\auth\`}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho com .. escapando da raiz",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{"internal/../../x/"}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho que é só ..",
			body:    bodyWith(t, map[string]any{"security_surface_paths": []any{".."}}),
			wantErr: "security_surface_paths",
		},
		{
			name:    "caminho vazio",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{""}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho que é a raiz",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{"./"}}),
			wantErr: "protected_paths",
		},
		{
			name:    "caminho com espaço nas pontas",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{" internal/"}}),
			wantErr: "protected_paths",
		},
		{
			name: "caminho com .. interno que fica na raiz",
			body: bodyWith(t, map[string]any{"protected_paths": []any{"internal/../cmd/"}}),
		},
		{
			name:    "deliveries_dir escapando",
			body:    bodyWith(t, map[string]any{"deliveries_dir": "../fora"}),
			wantErr: "deliveries_dir",
		},
		{
			name:    "index_file absoluto",
			body:    bodyWith(t, map[string]any{"index_file": "/INDEX.md"}),
			wantErr: "index_file",
		},
		{
			name:    "index_file terminado em barra",
			body:    bodyWith(t, map[string]any{"index_file": "docs-ai/"}),
			wantErr: "index_file",
		},
		{
			name:    "report_template escapando",
			body:    bodyWith(t, map[string]any{"report_template": "../tpl.md"}),
			wantErr: "report_template",
		},
		{
			name:    "invariants_files escapando",
			body:    bodyWith(t, map[string]any{"invariants_files": []any{"docs/INV.md", "../x.md"}}),
			wantErr: "invariants_files",
		},

		// no_agent_paths ⊆ protected_paths por casamento de padrão.
		{
			name: "no_agent_paths dentro de protected_paths",
			body: bodyWith(t, map[string]any{"no_agent_paths": []any{"docs-ai/restrito/", "internal/x.go"}}),
		},
		{
			name: "no_agent_paths igual a protected_paths",
			body: bodyWith(t, map[string]any{"no_agent_paths": []any{"docs-ai/"}}),
		},
		{
			name: "no_agent_paths com caixa diferente",
			body: bodyWith(t, map[string]any{"no_agent_paths": []any{"Docs-AI/Restrito/"}}),
		},
		{
			name:    "no_agent_paths fora de protected_paths",
			body:    bodyWith(t, map[string]any{"no_agent_paths": []any{"scripts/"}}),
			wantErr: "no_agent_paths",
		},
		{
			name:    "no_agent_paths com prefixo parcial",
			body:    bodyWith(t, map[string]any{"no_agent_paths": []any{"internals/"}}),
			wantErr: "no_agent_paths",
		},
		{
			name:    "no_agent_paths mais amplo que protected_paths",
			body:    bodyWith(t, map[string]any{"protected_paths": []any{"docs-ai/restrito/"}, "no_agent_paths": []any{"docs-ai/"}}),
			wantErr: "no_agent_paths",
		},

		// Nomes.
		{
			name:    "editor_agents com maiúscula",
			body:    bodyWith(t, map[string]any{"editor_agents": []any{"Dev-Implementation"}}),
			wantErr: "editor_agents",
		},
		{
			name:    "editor_agents vazio",
			body:    bodyWith(t, map[string]any{"editor_agents": []any{""}}),
			wantErr: "editor_agents",
		},
		{
			name:    "editor_agents começando com dígito",
			body:    bodyWith(t, map[string]any{"editor_agents": []any{"1dev"}}),
			wantErr: "editor_agents",
		},
		{
			name: "tenant-isolation no catálogo",
			body: bodyWith(t, map[string]any{"enabled_modules": []any{"tenant-isolation"}}),
		},
		{
			// O original aceitava caixa diferente em enabled_modules.
			name:    "enabled_modules com maiúscula",
			body:    bodyWith(t, map[string]any{"enabled_modules": []any{"Data-Model"}}),
			wantErr: "enabled_modules",
		},

		// delivery_id_pattern.
		{
			name: "delivery_id_pattern ancorado",
			body: bodyWith(t, map[string]any{"delivery_id_pattern": `^(FEAT|FIX)-[0-9]{3}$`}),
		},
		{
			name:    "delivery_id_pattern sem âncora final",
			body:    bodyWith(t, map[string]any{"delivery_id_pattern": `^FEAT-[0-9]+`}),
			wantErr: "delivery_id_pattern",
		},
		{
			name:    "delivery_id_pattern sem âncora inicial",
			body:    bodyWith(t, map[string]any{"delivery_id_pattern": `FEAT-[0-9]+$`}),
			wantErr: "delivery_id_pattern",
		},
		{
			name:    "delivery_id_pattern com alternância desancorada",
			body:    bodyWith(t, map[string]any{"delivery_id_pattern": `^FEAT-1|FIX-2$`}),
			wantErr: "delivery_id_pattern",
		},
		{
			name:    "delivery_id_pattern multilinha",
			body:    bodyWith(t, map[string]any{"delivery_id_pattern": `(?m)^FEAT-[0-9]+$`}),
			wantErr: "delivery_id_pattern",
		},
		{
			name:    "delivery_id_pattern não compila",
			body:    bodyWith(t, map[string]any{"delivery_id_pattern": `^FEAT-[0-9+$`}),
			wantErr: "delivery_id_pattern",
		},
		{
			name:    "delivery_prefixes com item vazio",
			body:    bodyWith(t, map[string]any{"delivery_prefixes": []any{"FEAT", ""}}),
			wantErr: "delivery_prefixes",
		},

		// auto_flags.
		{
			name: "auto_flags válido",
			body: bodyWith(t, map[string]any{"auto_flags": map[string]any{"require_release_ops": []any{"migrations/"}}}),
		},
		{
			name:    "auto_flags com chave fora do padrão",
			body:    bodyWith(t, map[string]any{"auto_flags": map[string]any{"release_ops": []any{"migrations/"}}}),
			wantErr: "auto_flags",
		},
		{
			name:    "auto_flags com chave em maiúscula",
			body:    bodyWith(t, map[string]any{"auto_flags": map[string]any{"require_Security": []any{"x/"}}}),
			wantErr: "auto_flags",
		},
		{
			name:    "auto_flags sem padrões",
			body:    bodyWith(t, map[string]any{"auto_flags": map[string]any{"require_security": []any{}}}),
			wantErr: "auto_flags",
		},
		{
			name:    "auto_flags com caminho escapando",
			body:    bodyWith(t, map[string]any{"auto_flags": map[string]any{"require_security": []any{"../x/"}}}),
			wantErr: "auto_flags",
		},

		// sensors.
		{
			name: "sensors válidos",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint", "cmd": []any{"make", "lint"}},
				map[string]any{"name": "test_unit", "cmd": []any{"go", "test", "./..."}},
			}}),
		},
		{
			name: "sensors com nome repetido",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint", "cmd": []any{"make", "lint"}},
				map[string]any{"name": "lint", "cmd": []any{"golangci-lint", "run"}},
			}}),
			wantErr: "sensors",
		},
		{
			name: "sensors com nome inválido",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint-go", "cmd": []any{"make", "lint"}},
			}}),
			wantErr: "sensors",
		},
		{
			name: "sensors sem cmd",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint"},
			}}),
			wantErr: "sensors",
		},
		{
			name: "sensors com cmd vazio",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint", "cmd": []any{""}},
			}}),
			wantErr: "sensors",
		},
		{
			name: "sensors com campo desconhecido",
			body: bodyWith(t, map[string]any{"sensors": []any{
				map[string]any{"name": "lint", "cmd": []any{"make"}, "shell": true},
			}}),
			wantErr: "shell",
		},

		// report_schema.
		{
			name:    "report_schema com section_level zero",
			body:    bodyWith(t, map[string]any{"report_schema": map[string]any{"section_level": 0}}),
			wantErr: "report_schema",
		},
		{
			name:    "report_schema com section_level 7",
			body:    bodyWith(t, map[string]any{"report_schema": map[string]any{"section_level": 7}}),
			wantErr: "report_schema",
		},
		{
			name: "report_schema com flag fora do padrão",
			body: bodyWith(t, map[string]any{"report_schema": map[string]any{
				"section_level": 2,
				"flag_sections": map[string]any{"security": map[string]any{"section": "Security"}},
			}}),
			wantErr: "report_schema",
		},
		{
			name: "report_schema com flag sem seção",
			body: bodyWith(t, map[string]any{"report_schema": map[string]any{
				"section_level": 2,
				"flag_sections": map[string]any{"require_security": map[string]any{"fields": []any{"Resultado"}}},
			}}),
			wantErr: "report_schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, tt.body)
			_, err := Load(dir)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("erro inesperado: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("esperava erro contendo %q, obteve nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("erro = %q, quer trecho %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	c := loadBody(t, validBody)
	if c.DeliveriesDir != "docs-ai/deliveries" {
		t.Errorf("DeliveriesDir = %q", c.DeliveriesDir)
	}
	if c.BaseRef != "origin/main" {
		t.Errorf("BaseRef = %q", c.BaseRef)
	}
	if c.IndexFile != "docs-ai/deliveries/INDEX.md" {
		t.Errorf("IndexFile = %q", c.IndexFile)
	}
	if c.ReportSchema == nil {
		t.Fatal("ReportSchema = nil, quer o esquema padrão")
	}
	if !reflect.DeepEqual(*c.ReportSchema, DefaultReportSchema()) {
		t.Errorf("ReportSchema = %+v, quer padrão %+v", *c.ReportSchema, DefaultReportSchema())
	}
}

func TestDefaultsIndexSegueDeliveriesDir(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{"deliveries_dir": "entregas/"}))
	if c.DeliveriesDir != "entregas" {
		t.Errorf("DeliveriesDir = %q, quer %q", c.DeliveriesDir, "entregas")
	}
	if c.IndexFile != "entregas/INDEX.md" {
		t.Errorf("IndexFile = %q, quer %q", c.IndexFile, "entregas/INDEX.md")
	}
}

func TestDefaultsNaoSobrescrevem(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{
		"base_ref":       "origin/develop",
		"deliveries_dir": "entregas",
		"index_file":     "docs/INDICE.md",
	}))
	if c.BaseRef != "origin/develop" || c.DeliveriesDir != "entregas" || c.IndexFile != "docs/INDICE.md" {
		t.Errorf("valores informados sobrescritos: base_ref=%q deliveries_dir=%q index_file=%q",
			c.BaseRef, c.DeliveriesDir, c.IndexFile)
	}
}

func TestDefaultReportSchema(t *testing.T) {
	s := DefaultReportSchema()
	if s.SectionLevel != 2 {
		t.Errorf("SectionLevel = %d, quer 2", s.SectionLevel)
	}
	wantSections := []string{"PM/Triage", "Architecture/Guardrails", "Dev", "QA", "Docs", "Final Checklist"}
	if !reflect.DeepEqual(s.SectionsRequired, wantSections) {
		t.Errorf("SectionsRequired = %v", s.SectionsRequired)
	}
	if !reflect.DeepEqual(s.FrontRequired, []string{"delivery_id", "status"}) {
		t.Errorf("FrontRequired = %v", s.FrontRequired)
	}
	if got := s.FieldsRequired["Dev"]; len(got) != 4 {
		t.Errorf("FieldsRequired[Dev] = %v", got)
	}
	if fs, ok := s.FlagSections["require_security"]; !ok || fs.Section != "Security" {
		t.Errorf("FlagSections[require_security] = %+v", fs)
	}
	if fs, ok := s.FlagSections["require_release_ops"]; !ok || fs.Section != "Release/Ops" {
		t.Errorf("FlagSections[require_release_ops] = %+v", fs)
	}
	if s.ChecklistSection != "Final Checklist" {
		t.Errorf("ChecklistSection = %q", s.ChecklistSection)
	}
	if len(s.PlaceholderValues) != 7 || s.PlaceholderValues[0] != "" {
		t.Errorf("PlaceholderValues = %q", s.PlaceholderValues)
	}
	if !reflect.DeepEqual(s.ForbidMarkers, []string{"TODO", "TBD", "FIXME", "XXX"}) {
		t.Errorf("ForbidMarkers = %v", s.ForbidMarkers)
	}

	// Cada chamada devolve cópia independente.
	s.SectionsRequired[0] = "alterado"
	s.FieldsRequired["Dev"][0] = "alterado"
	again := DefaultReportSchema()
	if again.SectionsRequired[0] != "PM/Triage" || again.FieldsRequired["Dev"][0] != "Arquivos alterados" {
		t.Error("DefaultReportSchema compartilha estado entre chamadas")
	}
}

func TestReportSchemaInformadoSubstituiPadrao(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{"report_schema": map[string]any{
		"section_level":     3,
		"sections_required": []any{"Resumo"},
	}}))
	if c.ReportSchema.SectionLevel != 3 {
		t.Errorf("SectionLevel = %d", c.ReportSchema.SectionLevel)
	}
	if !reflect.DeepEqual(c.ReportSchema.SectionsRequired, []string{"Resumo"}) {
		t.Errorf("SectionsRequired = %v", c.ReportSchema.SectionsRequired)
	}
	if len(c.ReportSchema.FrontRequired) != 0 {
		t.Errorf("FrontRequired = %v, quer vazio (sem mistura com o padrão)", c.ReportSchema.FrontRequired)
	}
}

func TestCaminhosNormalizados(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{
		"protected_paths":        []any{"./internal//", "internal/../cmd/"},
		"security_surface_paths": []any{"./go.mod"},
	}))
	if !reflect.DeepEqual(c.ProtectedPaths, []string{"internal/", "cmd/"}) {
		t.Errorf("ProtectedPaths = %q", c.ProtectedPaths)
	}
	if !reflect.DeepEqual(c.SecuritySurfacePaths, []string{"go.mod"}) {
		t.Errorf("SecuritySurfacePaths = %q", c.SecuritySurfacePaths)
	}
}

// Original recebia caminho absoluto e resolvia contra a raiz. A resolução
// (relativo à raiz, symlinks, fora da raiz) passou para o hook (TD-09); aqui o
// caminho já chega relativo à raiz, com "/" ou separador do sistema.
func TestIsProtected(t *testing.T) {
	c := loadBody(t, validBody)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"dentro de internal", "internal/auth/token.go", true},
		{"dentro de cmd", "cmd/app/main.go", true},
		{"dentro do frontend", "frontend/src/App.tsx", true},
		{"fora dos protegidos", "docs-ai/nota.md", false},
		{"raiz do projeto", "README.md", false},
		{"prefixo parcial não casa", "internals/x.go", false},
		{"fora da raiz", "../outro/internal/x.go", false},
		// Novos.
		{"o próprio diretório", "internal", true},
		{"caixa diferente", "Internal/Auth/Token.go", true}, // TD-09
		{"tudo em maiúscula", "CMD/APP/MAIN.GO", true},      // TD-09
		{"separador do sistema", filepath.Join("internal", "x.go"), true},
		{"com ./ inicial", "./internal/x.go", true},
		{"com .. interno", "docs/../internal/x.go", true},
		{"com .. saindo de dentro", "internal/../README.md", false},
		{"vazio", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsProtected(tt.path); got != tt.want {
				t.Errorf("IsProtected(%q) = %v, quer %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsProtectedPadraoSemBarraFinal(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{"protected_paths": []any{"go.mod", "migrations"}}))
	tests := []struct {
		path string
		want bool
	}{
		{"go.mod", true},
		{"GO.MOD", true},
		{"go.mod/x", true}, // casa também como diretório
		{"go.modx", false},
		{"migrations/001.sql", true},
		{"migrations", true},
		{"migrations_old/001.sql", false},
	}
	for _, tt := range tests {
		if got := c.IsProtected(tt.path); got != tt.want {
			t.Errorf("IsProtected(%q) = %v, quer %v", tt.path, got, tt.want)
		}
	}
}

func TestIsNoAgent(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{"no_agent_paths": []any{"docs-ai/restrito/"}}))
	tests := []struct {
		path string
		want bool
	}{
		{"docs-ai/restrito/x.md", true},
		{"Docs-AI/Restrito/X.md", true}, // TD-09
		{"docs-ai/restrito", true},
		{"docs-ai/outro.md", false},
		{"docs-ai/restritos/x.md", false},
		{"internal/x.go", false},
	}
	for _, tt := range tests {
		if got := c.IsNoAgent(tt.path); got != tt.want {
			t.Errorf("IsNoAgent(%q) = %v, quer %v", tt.path, got, tt.want)
		}
	}
}

func TestIsNoAgentSemPadroes(t *testing.T) {
	c := loadBody(t, validBody)
	if c.IsNoAgent("internal/x.go") {
		t.Error("IsNoAgent = true sem no_agent_paths configurado")
	}
}

func TestIsEditorAgent(t *testing.T) {
	c := loadBody(t, validBody)

	tests := []struct {
		name  string
		agent string
		want  bool
	}{
		{"agent autorizado", "dev-implementation", true},
		{"outro autorizado", "test-writer", true},
		{"case-insensitive", "Dev-Implementation", true},
		{"com espaços", "  test-writer  ", true},
		{"agent não autorizado", "pm-triage", false},
		{"sessão principal", "", false},
		{"só espaços", "   ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsEditorAgent(tt.agent); got != tt.want {
				t.Errorf("IsEditorAgent(%q) = %v, quer %v", tt.agent, got, tt.want)
			}
		})
	}
}

func TestIsSecuritySurface(t *testing.T) {
	c := loadBody(t, validBody)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"diretório de auth", "internal/auth/jwt.go", true},
		{"arquivo exato go.mod", "go.mod", true},
		{"fora da superfície", "internal/ui/render.go", false},
		{"go.sum não listado", "go.sum", false},
		{"caixa diferente", "Internal/AUTH/jwt.go", true}, // TD-09
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsSecuritySurface(tt.path); got != tt.want {
				t.Errorf("IsSecuritySurface(%q) = %v, quer %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFlagsTriggeredBy(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{"auto_flags": map[string]any{
		"require_release_ops": []any{"migrations/", "deploy/"},
		"require_security":    []any{"internal/auth/", "migrations/"},
		"require_data_model":  []any{"internal/model/"},
	}}))

	tests := []struct {
		path string
		want []string
	}{
		{"migrations/001.sql", []string{"require_release_ops", "require_security"}},
		{"Migrations/001.SQL", []string{"require_release_ops", "require_security"}}, // TD-09
		{"internal/auth/x.go", []string{"require_security"}},
		{"internal/model/user.go", []string{"require_data_model"}},
		{"deploy", []string{"require_release_ops"}},
		{"README.md", nil},
	}
	for _, tt := range tests {
		got := c.FlagsTriggeredBy(tt.path)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("FlagsTriggeredBy(%q) = %v, quer %v", tt.path, got, tt.want)
		}
	}
}

func TestIsModuleEnabled(t *testing.T) {
	c := loadBody(t, bodyWith(t, map[string]any{
		"enabled_modules": []any{"data-model", "release-ops", "query-performance"},
	}))

	tests := []struct {
		name   string
		module string
		want   bool
	}{
		{"módulo ativo", "data-model", true},
		{"outro ativo", "release-ops", true},
		{"case-insensitive", "Query-Performance", true},
		{"com espaços", "  data-model  ", true},
		{"módulo válido mas não ativo", "migration-reviewer", false},
		{"módulo inexistente", "desconhecido", false},
		{"string vazia", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsModuleEnabled(tt.module); got != tt.want {
				t.Errorf("IsModuleEnabled(%q) = %v, quer %v", tt.module, got, tt.want)
			}
		})
	}
}

func TestIsModuleEnabledSemModulos(t *testing.T) {
	c := loadBody(t, validBody)
	if c.IsModuleEnabled("data-model") {
		t.Errorf("IsModuleEnabled devolveu true sem enabled_modules configurado")
	}
}

func TestErroCitaArquivo(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, bodyWith(t, map[string]any{"database": "mysql"}))
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("erro = %v, quer caminho %q na mensagem", err, path)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(validBody))
	f.Add([]byte(`{"version":1,"project_type":"api","database":"postgres","git_provider":"gitlab",` +
		`"protected_paths":["internal/","docs-ai/"],"no_agent_paths":["docs-ai/restrito/"],` +
		`"auto_flags":{"require_security":["internal/auth/"]},"delivery_id_pattern":"^F-[0-9]+$",` +
		`"sensors":[{"name":"lint","cmd":["make","lint"]}],"report_schema":{"section_level":2}}`))
	f.Add([]byte(`{"version":1,`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"protected_paths":["../x"]}`))
	f.Add([]byte(`{"delivery_id_pattern":"^a|b$"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		c1, err1 := decode(data)
		c2, err2 := decode(data)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("decode não determinístico: %v × %v", err1, err2)
		}
		if err1 != nil {
			if err1.Error() != err2.Error() {
				t.Fatalf("mensagem não determinística: %q × %q", err1, err2)
			}
			return
		}
		if !reflect.DeepEqual(c1, c2) {
			t.Fatal("decode não determinístico")
		}
		if c1.Version != 1 || c1.ProjectType != "api" || c1.Database != "postgres" {
			t.Fatalf("config aceita fora da v1: %+v", c1)
		}
		if len(c1.ProtectedPaths) == 0 || c1.ReportSchema == nil {
			t.Fatalf("config aceita sem protected_paths ou report_schema: %+v", c1)
		}
		for _, p := range c1.ProtectedPaths {
			if strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, `\`) {
				t.Fatalf("protected_paths aceitou %q", p)
			}
		}
		for _, n := range c1.NoAgentPaths {
			if !c1.IsProtected(strings.TrimSuffix(n, "/")) {
				t.Fatalf("no_agent_paths %q fora de protected_paths", n)
			}
		}
	})
}
