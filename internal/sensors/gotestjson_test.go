package sensors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// Os arquivos em testdata/gotestjson são saídas reais de `go test -json` de um
// módulo fictício (example.com/biblioteca):
//   - misto.jsonl: acervo (pass, skip, subteste pulado), emprestimo (um teste
//     falha) e vazio (sem arquivos de teste); exit 1;
//   - pulados.jsonl: só acervo e vazio; exit 0;
//   - build.jsonl: pacote que não compila; exit 1.
func goTestData(t testing.TB, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "gotestjson", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func readGoTestData(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(goTestData(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseGoTestJSON(t *testing.T) {
	cases := []struct {
		file                            string
		packages, pkgFailed, pkgNoTests int
		passed, failed, skipped         int
		has, not, order                 []string
	}{
		{
			file: "misto.jsonl", packages: 3, pkgFailed: 1, pkgNoTests: 1,
			passed: 4, failed: 1, skipped: 2,
			has: []string{
				"pacotes: 3 (1 ok, 1 com falha, 1 sem testes)",
				"testes: 4 passaram, 1 falhou, 2 pulados",
				// Pulados agrupados por pacote e motivo, sem arquivo:linha.
				"pulados, por pacote e motivo:\n" +
					"  1. example.com/biblioteca/acervo, 1 pulado: autor ainda não indexado\n" +
					"  2. example.com/biblioteca/acervo, 1 pulado: sem BIBLIOTECA_DB_URL\n",
				"nomes dos pulados:\n  1. TestSub/autor\n  2. TestBanco\n",
				"--- FAIL: TestPrazo (0.00s)",
				"emprestimo_test.go:6: calculando prazo",
				"emprestimo_test.go:7: prazo = 7, esperado 14",
				"example.com/biblioteca/emprestimo",
			},
			// Teste que passou não aparece; molduras de execução também não.
			not: []string{"TestCatalogo", "TestDevolucao", "=== RUN", "TestSub/titulo", "--- SKIP", "acervo_test.go:8"},
			// Falha vem antes dos pulados: o corte nunca a esconde atrás deles.
			order: []string{"falhas:", "pulados, por pacote e motivo:"},
		},
		{
			file: "pulados.jsonl", packages: 2, pkgNoTests: 1,
			passed: 3, skipped: 2,
			has: []string{"pacotes: 2 (1 ok, 0 com falha, 1 sem testes)", "testes: 3 passaram, 0 falharam, 2 pulados", "sem BIBLIOTECA_DB_URL"},
			not: []string{"falhas:"},
		},
		{
			file: "build.jsonl", packages: 1, pkgFailed: 1,
			has: []string{
				"pacotes: 1 (0 ok, 1 com falha, 0 sem testes)",
				"example.com/biblioteca/quebrado",
				"cannot use \"x\" (untyped string constant) as int value",
				"[build failed]",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			s, err := parseGoTestJSON(bytes.NewReader(readGoTestData(t, tc.file)))
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			got := [6]int{s.Packages, s.PackagesFailed, s.PackagesNoTests, s.Passed, s.Failed, s.Skipped}
			want := [6]int{tc.packages, tc.pkgFailed, tc.pkgNoTests, tc.passed, tc.failed, tc.skipped}
			if got != want {
				t.Errorf("contagens (pacotes, falha, sem testes, passaram, falharam, pulados) = %v, quer %v", got, want)
			}
			for _, h := range tc.has {
				if !strings.Contains(s.Text, h) {
					t.Errorf("resumo sem %q:\n%s", h, s.Text)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(s.Text, n) {
					t.Errorf("resumo com %q:\n%s", n, s.Text)
				}
			}
			rest := s.Text
			for _, o := range tc.order {
				i := strings.Index(rest, o)
				if i < 0 {
					t.Errorf("resumo sem %q depois do anterior (ordem %q):\n%s", o, tc.order, s.Text)
					break
				}
				rest = rest[i+len(o):]
			}
			if s.Truncated {
				t.Error("resumo truncado sem motivo")
			}
		})
	}
}

// TestParseGoTestJSONPuladosAgrupados: o caso da DEL-011 da demo. Muitos
// testes pulados pelo mesmo motivo (com arquivo:linha diferentes) cabem no
// resumo: um grupo por pacote e motivo, todos os nomes listados, e a falha
// aparece antes deles.
func TestParseGoTestJSONPuladosAgrupados(t *testing.T) {
	const motivo = "BIBLIOTECA_TEST_DATABASE_URL não definida: teste de banco pulado"
	var b strings.Builder
	enc := json.NewEncoder(&b)
	ev := func(action, pkg, test, output string) {
		e := map[string]string{"Action": action, "Package": "example.com/biblioteca/" + pkg}
		if test != "" {
			e["Test"] = test
		}
		if output != "" {
			e["Output"] = output
		}
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	skip := func(pkg, test, file string, line int, reason string) {
		ev("output", pkg, test, fmt.Sprintf("    %s:%d: %s\n", file, line, reason))
		ev("output", pkg, test, "--- SKIP: "+test+" (0.00s)\n")
		ev("skip", pkg, test, "")
	}
	var names []string
	for _, g := range []struct {
		pkg, file, reason string
		n                 int
	}{
		{"storage", "repositorio_test.go", motivo, 150},
		{"migrate", "controle_banco_test.go", motivo, 30},
		{"cmd", "migrate_correcao_test.go", motivo, 5},
		{"cmd", "pool_test.go", "sem docker", 3},
	} {
		ev("start", g.pkg, "", "")
		for i := range g.n {
			name := fmt.Sprintf("Test%sBanco%03d/sub_%d", strings.ToUpper(g.pkg[:1])+g.pkg[1:], i, i%3)
			names = append(names, name)
			skip(g.pkg, name, g.file, 100+i, g.reason)
		}
	}
	ev("output", "storage", "TestPrazo", "    prazo_test.go:7: prazo = 7, esperado 14\n")
	ev("output", "storage", "TestPrazo", "--- FAIL: TestPrazo (0.00s)\n")
	ev("fail", "storage", "TestPrazo", "")
	for _, pkg := range []string{"storage", "migrate", "cmd"} {
		action := "pass"
		if pkg == "storage" {
			action = "fail"
		}
		ev(action, pkg, "", "")
	}

	s, err := parseGoTestJSON(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Skipped != 188 || s.Failed != 1 {
		t.Errorf("pulados = %d, falhas = %d", s.Skipped, s.Failed)
	}
	if s.Truncated {
		t.Fatalf("resumo truncado com %d bytes:\n%s", len(s.Text), s.Text)
	}
	for _, want := range []string{
		"testes: 0 passaram, 1 falhou, 188 pulados",
		"prazo = 7, esperado 14",
		"  1. example.com/biblioteca/cmd, 5 pulados: " + motivo + "\n",
		"  2. example.com/biblioteca/cmd, 3 pulados: sem docker\n",
		"  3. example.com/biblioteca/migrate, 30 pulados: " + motivo + "\n",
		"  4. example.com/biblioteca/storage, 150 pulados: " + motivo + "\n",
	} {
		if !strings.Contains(s.Text, want) {
			t.Errorf("resumo sem %q:\n%s", want, s.Text)
		}
	}
	if n := strings.Count(s.Text, motivo); n != 3 {
		t.Errorf("motivo repetido %d vezes, quer 3 (um por grupo)", n)
	}
	for _, name := range names {
		if !strings.Contains(s.Text, name) {
			t.Errorf("pulado %s fora do resumo", name)
		}
	}
	if i, j := strings.Index(s.Text, "falhas:"), strings.Index(s.Text, "pulados, por pacote e motivo:"); i < 0 || j < i {
		t.Errorf("falhas (%d) não vêm antes dos pulados (%d)", i, j)
	}
	for _, l := range strings.Split(s.Text, "\n") {
		if len(l) > 120 {
			t.Errorf("linha com %d bytes, quer até 120: %q", len(l), l)
		}
	}
}

// TestParseGoTestJSONGruposAntesDosNomes: o caso da demo na rc.2. Nomes de
// subteste longos passam do limite; o corte come os nomes, nunca um grupo:
// todos os grupos vêm antes de qualquer nome.
func TestParseGoTestJSONGruposAntesDosNomes(t *testing.T) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	ev := func(action, pkg, test, output string) {
		e := map[string]string{"Action": action, "Package": "example.com/biblioteca/" + pkg, "Test": test, "Output": output}
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, pkg := range []string{"acervo", "migrate", "zzz"} {
		n := 2
		if pkg == "migrate" {
			n = 400
		}
		for i := range n {
			name := fmt.Sprintf("TestControleAdotadoComArmadilhaFalhaFechado/caso_com_nome_comprido_%03d/Subir", i)
			ev("output", pkg, name, "    controle_test.go:9: sem banco\n")
			ev("skip", pkg, name, "")
		}
	}
	s, err := parseGoTestJSON(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Truncated {
		t.Fatalf("quero corte nos nomes; resumo com %d bytes", len(s.Text))
	}
	head, names, ok := strings.Cut(s.Text, "nomes dos pulados:\n")
	if !ok {
		t.Fatalf("sem a lista de nomes:\n%.600s", s.Text)
	}
	for _, want := range []string{
		"  1. example.com/biblioteca/acervo, 2 pulados: sem banco\n",
		"  2. example.com/biblioteca/migrate, 400 pulados: sem banco\n",
		"  3. example.com/biblioteca/zzz, 2 pulados: sem banco\n",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("grupo fora da parte antes dos nomes: %q", want)
		}
	}
	if strings.Contains(head, "TestControle") {
		t.Error("nome de teste antes da lista de nomes")
	}
	if !strings.HasPrefix(names, "  1. TestControle") {
		t.Errorf("nomes não começam pelo grupo 1: %.120q", names)
	}
}

// TestCapTextCortaEmLinha: o corte do resumo cai no fim de uma linha, nunca
// no meio dela.
func TestCapTextCortaEmLinha(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() <= OutputLimit; i++ {
		fmt.Fprintf(&b, "linha %05d com algum texto para encher\n", i)
	}
	got, cut := capText(b.String())
	if !cut || len(got) > OutputLimit {
		t.Fatalf("cut = %v, len = %d", cut, len(got))
	}
	body, ok := strings.CutSuffix(got, truncatedMark)
	if !ok {
		t.Fatalf("sem a marca de corte no fim: %q", got[len(got)-80:])
	}
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasSuffix(l, "para encher") {
			t.Fatalf("linha cortada no meio: %q", l)
		}
	}

	// Uma linha única maior que o limite ainda é cortada, em UTF-8 válido.
	got, cut = capText(strings.Repeat("ç", OutputLimit))
	if !cut || len(got) > OutputLimit || !utf8.ValidString(got) {
		t.Errorf("linha única: cut = %v, len = %d, utf8 = %v", cut, len(got), utf8.ValidString(got))
	}
}

// TestParseGoTestJSONPedacos: a saída chega ao parser em pedaços arbitrários
// (pipe); o resultado não depende do corte.
func TestParseGoTestJSONPedacos(t *testing.T) {
	data := readGoTestData(t, "misto.jsonl")
	whole, err := parseGoTestJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 7, 64, 1000} {
		p := newGoTestParser()
		for rest := data; len(rest) > 0; {
			n := min(size, len(rest))
			if k, err := p.Write(rest[:n]); k != n || err != nil {
				t.Fatalf("Write = %d, %v", k, err)
			}
			rest = rest[n:]
		}
		got, err := p.finish()
		if err != nil {
			t.Fatalf("pedaços de %d: %v", size, err)
		}
		if got != whole {
			t.Errorf("pedaços de %d: resumo diferente:\n%s\n--- inteiro:\n%s", size, got.Text, whole.Text)
		}
	}
}

func TestParseGoTestJSONIlegivel(t *testing.T) {
	ok := `{"Action":"start","Package":"example.com/biblioteca/acervo"}` + "\n"
	cases := []struct {
		name, in, wantErr string
	}{
		{"linha que não é JSON", ok + "ok  \texample.com/biblioteca/acervo\t0.01s\n", "linha 2"},
		{"JSON sem Action", ok + `{"Package":"example.com/biblioteca/acervo"}` + "\n", "linha 2: evento sem Action"},
		{"Action de tipo errado", `{"Action":1}` + "\n", "linha 1"},
		{"null", "null\n", "linha 1: evento sem Action"},
		{"lixo depois do objeto", ok + `{"Action":"pass"} x` + "\n", "linha 2"},
		{"última linha sem quebra e ilegível", ok + `{"Action":`, "linha 2"},
		{"linha vazia", ok + "\n", "linha 2"},
		{"saída vazia", "", "nenhum evento"},
		{"linha acima do limite", ok + `{"Action":"output","Output":"` + strings.Repeat("x", maxEventLine) + `"}` + "\n", "linha 2: acima de"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGoTestJSON(strings.NewReader(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("erro = %v, quer trecho %q", err, tc.wantErr)
			}
		})
	}
}

// TestParseGoTestJSONLimites: muitos testes pulados e falhas longas não
// estouram o resumo; o corte é declarado.
func TestParseGoTestJSONLimites(t *testing.T) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	ev := func(action, test, output string) {
		e := map[string]string{"Action": action, "Package": "example.com/biblioteca/acervo"}
		if test != "" {
			e["Test"] = test
		}
		if output != "" {
			e["Output"] = output
		}
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	ev("start", "", "")
	for i := range 500 {
		name := "TestPulado" + strings.Repeat("x", i%7) + string(rune('A'+i%26))
		ev("output", name, "    acervo_test.go:9: sem BIBLIOTECA_DB_URL\n")
		ev("output", name, "--- SKIP: "+name+" (0.00s)\n")
		ev("skip", name, "")
	}
	for range 50 {
		ev("output", "TestFalha", strings.Repeat("prazo errado ", 100)+"\n")
		ev("fail", "TestFalha", "")
	}
	ev("fail", "", "")
	s, err := parseGoTestJSON(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Skipped != 500 || s.Failed != 50 {
		t.Errorf("pulados = %d, falhas = %d", s.Skipped, s.Failed)
	}
	if !s.Truncated || len(s.Text) > OutputLimit {
		t.Errorf("truncated = %v, len = %d, quer corte em até %d bytes", s.Truncated, len(s.Text), OutputLimit)
	}
	if !strings.Contains(s.Text, "testes: 0 passaram, 50 falharam, 500 pulados") {
		t.Errorf("contagens fora do começo do resumo:\n%.300s", s.Text)
	}
	if !strings.Contains(s.Text, "resumo truncado") {
		t.Errorf("corte sem aviso:\n%s", s.Text[len(s.Text)-200:])
	}
}

func FuzzParseGoTestJSON(f *testing.F) {
	for _, name := range []string{"misto.jsonl", "pulados.jsonl", "build.jsonl"} {
		f.Add(readGoTestData(f, name), 5)
	}
	f.Add([]byte(`{"Action":"skip","Package":"p","Test":"T"}`+"\n"), 0)
	f.Add([]byte("{\"Action\":\"output\",\"Test\":\"T\",\"Output\":\"\\xff\"}\n"), 3)
	f.Fuzz(func(t *testing.T, data []byte, cut int) {
		whole, err := parseGoTestJSON(bytes.NewReader(data))
		// Em dois pedaços, o resultado é o mesmo.
		p := newGoTestParser()
		if cut < 0 {
			cut = -cut
		}
		if len(data) > 0 {
			cut %= len(data) + 1
		} else {
			cut = 0
		}
		p.Write(data[:cut])
		p.Write(data[cut:])
		split, err2 := p.finish()
		if (err == nil) != (err2 == nil) || split != whole {
			t.Fatalf("resultado depende do corte em %d: %v / %v", cut, err, err2)
		}
		if err != nil {
			return
		}
		if whole.Passed < 0 || whole.Failed < 0 || whole.Skipped < 0 ||
			whole.PackagesFailed+whole.PackagesNoTests > whole.Packages {
			t.Fatalf("contagens incoerentes: %+v", whole)
		}
		if len(whole.Text) > OutputLimit {
			t.Fatalf("resumo com %d bytes, acima de %d", len(whole.Text), OutputLimit)
		}
		if !utf8.ValidString(whole.Text) {
			t.Fatalf("resumo não é UTF-8 válido: %q", whole.Text)
		}
	})
}

func TestRunGoTestJSON(t *testing.T) {
	sensor := func(name, file, exit, skips string, stderr ...string) config.Sensor {
		return config.Sensor{
			Name:   name,
			Cmd:    helperCmd(t, "cat", append([]string{goTestData(t, file), exit}, stderr...)...),
			Format: config.SensorFormatGoTestJSON,
			Skips:  skips,
		}
	}
	cases := []struct {
		name    string
		sensor  config.Sensor
		ok      bool
		skipped int
		errHas  string // "" exige erro vazio
		outHas  []string
		outNot  []string
	}{
		{
			name: "pulados com warn fica verde", sensor: sensor("test", "pulados.jsonl", "0", config.SkipsWarn),
			ok: true, skipped: 2, outHas: []string{"2 pulados", "sem BIBLIOTECA_DB_URL"},
		},
		{
			name: "pulados com fail reprova", sensor: sensor("test", "pulados.jsonl", "0", config.SkipsFail),
			skipped: 2, errHas: "2 teste(s) pulado(s) com skips: fail",
		},
		{
			name: "falha do go test reprova", sensor: sensor("test", "misto.jsonl", "1", config.SkipsWarn),
			skipped: 2, outHas: []string{"prazo = 7, esperado 14"},
		},
		{
			name: "exit 0 com teste falho reprova", sensor: sensor("test", "misto.jsonl", "0", config.SkipsWarn),
			skipped: 2, errHas: "1 teste(s) com falha",
		},
		{
			name: "build quebrado mostra o erro de compilação", sensor: sensor("test", "build.jsonl", "1", config.SkipsWarn),
			outHas: []string{"cannot use"},
		},
		{
			name: "stderr aparece quando o sensor falha", sensor: sensor("test", "build.jsonl", "1", config.SkipsWarn, "go: aviso-no-stderr"),
			outHas: []string{"stderr:", "go: aviso-no-stderr"},
		},
		{
			name: "stderr não aparece quando o sensor passa", sensor: sensor("test", "pulados.jsonl", "0", config.SkipsWarn, "go: baixando-modulo"),
			ok: true, skipped: 2, outNot: []string{"baixando-modulo", "stderr:"},
		},
		{
			name:   "linha ilegível reprova com o erro",
			sensor: config.Sensor{Name: "test", Cmd: helperCmd(t, "ok"), Format: config.SensorFormatGoTestJSON, Skips: config.SkipsWarn},
			errHas: "saída do go test -json ilegível: linha 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, []config.Sensor{tc.sensor})
			r := Run(context.Background(), cfg, Options{})
			s := r.Sensors[0]
			if s.OK != tc.ok || r.OK != tc.ok {
				t.Errorf("ok = %v (resultado %v), quer %v\nerror=%q\n%s", s.OK, r.OK, tc.ok, s.Error, s.Output)
			}
			if s.Skipped != tc.skipped {
				t.Errorf("skipped = %d, quer %d", s.Skipped, tc.skipped)
			}
			if tc.errHas == "" && s.Error != "" {
				t.Errorf("error = %q, quer vazio", s.Error)
			}
			if tc.errHas != "" && !strings.Contains(s.Error, tc.errHas) {
				t.Errorf("error = %q, quer trecho %q", s.Error, tc.errHas)
			}
			for _, h := range tc.outHas {
				if !strings.Contains(s.Output, h) {
					t.Errorf("output sem %q:\n%s", h, s.Output)
				}
			}
			for _, n := range tc.outNot {
				if strings.Contains(s.Output, n) {
					t.Errorf("output com %q:\n%s", n, s.Output)
				}
			}
			if strings.Contains(s.Output, `"Action"`) {
				t.Errorf("output com eventos crus em vez do resumo:\n%s", s.Output)
			}
		})
	}
}

func TestRunSemFormatSkippedZero(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{{Name: "test", Cmd: helperCmd(t, "cat", goTestData(t, "pulados.jsonl"), "0")}})
	r := Run(context.Background(), cfg, Options{})
	if s := r.Sensors[0]; !s.OK || s.Skipped != 0 || !strings.Contains(s.Output, `"Action"`) {
		t.Errorf("sem format: ok=%v skipped=%d; a saída fica crua", s.OK, s.Skipped)
	}
}

func TestPrintHumanSkips(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{
		{Name: "test", Cmd: helperCmd(t, "cat", goTestData(t, "pulados.jsonl"), "0"), Format: config.SensorFormatGoTestJSON, Skips: config.SkipsWarn},
		{Name: "test_db", Cmd: helperCmd(t, "cat", goTestData(t, "pulados.jsonl"), "0"), Format: config.SensorFormatGoTestJSON, Skips: config.SkipsFail},
	})
	var buf bytes.Buffer
	if code := Print(&buf, Run(context.Background(), cfg, Options{Names: []string{"test"}}), false); code != 0 {
		t.Fatalf("warn: Print = %d, quer 0\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"[ok]", "AVISO: 2 teste(s) pulado(s)", "TestBanco", "sem BIBLIOTECA_DB_URL", "aprovado"} {
		if !strings.Contains(out, want) {
			t.Errorf("warn: saída sem %q:\n%s", want, out)
		}
	}

	buf.Reset()
	if code := Print(&buf, Run(context.Background(), cfg, Options{Names: []string{"test_db"}}), false); code != 1 {
		t.Fatalf("fail: Print = %d, quer 1\n%s", code, buf.String())
	}
	out = buf.String()
	for _, want := range []string{"[falha]", "2 teste(s) pulado(s) com skips: fail", "sem BIBLIOTECA_DB_URL", "reprovado"} {
		if !strings.Contains(out, want) {
			t.Errorf("fail: saída sem %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "AVISO") {
		t.Errorf("fail: AVISO em sensor reprovado:\n%s", out)
	}
}
