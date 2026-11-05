package sensors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxEventLine é o tamanho máximo de uma linha de evento do go test -json.
// Linha maior reprova o sensor.
const maxEventLine = 1 << 20

// Limites de memória do parser: saída guardada por teste, por pacote e por
// build, testes pulados e falhas listados, testes em andamento acompanhados.
const (
	eventOutputLimit = 2 << 10
	maxListed        = 200
	maxRunning       = 10000
)

// truncatedMark fecha um resumo cortado em OutputLimit.
const truncatedMark = "\n… (resumo truncado)\n"

// goTestSummary é o resumo da saída de go test -json (TD-43).
type goTestSummary struct {
	// Packages conta os pacotes que terminaram; PackagesFailed os que
	// falharam (inclusive por build) e PackagesNoTests os sem arquivo de teste.
	Packages, PackagesFailed, PackagesNoTests int
	// Passed, Failed e Skipped contam testes e subtestes.
	Passed, Failed, Skipped int
	// Text é o resumo legível, com no máximo OutputLimit bytes.
	Text string
	// Truncated indica que Text foi cortado.
	Truncated bool
}

// testEvent é um evento do test2json. Campos que o parser não usa são
// ignorados; Action é obrigatório.
type testEvent struct {
	Action      string
	Package     string
	Test        string
	Output      string
	ImportPath  string
	FailedBuild string
}

type pkgState struct {
	status      string // pass, fail ou skip; vazio enquanto roda
	out         *tailBuffer
	failedTests int
	failedBuild string
}

type listed struct {
	head  string // linha --- SKIP / --- FAIL / FAIL <pacote>
	pkg   string
	lines []string
}

// goTestParser consome a saída de go test -json como io.Writer, linha a linha,
// com memória limitada. Write nunca falha, para o processo não travar no pipe;
// o primeiro erro fica guardado e finish o devolve.
type goTestParser struct {
	partial  []byte
	overflow bool
	line     int
	events   int
	err      error

	pkgs        map[string]*pkgState
	order       []string
	tests       map[string]*tailBuffer
	builds      map[string]*tailBuffer
	buildFailed []string

	passed, failed, skipped int
	skips, fails            []listed
}

func newGoTestParser() *goTestParser {
	return &goTestParser{
		pkgs:   map[string]*pkgState{},
		tests:  map[string]*tailBuffer{},
		builds: map[string]*tailBuffer{},
	}
}

// parseGoTestJSON lê r inteiro e devolve o resumo.
func parseGoTestJSON(r io.Reader) (goTestSummary, error) {
	p := newGoTestParser()
	if _, err := io.Copy(p, r); err != nil {
		return goTestSummary{}, err
	}
	return p.finish()
}

// Write consome p inteiro e nunca devolve erro.
func (p *goTestParser) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !p.overflow && len(p.partial)+len(b) > maxEventLine {
				p.overflow, p.partial = true, nil
			}
			if !p.overflow {
				p.partial = append(p.partial, b...)
			}
			break
		}
		p.endLine(b[:i])
		b = b[i+1:]
	}
	return n, nil
}

// endLine fecha a linha em andamento com o resto dela.
func (p *goTestParser) endLine(rest []byte) {
	p.line++
	tooLong := p.overflow || len(p.partial)+len(rest) > maxEventLine
	line := append(p.partial, rest...)
	p.partial, p.overflow = nil, false
	if p.err != nil {
		return
	}
	if tooLong {
		p.err = fmt.Errorf("linha %d: acima de %d bytes", p.line, maxEventLine)
		return
	}
	p.handle(bytes.TrimSuffix(line, []byte("\r")))
}

// finish fecha a última linha, sem quebra no fim, e devolve o resumo. Com
// erro, o resumo traz o que foi lido até a linha ilegível.
func (p *goTestParser) finish() (goTestSummary, error) {
	if p.overflow || len(p.partial) > 0 {
		p.endLine(nil)
	}
	if p.err == nil && p.events == 0 {
		p.err = errors.New("nenhum evento do go test -json na saída")
	}
	return p.summary(), p.err
}

func (p *goTestParser) handle(line []byte) {
	var ev testEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		p.err = fmt.Errorf("linha %d: não é evento JSON (%v): %q", p.line, err, clip(line, 120))
		return
	}
	if ev.Action == "" {
		p.err = fmt.Errorf("linha %d: evento sem Action: %q", p.line, clip(line, 120))
		return
	}
	p.events++
	switch ev.Action {
	case "output":
		switch {
		case ev.Test != "" && ev.Package != "":
			if b := p.testBuffer(ev.Package, ev.Test); b != nil {
				b.Write([]byte(ev.Output))
			}
		case ev.Package != "":
			p.pkg(ev.Package).out.Write([]byte(ev.Output))
		}
	case "build-output":
		if ev.ImportPath != "" {
			p.build(ev.ImportPath).Write([]byte(ev.Output))
		}
	case "build-fail":
		if ev.ImportPath != "" {
			p.build(ev.ImportPath)
			p.buildFailed = append(p.buildFailed, ev.ImportPath)
		}
	case "pass", "fail", "skip":
		switch {
		case ev.Test != "" && ev.Package != "":
			p.endTest(ev)
		case ev.Package != "":
			p.endPackage(ev)
		}
	}
}

func (p *goTestParser) endTest(ev testEvent) {
	key := ev.Package + "\x00" + ev.Test
	var lines []string
	if b, ok := p.tests[key]; ok {
		out, _ := b.result()
		lines = outputLines(out)
		delete(p.tests, key)
	}
	switch ev.Action {
	case "pass":
		p.passed++
	case "skip":
		p.skipped++
		if len(p.skips) < maxListed {
			p.skips = append(p.skips, frame(lines, "SKIP", ev.Test, ev.Package))
		}
	case "fail":
		p.failed++
		p.pkg(ev.Package).failedTests++
		if len(p.fails) < maxListed {
			p.fails = append(p.fails, frame(lines, "FAIL", ev.Test, ev.Package))
		}
	}
}

func (p *goTestParser) endPackage(ev testEvent) {
	s := p.pkg(ev.Package)
	s.status = ev.Action
	if ev.Action == "fail" {
		s.failedBuild = ev.FailedBuild
	}
}

func (p *goTestParser) pkg(name string) *pkgState {
	s, ok := p.pkgs[name]
	if !ok {
		s = &pkgState{out: newTailBuffer(eventOutputLimit)}
		p.pkgs[name] = s
		p.order = append(p.order, name)
	}
	return s
}

// testBuffer devolve a saída guardada do teste; nil quando há testes demais em
// andamento (a contagem continua certa, só a saída se perde).
func (p *goTestParser) testBuffer(pkg, test string) *tailBuffer {
	key := pkg + "\x00" + test
	b, ok := p.tests[key]
	if !ok {
		if len(p.tests) >= maxRunning {
			return nil
		}
		b = newTailBuffer(eventOutputLimit)
		p.tests[key] = b
	}
	return b
}

func (p *goTestParser) build(importPath string) *tailBuffer {
	b, ok := p.builds[importPath]
	if !ok {
		if len(p.builds) >= maxRunning {
			return newTailBuffer(0)
		}
		b = newTailBuffer(eventOutputLimit)
		p.builds[importPath] = b
	}
	return b
}

// summary monta o resumo: contagens, testes pulados com o motivo e falhas com
// a saída.
func (p *goTestParser) summary() goTestSummary {
	s := goTestSummary{Passed: p.passed, Failed: p.failed, Skipped: p.skipped}

	// Build que falhou sem evento de pacote: o pacote conta como falho.
	usedBuild := map[string]bool{}
	for _, name := range p.order {
		if st := p.pkgs[name]; st.status == "fail" && st.failedBuild != "" {
			usedBuild[st.failedBuild] = true
		}
	}
	var orphanBuilds []string
	for _, ip := range p.buildFailed {
		if !usedBuild[ip] {
			usedBuild[ip] = true
			orphanBuilds = append(orphanBuilds, ip)
		}
	}

	var pkgFails []listed
	ok := 0
	for _, name := range p.order {
		st := p.pkgs[name]
		if st.status != "" {
			s.Packages++
		}
		switch st.status {
		case "pass":
			ok++
		case "skip":
			s.PackagesNoTests++
		case "fail":
			s.PackagesFailed++
			if st.failedTests > 0 && st.failedBuild == "" {
				continue // as falhas dos testes já dizem o que houve
			}
			var lines []string
			if b, found := p.builds[st.failedBuild]; found && st.failedBuild != "" {
				out, _ := b.result()
				lines = outputLines(out)
			}
			out, _ := st.out.result()
			lines = append(lines, outputLines(out)...)
			if len(pkgFails) < maxListed {
				pkgFails = append(pkgFails, listed{head: "FAIL " + name, lines: lines})
			}
		}
	}
	for _, ip := range orphanBuilds {
		s.Packages++
		s.PackagesFailed++
		out, _ := p.builds[ip].result()
		if len(pkgFails) < maxListed {
			pkgFails = append(pkgFails, listed{head: "FAIL " + ip + " [build failed]", lines: outputLines(out)})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "pacotes: %d (%d ok, %d com falha, %d sem testes)\n", s.Packages, ok, s.PackagesFailed, s.PackagesNoTests)
	fmt.Fprintf(&b, "testes: %s, %s, %s\n",
		plural(s.Passed, "passou", "passaram"), plural(s.Failed, "falhou", "falharam"), plural(s.Skipped, "pulado", "pulados"))
	writeListed(&b, "pulados", p.skips, s.Skipped)
	writeListed(&b, "falhas", slices.Concat(p.fails, pkgFails), s.Failed+len(pkgFails))
	s.Text, s.Truncated = capText(b.String())
	return s
}

func writeListed(b *strings.Builder, title string, items []listed, total int) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:\n", title)
	for _, it := range items {
		if it.pkg != "" {
			fmt.Fprintf(b, "  %s [%s]\n", it.head, it.pkg)
		} else {
			fmt.Fprintf(b, "  %s\n", it.head)
		}
		for _, l := range it.lines {
			fmt.Fprintf(b, "      %s\n", l)
		}
	}
	if total > len(items) {
		fmt.Fprintf(b, "  … e mais %d\n", total-len(items))
	}
}

// frame separa a linha "--- <kind>: <teste> (0.00s)" das demais; sem ela, o
// cabeçalho é "--- <kind>: <teste>".
func frame(lines []string, kind, test, pkg string) listed {
	marker := "--- " + kind + ": " + test
	head := ""
	rest := make([]string, 0, len(lines))
	for _, l := range lines {
		if head == "" && (l == marker || strings.HasPrefix(l, marker+" ")) {
			head = l
			continue
		}
		rest = append(rest, l)
	}
	if head == "" {
		head = marker
	}
	return listed{head: head, pkg: pkg, lines: rest}
}

// outputLines divide a saída em linhas sem espaço nas pontas, sem as molduras
// de execução (=== RUN, PAUSE, CONT, NAME) e sem linhas vazias.
func outputLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "=== ") {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// capText devolve s em UTF-8 válido e com no máximo OutputLimit bytes; o
// corte guarda o começo (as contagens) e termina com truncatedMark.
func capText(s string) (string, bool) {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= OutputLimit {
		return s, false
	}
	cut := OutputLimit - len(truncatedMark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedMark, true
}

func clip(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}
