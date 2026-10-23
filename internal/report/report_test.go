// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/report/report_test.go @ 0eac73a (MIT).
// Alterações: relatório de exemplo reescrito para o esquema padrão (títulos "## ",
// campos "- Nome: valor") e validado por Check(r, config.ReportSchema) (TD-12); casos de
// flag permissiva, marcadores fixos e seções fixas ajustados (TD-12, ver
// comentários em cada caso); casos novos da Fase 1 (CRLF, blocos de código,
// campo multilinha, esquema inválido, fuzz).

package report

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// fence evita crases dentro de string literal crua.
const fence = "```"

// relatorioOK é um relatório completo e válido no esquema padrão, de um
// domínio fictício (biblioteca de bairro).
const relatorioOK = `---
delivery_id: DEL-001
task_code: TASK-042
title: "Ajuste no cálculo de multa por atraso"
status: done
require_security: false
require_release_ops: false
---

# Relatório da DEL-001

## PM/Triage
- Escopo fechado: cálculo de multa por atraso na devolução de livros
- Critérios de aceite:
  - multa zero dentro do prazo
  - multa proporcional aos dias de atraso
- Riscos: arredondamento de centavos

## Architecture/Guardrails
- Invariantes aplicáveis: INV-01 valores monetários em centavos
- Arquivos prováveis de impacto: internal/emprestimo/multa.go

## Dev
- Arquivos alterados: internal/emprestimo/multa.go, internal/emprestimo/multa_test.go
- Resumo técnico: Table-driven cobrindo 6 casos.
- Testes criados/ajustados: TestMulta
- Evidências (código/teste): go test ./internal/emprestimo/ ok

### Notas
Arredondamento feito com divisão inteira.

` + fence + `go
## Isto não é título
// TODO dentro de bloco de código não reprova
- [ ] item dentro de bloco de código não é checklist
` + fence + `

## QA
- Resultado: aprovado
- Cenários executados: devolução no prazo, com 1 e com 30 dias de atraso

## Docs
- Docs atualizados: docs/emprestimo.md

## Final Checklist
- [x] build passa
- [x] testes passam
- [x] vet limpo
`

func mustParse(t *testing.T, content string) *Report {
	t.Helper()
	r, err := Parse([]byte(content), config.DefaultReportSchema().SectionLevel)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func mustCheck(t *testing.T, content string) []Finding {
	t.Helper()
	fs, err := Check(mustParse(t, content), config.DefaultReportSchema())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return fs
}

func hasError(fs []Finding, substr string) bool {
	for _, f := range fs {
		if f.Level == LevelError && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("texto %q não encontrado no relatório de teste", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func TestRelatorioValido(t *testing.T) {
	if fs := mustCheck(t, relatorioOK); len(fs) != 0 {
		t.Errorf("esperava zero findings, obteve %d: %v", len(fs), fs)
	}
}

func TestRelatorioValidoCRLF(t *testing.T) {
	content := strings.ReplaceAll(relatorioOK, "\n", "\r\n")
	r := mustParse(t, content)
	if got := r.Front["title"]; got != "Ajuste no cálculo de multa por atraso" {
		t.Errorf("front title = %q", got)
	}
	f, ok := r.Field("PM/Triage", "Criterios de aceite")
	if !ok || f.Value != "multa zero dentro do prazo\nmulta proporcional aos dias de atraso" {
		t.Errorf("campo multilinha com CRLF = %q, %v", f.Value, ok)
	}
	fs, err := Check(r, config.DefaultReportSchema())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("CRLF: esperava zero findings, obteve %v", fs)
	}
}

func TestParseErros(t *testing.T) {
	tests := []struct {
		name    string
		content string
		level   int
	}{
		// Casos originais.
		{"sem front matter", "# Contexto\ntexto", 2},
		{"front matter não fechado", "---\ndelivery_id: X\n# Contexto", 2},
		// Casos novos (falha fechada, TD-08/TD-12).
		{"vazio", "", 2},
		{"front matter não fechado no fim", "---\ndelivery_id: X\n", 2},
		{"fechamento com sufixo não fecha", "---\ndelivery_id: X\n--- extra\n## QA\n", 2},
		{"linha sem dois-pontos no front", "---\ndelivery_id X\n---\n", 2},
		{"chave vazia no front", "---\n: valor\n---\n", 2},
		{"chave duplicada no front", "---\nstatus: done\nSTATUS: wip\n---\n", 2},
		{"bloco de código não fechado", "---\nstatus: done\n---\n## QA\n" + fence + "\n## Docs\n", 2},
		{"nível 0", "---\nstatus: done\n---\n", 0},
		{"nível 7", "---\nstatus: done\n---\n", 7},
		{"nível negativo", "---\nstatus: done\n---\n", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.content), tt.level); err == nil {
				t.Error("esperava erro de parse, obteve nil")
			}
		})
	}
}

func TestParseFrontMatter(t *testing.T) {
	content := "\ufeff---\r\n" +
		"Delivery_ID: 'DEL-007'\r\n" +
		"# comentário ignorado\r\n" +
		"\r\n" +
		"title: \"Título: com dois-pontos\"\r\n" +
		"aspas_desbalanceadas: \"meio'\r\n" +
		"vazio:\r\n" +
		"---\r\n" +
		"## QA\r\n"
	r, err := Parse([]byte(content), 2)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]string{
		"delivery_id":          "DEL-007",
		"title":                "Título: com dois-pontos",
		"aspas_desbalanceadas": `"meio'`,
		"vazio":                "",
	}
	if !reflect.DeepEqual(r.Front, want) {
		t.Errorf("Front = %#v, quer %#v", r.Front, want)
	}
}

func TestParseSecoes(t *testing.T) {
	r := mustParse(t, relatorioOK)
	var keys []string
	for _, s := range r.Sections {
		keys = append(keys, s.Key)
	}
	want := []string{"pm/triage", "architecture/guardrails", "dev", "qa", "docs", "final checklist"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("seções = %q, quer %q", keys, want)
	}

	// "### Notas" fica dentro de Dev; "## " dentro do bloco de código não abre seção.
	dev, ok := r.Section("dev")
	if !ok {
		t.Fatal("seção Dev não encontrada")
	}
	for _, want := range []string{"### Notas", "## Isto não é título"} {
		if !strings.Contains(dev.Body, want) {
			t.Errorf("corpo de Dev não contém %q:\n%s", want, dev.Body)
		}
	}
	if _, ok := r.Section("Isto não é título"); ok {
		t.Error("título dentro de bloco de código abriu seção")
	}
	if _, ok := r.Section("Notas"); ok {
		t.Error("### abriu seção com section_level 2")
	}
	// "# Relatório" (nível acima) não abre seção.
	if _, ok := r.Section("Relatório da DEL-001"); ok {
		t.Error("# abriu seção com section_level 2")
	}
}

func TestParseNivelConfiguravel(t *testing.T) {
	content := "---\nstatus: done\n---\n## Grupo\n### QA\n- Resultado: ok\n#### Detalhe\ntexto\n## Outro\n### Docs\n"
	r, err := Parse([]byte(content), 3)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(r.Sections) != 2 || r.Sections[0].Key != "qa" || r.Sections[1].Key != "docs" {
		t.Fatalf("seções = %+v", r.Sections)
	}
	// "## Outro" (nível acima) fecha a seção QA sem abrir outra.
	if strings.Contains(r.Sections[0].Body, "Outro") {
		t.Errorf("título de nível acima não fechou a seção: %q", r.Sections[0].Body)
	}
	if !strings.Contains(r.Sections[0].Body, "#### Detalhe") {
		t.Errorf("título de nível abaixo deveria ficar no corpo: %q", r.Sections[0].Body)
	}
}

func TestParseTitulosComAcento(t *testing.T) {
	content := "---\nstatus: done\n---\n## Implementação:\n- Validação.: ok\n## Revisão de Código\ntexto\n"
	r, err := Parse([]byte(content), 2)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := r.Section("IMPLEMENTACAO"); !ok {
		t.Error("Implementação: não casou com IMPLEMENTACAO")
	}
	if _, ok := r.Section("revisao de codigo."); !ok {
		t.Error("Revisão de Código não casou com revisao de codigo.")
	}
	if f, ok := r.Field("Implementacao", "validacao"); !ok || f.Value != "ok" {
		t.Errorf("campo com acento = %+v, %v", f, ok)
	}

	s := config.ReportSchema{
		SectionLevel:     2,
		SectionsRequired: []string{"Implementacao", "Revisão de código"},
		FieldsRequired:   map[string][]string{"implementação": {"Validacao"}},
	}
	fs, err := Check(r, s)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("esperava zero findings, obteve %v", fs)
	}
}

func TestParseCampoMultilinha(t *testing.T) {
	content := "---\nstatus: done\n---\n## Dev\n" +
		"- Arquivos alterados:\n" +
		"  - a.go\n" +
		"\t- b.go\n" +
		"    continuação de b\n" +
		"- Resumo: linha única\n" +
		"  - sub-item ignorado porque o valor não é vazio\n" +
		"- Vazio na linha:\n" +
		"\n" +
		"  - depois da linha em branco não conta\n" +
		"- Até o próximo item:\n" +
		"- Último:\n" +
		"  - x\n" +
		"## QA\n" +
		"  - não pertence a Dev\n"
	r, err := Parse([]byte(content), 2)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tests := []struct {
		name string
		want string
	}{
		{"Arquivos alterados", "a.go\nb.go\ncontinuação de b"},
		{"Resumo", "linha única"},
		{"Vazio na linha", ""},
		{"Até o próximo item", ""},
		{"Último", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := r.Field("Dev", tt.name)
			if !ok {
				t.Fatalf("campo %q não encontrado", tt.name)
			}
			if f.Value != tt.want {
				t.Errorf("valor = %q, quer %q", f.Value, tt.want)
			}
		})
	}
}

// TestCheck tem um caso por checagem do esquema padrão.
func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		querErro string
	}{
		{"front obrigatório ausente", "status: done\n", "", `"status"`},
		{"front obrigatório vazio", "status: done\n", "status:\n", `"status"`},
		{"front obrigatório com placeholder", "delivery_id: DEL-001\n", "delivery_id: <delivery_id>\n", `"delivery_id"`},
		{"seção obrigatória ausente", "## Docs\n- Docs atualizados: docs/emprestimo.md\n", "", `"Docs"`},
		{"seção obrigatória vazia", "- Docs atualizados: docs/emprestimo.md\n", "\n", `"Docs"`},
		{"campo obrigatório ausente", "- Riscos: arredondamento de centavos\n", "", `"Riscos"`},
		{"campo obrigatório vazio", "- Riscos: arredondamento de centavos\n", "- Riscos:\n", `"Riscos"`},
		{"campo com placeholder_values", "- Resultado: aprovado", "- Resultado: N / A", `"Resultado"`},
		{"campo com placeholder sem caixa", "- Resultado: aprovado", "- Resultado: Pendente", `"Resultado"`},
		{"campo com <...>", "- Docs atualizados: docs/emprestimo.md", "- Docs atualizados: <caminho do doc>", `"Docs atualizados"`},
		{"campo multilinha com <...>", "  - multa zero dentro do prazo\n", "  - <criterio>\n", `"Criterios de aceite"`},
		{"campo multilinha sem sub-itens vira vazio", "- Critérios de aceite:\n  - multa zero dentro do prazo\n  - multa proporcional aos dias de atraso\n", "- Critérios de aceite:\n", `"Criterios de aceite"`},
		{"campo duplicado com placeholder", "- Riscos: arredondamento de centavos\n", "- Riscos: arredondamento de centavos\n- Riscos: tbd\n", `"Riscos"`},
		{"seção obrigatória duplicada", "## QA\n", "## QA\n- Resultado: aprovado\n\n## QA\n", "duplicada"},
		{"flag true sem seção", "require_security: false", "require_security: true", `"Security"`},
		{"segunda flag true sem seção", "require_release_ops: false", "require_release_ops: true", `"Release/Ops"`},
		{"flag inválida", "require_security: false", "require_security: sim", "require_security"},
		{"flag fora do esquema inválida", "require_security: false", "require_security: false\nrequire_migration: 1", "require_migration"},
		{"checklist com item aberto", "- [x] vet limpo", "- [ ] vet limpo", "não concluído"},
		{"checklist com item aberto indentado", "- [x] vet limpo", "  * [ ] vet limpo", "não concluído"},
		{"checklist sem itens", "- [x] build passa\n- [x] testes passam\n- [x] vet limpo\n", "tudo certo\n", "nenhum item"},
		{"marcador no corpo", "Arredondamento feito com divisão inteira.", "Arredondamento FIXME.", `"FIXME"`},
		{"marcador no front", "status: done", "status: done TODO", `"TODO"`},
		{"marcador no título", "### Notas", "### Notas XXX", `"XXX"`},
		{"marcador fora de seção", "# Relatório da DEL-001", "# Relatório TBD", `"TBD"`},
		{"marcador colado em pontuação", "Arredondamento feito com divisão inteira.", "Arredondamento (TODO).", `"TODO"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := mustCheck(t, replaceOnce(t, relatorioOK, tt.old, tt.new))
			if !hasError(fs, tt.querErro) {
				t.Errorf("esperava erro contendo %q, obteve: %v", tt.querErro, fs)
			}
		})
	}
}

func TestCheckFlagComSecaoCompleta(t *testing.T) {
	content := replaceOnce(t, relatorioOK, "require_security: false", "require_security: true") +
		"\n## Security\n- Resultado: govulncheck limpo\n"
	if fs := mustCheck(t, content); len(fs) != 0 {
		t.Errorf("esperava zero findings, obteve %v", fs)
	}
}

func TestCheckFlagAusente(t *testing.T) {
	// Flag ausente no front vale false: a seção condicional não é exigida.
	content := replaceOnce(t, relatorioOK, "require_security: false\n", "")
	if fs := mustCheck(t, content); len(fs) != 0 {
		t.Errorf("esperava zero findings, obteve %v", fs)
	}
}

func TestCheckSecaoDuplicadaForaDoEsquema(t *testing.T) {
	// Seção que o esquema não usa pode se repetir.
	content := relatorioOK + "\n## Notas\nprimeira\n\n## Notas\nsegunda\n"
	if fs := mustCheck(t, content); len(fs) != 0 {
		t.Errorf("esperava zero findings, obteve %v", fs)
	}
}

func TestCheckErros(t *testing.T) {
	r := mustParse(t, relatorioOK)
	invalid := []struct {
		name string
		mut  func(*config.ReportSchema)
	}{
		{"section_level 0", func(s *config.ReportSchema) { s.SectionLevel = 0 }},
		{"section_level 7", func(s *config.ReportSchema) { s.SectionLevel = 7 }},
		{"front_required vazio", func(s *config.ReportSchema) { s.FrontRequired = append(s.FrontRequired, " ") }},
		{"sections_required vazio", func(s *config.ReportSchema) { s.SectionsRequired = append(s.SectionsRequired, ":.") }},
		{"fields_required seção vazia", func(s *config.ReportSchema) { s.FieldsRequired[""] = []string{"x"} }},
		{"fields_required campo vazio", func(s *config.ReportSchema) { s.FieldsRequired["QA"] = append(s.FieldsRequired["QA"], "") }},
		{"flag_sections chave inválida", func(s *config.ReportSchema) {
			s.FlagSections["Require Security"] = config.FlagSection{Section: "Security"}
		}},
		{"flag_sections seção vazia", func(s *config.ReportSchema) { s.FlagSections["require_x"] = config.FlagSection{Fields: []string{"a"}} }},
		{"flag_sections campo vazio", func(s *config.ReportSchema) {
			s.FlagSections["require_x"] = config.FlagSection{Section: "X", Fields: []string{" "}}
		}},
		{"forbid_markers vazio", func(s *config.ReportSchema) { s.ForbidMarkers = append(s.ForbidMarkers, "") }},
		{"forbid_markers com espaço nas pontas", func(s *config.ReportSchema) { s.ForbidMarkers = append(s.ForbidMarkers, " TODO") }},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			s := config.DefaultReportSchema()
			tt.mut(&s)
			if _, err := Check(r, s); err == nil {
				t.Error("Check: esperava erro com esquema inválido, obteve nil")
			}
		})
	}

	t.Run("esquema zerado", func(t *testing.T) {
		if _, err := Check(r, config.ReportSchema{}); err == nil {
			t.Error("esperava erro, obteve nil")
		}
	})
	t.Run("nível do report diferente do esquema", func(t *testing.T) {
		s := config.DefaultReportSchema()
		s.SectionLevel = 3
		if _, err := Check(r, s); err == nil {
			t.Error("esperava erro, obteve nil")
		}
	})
	t.Run("report nil", func(t *testing.T) {
		if _, err := Check(nil, config.DefaultReportSchema()); err == nil {
			t.Error("esperava erro, obteve nil")
		}
	})
}

// Casos originais, ajustados ao esquema padrão.

func TestFrontMatterObrigatorio(t *testing.T) {
	// Original: remover task_code reprovava, porque os campos obrigatórios eram
	// fixos no código. Agora vêm de front_required (TD-12): no padrão, task_code
	// é opcional; declarado no esquema, volta a ser exigido.
	content := replaceOnce(t, relatorioOK, "task_code: TASK-042\n", "")
	if fs := mustCheck(t, content); hasError(fs, "task_code") {
		t.Errorf("task_code não está no esquema padrão, mas reprovou: %v", fs)
	}
	s := config.DefaultReportSchema()
	s.FrontRequired = append(s.FrontRequired, "task_code")
	fs, err := Check(mustParse(t, content), s)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !hasError(fs, "task_code") {
		t.Error("esperava erro sobre task_code ausente")
	}
}

func TestSecaoObrigatoriaAusente(t *testing.T) {
	// Original removia "Code Review"; as seções agora vêm de sections_required
	// (TD-12) e "QA" faz o papel equivalente no padrão.
	content := replaceOnce(t, relatorioOK, "## QA\n", "")
	if !hasError(mustCheck(t, content), `"QA"`) {
		t.Error("esperava erro sobre a seção QA")
	}
}

// TestPlaceholderParcial fixa a divergência 2: a versão em bash só rejeitava
// quando o valor inteiro era TODO. Aqui o marcador aparece no meio da frase.
func TestPlaceholderParcial(t *testing.T) {
	content := replaceOnce(t,
		relatorioOK,
		"Table-driven cobrindo 6 casos.",
		"Table-driven cobrindo 6 casos. TODO: falta o caso de expiração.",
	)
	if fs := mustCheck(t, content); !hasError(fs, "TODO") {
		t.Errorf("esperava erro sobre marcador TODO, obteve: %v", fs)
	}
}

// TestPortuguesNaoEhMarcador fixa o falso positivo que a primeira versão tinha:
// a detecção por substring reprovava "todos" (contém "todo") e "independente"
// (contém "pendente"), inviabilizando qualquer relatório em português.
func TestPortuguesNaoEhMarcador(t *testing.T) {
	frases := []string{
		"Todos os testes passaram.",
		"O módulo é independente do storage.",
		"Cobre todo o fluxo de autenticação.",
		"Nenhum item pendente no checklist.",
	}
	for _, frase := range frases {
		t.Run(frase, func(t *testing.T) {
			content := replaceOnce(t, relatorioOK, "Table-driven cobrindo 6 casos.", frase)
			if fs := mustCheck(t, content); len(fs) != 0 {
				t.Errorf("texto português legítimo foi reprovado: %v", fs)
			}
		})
	}
}

func TestMarcadoresDeCodigo(t *testing.T) {
	tests := []struct {
		frase string
		erro  bool
	}{
		{"Falta cobrir expiração. FIXME antes do merge.", true},
		{"Prazo TBD com o time.", true},
		{"Deixado como XXX por ora.", true},
		// Os três casos abaixo reprovavam no original (WIP e frases de
		// preenchimento fixas no código). Agora os marcadores vêm de
		// forbid_markers (TD-12), e o padrão não os inclui.
		{"Ainda WIP.", false},
		{"Seção a definir.", false},
		{"Lorem ipsum dolor.", false},
		{"O fix me pareceu correto.", false},
		{"Rodou em ambiente Windows XP.", false},
		{"Comentário todo em minúsculas.", false},
	}
	for _, tt := range tests {
		t.Run(tt.frase, func(t *testing.T) {
			content := replaceOnce(t, relatorioOK, "Table-driven cobrindo 6 casos.", tt.frase)
			fs := mustCheck(t, content)
			if tt.erro && len(fs) == 0 {
				t.Error("esperava reprovação, passou")
			}
			if !tt.erro && len(fs) != 0 {
				t.Errorf("esperava aprovação, reprovou: %v", fs)
			}
		})
	}
}

func TestMarcadoresDeclarados(t *testing.T) {
	// Com o esquema declarando, WIP e frases voltam a reprovar (TD-12).
	s := config.DefaultReportSchema()
	s.ForbidMarkers = append(s.ForbidMarkers, "WIP", "a definir")
	for _, frase := range []string{"Ainda WIP.", "Seção a definir."} {
		t.Run(frase, func(t *testing.T) {
			r := mustParse(t, replaceOnce(t, relatorioOK, "Table-driven cobrindo 6 casos.", frase))
			fs, err := Check(r, s)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if len(fs) == 0 {
				t.Error("esperava reprovação, passou")
			}
		})
	}
}

func TestChecklistIncompleto(t *testing.T) {
	content := replaceOnce(t, relatorioOK, "- [x] vet limpo", "- [ ] vet limpo")
	if !hasError(mustCheck(t, content), "não concluído") {
		t.Error("esperava erro sobre item não concluído")
	}
}

func TestFlagsCondicionais(t *testing.T) {
	tests := []struct {
		name     string
		front    string
		secao    string
		querErro string
	}{
		{
			name:     "security ligado sem seção",
			front:    "require_security: true",
			secao:    "",
			querErro: `seção "Security"`,
		},
		{
			// Original: corpo "N/A" reprovava por isNA. Agora a seção exige o
			// campo Resultado e "n/a" está em placeholder_values (TD-12).
			name:     "security ligado com N/A",
			front:    "require_security: true",
			secao:    "\n## Security\n- Resultado: N/A\n",
			querErro: `"Resultado"`,
		},
		{
			// Original: texto livre bastava. Agora o campo é exigido (TD-12).
			name:     "security ligado com texto livre sem campo",
			front:    "require_security: true",
			secao:    "\n## Security\ngovulncheck limpo; sem novos símbolos vulneráveis.\n",
			querErro: `"Resultado"`,
		},
		{
			name:     "security ligado e preenchido",
			front:    "require_security: true",
			secao:    "\n## Security\n- Resultado: govulncheck limpo; sem novos símbolos vulneráveis.\n",
			querErro: "",
		},
		{
			name:     "security desligado sem seção",
			front:    "require_security: false",
			secao:    "",
			querErro: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := replaceOnce(t, relatorioOK, "require_security: false", tt.front) + tt.secao
			fs := mustCheck(t, content)
			if tt.querErro == "" {
				if len(fs) != 0 {
					t.Errorf("esperava zero findings, obteve: %v", fs)
				}
				return
			}
			if !hasError(fs, tt.querErro) {
				t.Errorf("esperava erro contendo %q, obteve: %v", tt.querErro, fs)
			}
		})
	}
}

func TestFlag(t *testing.T) {
	// Original aceitava TRUE, sim e 1 como true e tratava qualquer outro valor
	// como false. Agora o booleano é estrito: só "true" e "false"; o resto é
	// erro (TD-12, falha fechada TD-08).
	tests := []struct {
		valor   string
		want    bool
		wantErr bool
	}{
		{"true", true, false},
		{"TRUE", false, true},
		{"sim", false, true},
		{"1", false, true},
		{"false", false, false},
		{"", false, true},
		{"talvez", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.valor, func(t *testing.T) {
			r := &Report{Front: map[string]string{"require_security": tt.valor}}
			got, err := r.Flag("require_security")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Flag(%q) erro = %v, quer erro %v", tt.valor, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Flag(%q) = %v, quer %v", tt.valor, got, tt.want)
			}
		})
	}
}

func TestFlagAusenteEhFalse(t *testing.T) {
	r := &Report{Front: map[string]string{}}
	got, err := r.Flag("require_security")
	if err != nil || got {
		t.Errorf("flag ausente deve valer false sem erro (D-14), obteve %v, %v", got, err)
	}
}

func TestNormalizeTitulos(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// Casos originais.
		{"Implementação", "implementacao"},
		{"# Code Review", "code review"},
		{"  TESTES  ", "testes"},
		{"Migração:", "migracao"},
		// Casos novos.
		{"Evidências (código/teste)", "evidencias (codigo/teste)"},
		{"ÀÉÎÕÜ Ñ Ç", "aeiou n c"},
		{"Cena\u0301rios", "cenarios"}, // acento combinante (forma decomposta)
		{"PM/Triage.", "pm/triage"},
		{"Final   Checklist", "final checklist"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := NormalizeTitle(strings.TrimLeft(tt.in, "# ")); got != tt.want {
				t.Errorf("NormalizeTitle(%q) = %q, quer %q", tt.in, got, tt.want)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		relatorioOK,
		strings.ReplaceAll(relatorioOK, "\n", "\r\n"),
		"",
		"---\n---\n",
		"---\nstatus: done\n---\n## A\n- B:\n  - c\n" + fence + "\n## D\n" + fence + "\n",
		"---\nrequire_security: talvez\n---\n## Security\n- [ ] x\n",
		"---\n\"\": '\n---\n##\n- :\n",
		"\ufeff---\r\nk: \"v\"\r\n---\r\n~~~\r\n## x\r\n~~~\r\n",
	}
	for _, s := range seeds {
		for _, level := range []int{2, 1, 3, 0, 7} {
			f.Add([]byte(s), level)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, level int) {
		r1, err1 := Parse(data, level)
		r2, err2 := Parse(data, level)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("Parse não determinístico: %v × %v", err1, err2)
		}
		if err1 != nil {
			if err1.Error() != err2.Error() {
				t.Fatalf("erro não determinístico: %v × %v", err1, err2)
			}
			return
		}
		if !reflect.DeepEqual(r1, r2) {
			t.Fatal("Parse não determinístico")
		}
		s := config.DefaultReportSchema()
		s.SectionLevel = level
		f1, e1 := Check(r1, s)
		f2, e2 := Check(r2, s)
		if (e1 == nil) != (e2 == nil) || !reflect.DeepEqual(f1, f2) {
			t.Fatalf("Check não determinístico: %v %v × %v %v", f1, e1, f2, e2)
		}
	})
}
