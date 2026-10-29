package main

// Testes dos documentos do contrato (TD-37, TD-38): o CHANGELOG.md segue o
// "Keep a Changelog" com uma seção por versão publicada, o docs/CONTRATO.md
// cobre cada arquivo-referência de testdata/contract e os links relativos dos
// documentos de entrada apontam para arquivos que existem.

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func readRepoDoc(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("ler %s: %v", rel, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

var (
	changelogVersion = regexp.MustCompile(`^## \[(v\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)\] - (\d{4}-\d{2}-\d{2})$`)
	changelogKinds   = []string{"Adicionado", "Alterado", "Obsoleto", "Removido", "Corrigido", "Segurança"}
)

const unreleasedHeading = "## [Não lançado]"

type changelogSection struct {
	name  string // "Não lançado" ou a versão
	kinds []string
	items int
}

// parseChangelog devolve as seções na ordem do arquivo ou o primeiro problema.
func parseChangelog(text string) ([]changelogSection, string) {
	var secs []changelogSection
	var cur *changelogSection
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		switch {
		case line == unreleasedHeading:
			secs = append(secs, changelogSection{name: "Não lançado"})
			cur = &secs[len(secs)-1]
		case strings.HasPrefix(line, "## "):
			m := changelogVersion.FindStringSubmatch(line)
			if m == nil {
				return nil, "linha " + strconv.Itoa(n) + ": título de versão fora do formato `## [vX.Y.Z] - AAAA-MM-DD`: " + line
			}
			secs = append(secs, changelogSection{name: m[1]})
			cur = &secs[len(secs)-1]
		case strings.HasPrefix(line, "### "):
			kind := strings.TrimPrefix(line, "### ")
			if cur == nil || !slices.Contains(changelogKinds, kind) {
				return nil, "linha " + strconv.Itoa(n) + ": subseção inválida ou fora de versão: " + line
			}
			if slices.Contains(cur.kinds, kind) {
				return nil, "linha " + strconv.Itoa(n) + ": subseção repetida em " + cur.name + ": " + line
			}
			cur.kinds = append(cur.kinds, kind)
		case strings.HasPrefix(line, "- ") && cur != nil:
			if len(cur.kinds) == 0 {
				return nil, "linha " + strconv.Itoa(n) + ": item fora de subseção em " + cur.name
			}
			cur.items++
		}
	}
	return secs, ""
}

// semverLess compara vX.Y.Z[-pre]; pré-lançamento vem antes da versão final.
func semverLess(a, b string) bool {
	split := func(v string) ([3]int, string) {
		core, pre, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
		var n [3]int
		for i, p := range strings.SplitN(core, ".", 3) {
			n[i], _ = strconv.Atoi(p)
		}
		return n, pre
	}
	na, pa := split(a)
	nb, pb := split(b)
	for i := range na {
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	switch {
	case pa == pb:
		return false
	case pa == "":
		return false
	case pb == "":
		return true
	}
	return pa < pb
}

func TestChangelog(t *testing.T) {
	text := readRepoDoc(t, "CHANGELOG.md")
	if !strings.Contains(text, "https://keepachangelog.com/") {
		t.Error("CHANGELOG.md não cita o formato Keep a Changelog")
	}
	secs, problem := parseChangelog(text)
	if problem != "" {
		t.Fatal(problem)
	}
	if len(secs) == 0 || secs[0].name != "Não lançado" {
		t.Fatalf("a primeira seção do CHANGELOG.md precisa ser %q", unreleasedHeading)
	}
	var versions []string
	for _, s := range secs[1:] {
		if s.name == "Não lançado" {
			t.Errorf("seção %q repetida", unreleasedHeading)
			continue
		}
		if slices.Contains(versions, s.name) {
			t.Errorf("versão %s repetida", s.name)
		}
		if s.items == 0 {
			t.Errorf("versão %s sem itens", s.name)
		}
		versions = append(versions, s.name)
	}
	for i := 1; i < len(versions); i++ {
		if !semverLess(versions[i], versions[i-1]) {
			t.Errorf("versões fora de ordem decrescente: %s antes de %s", versions[i-1], versions[i])
		}
	}
	for _, v := range []string{"v0.1.0", "v0.2.0", "v0.3.0", "v1.0.0-rc.1", "v1.0.0-rc.2"} {
		if !slices.Contains(versions, v) {
			t.Errorf("versão publicada %s sem seção no CHANGELOG.md", v)
		}
	}
	// Referências de link: [Não lançado] compara a última versão com HEAD;
	// cada versão compara com a anterior (a primeira aponta para a tag).
	repo := "https://github.com/rodrigodosanjosoliveira/pipeline-ai-claude"
	if len(versions) > 0 {
		want := "[Não lançado]: " + repo + "/compare/" + versions[0] + "...HEAD"
		if !strings.Contains(text, "\n"+want+"\n") {
			t.Errorf("falta a referência %q", want)
		}
	}
	for i, v := range versions {
		want := "[" + v + "]: " + repo + "/releases/tag/" + v
		if i+1 < len(versions) {
			want = "[" + v + "]: " + repo + "/compare/" + versions[i+1] + "..." + v
		}
		if !strings.Contains(text, "\n"+want+"\n") {
			t.Errorf("falta a referência %q", want)
		}
	}
}

func TestParseChangelogRejects(t *testing.T) {
	base := "# Changelog\n\n## [Não lançado]\n\n### Adicionado\n\n- a\n\n"
	cases := []struct{ name, text, want string }{
		{"título sem data", base + "## [v0.1.0]\n", "formato"},
		{"versão sem v", base + "## [0.1.0] - 2026-10-27\n", "formato"},
		{"subseção desconhecida", base + "### Novidades\n", "subseção"},
		{"subseção repetida", base + "### Adicionado\n", "repetida"},
		{"item fora de subseção", "## [Não lançado]\n\n- a\n", "fora de subseção"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, problem := parseChangelog(c.text)
			if !strings.Contains(problem, c.want) {
				t.Errorf("problema = %q, quer conter %q", problem, c.want)
			}
		})
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.10.0", "v0.9.0", false},
		{"v1.0.0-rc.1", "v1.0.0", true},
		{"v1.0.0", "v1.0.0-rc.1", false},
		{"v1.0.0-rc.1", "v1.0.0-rc.2", true},
		{"v0.3.0", "v1.0.0-rc.1", true},
		{"v1.0.0", "v1.0.0", false},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%s, %s) = %v, quer %v", c.a, c.b, got, c.want)
		}
	}
}

// TestChangelogTags confere que cada tag de versão do clone tem seção. O CI
// clona sem tags; aí o teste pula com o motivo e a lista fixa de
// TestChangelog continua valendo.
func TestChangelogTags(t *testing.T) {
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."), "tag", "--list", "v*").Output()
	if err != nil {
		t.Skipf("git tag indisponível (%v): sem como listar as tags", err)
	}
	tags := strings.Fields(string(out))
	if len(tags) == 0 {
		t.Skip("clone sem tags (checkout raso do CI): seções conferidas só pela lista fixa")
	}
	secs, problem := parseChangelog(readRepoDoc(t, "CHANGELOG.md"))
	if problem != "" {
		t.Fatal(problem)
	}
	var names []string
	for _, s := range secs {
		names = append(names, s.name)
	}
	for _, tag := range tags {
		if !slices.Contains(names, tag) {
			t.Errorf("tag %s sem seção no CHANGELOG.md", tag)
		}
	}
}

func TestContratoDoc(t *testing.T) {
	text := readRepoDoc(t, "docs/CONTRATO.md")
	if !strings.Contains(text, contractUpdateHint) {
		t.Errorf("docs/CONTRATO.md não traz o comando de regravação %q", contractUpdateHint)
	}
	root := filepath.Join("..", "..", "testdata", "contract")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := "testdata/contract/" + filepath.ToSlash(rel)
		if !strings.Contains(text, name) {
			t.Errorf("docs/CONTRATO.md não cita %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TD-37", "TD-38", "/v2", "CHANGELOG.md", "Não lançado"} {
		if !strings.Contains(text, want) {
			t.Errorf("docs/CONTRATO.md não cita %q", want)
		}
	}
}

var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// TestDocLinks confere os links relativos dos documentos de entrada.
func TestDocLinks(t *testing.T) {
	docs := []string{"README.md", "CHANGELOG.md", "CONTRIBUTING.md", "docs/CONTRATO.md", "docs/ADOCAO.md", "docs/CI.md"}
	for _, doc := range docs {
		text := readRepoDoc(t, doc)
		for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			file := filepath.Join("..", "..", filepath.FromSlash(path.Join(path.Dir(doc), target)))
			if _, err := os.Stat(file); err != nil {
				t.Errorf("%s: link %q aponta para arquivo inexistente", doc, m[1])
			}
		}
	}
	readme := readRepoDoc(t, "README.md")
	for _, want := range []string{"](CHANGELOG.md)", "](docs/CONTRATO.md)", "v1.0 em preparação", "](https://github.com/rodrigodosanjosoliveira/biblioteca-bairro)"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md não traz %q", want)
		}
	}
}
