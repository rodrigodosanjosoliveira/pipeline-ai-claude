package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	ciGitHub = ".github/workflows/delivery.yml"
	ciGitLab = ".gitlab-ci.yml"
)

// TestExpectedProvider: o CI do projeto sai pelo provedor (TD-07, TD-22); só
// o do provedor dado, nunca os dois; sem provedor, nenhum.
func TestExpectedProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     string // CI esperado; "" = nenhum
	}{
		{"github", ciGitHub},
		{"gitlab", ciGitLab},
		{"", ""},
		{"bitbucket", ""},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := ExpectedFor(Project, allModules(), tt.provider)
			var paths []string
			for _, f := range got {
				paths = append(paths, f.Path)
				if f.Class != Project {
					t.Errorf("%s: classe errada em ExpectedFor(Project)", f.Path)
				}
			}
			for _, ci := range []string{ciGitHub, ciGitLab} {
				if has := slices.Contains(paths, ci); has != (ci == tt.want) {
					t.Errorf("provedor %q: %s presente = %v", tt.provider, ci, has)
				}
			}
			// Os demais arquivos do projeto não dependem do provedor.
			base := Expected(Project, allModules())
			if n := len(got) - len(base); (tt.want == "" && n != 0) || (tt.want != "" && n != 1) {
				t.Errorf("provedor %q: %d arquivo(s) além dos sem provedor", tt.provider, n)
			}
			for _, f := range base {
				if !slices.Contains(got, f) {
					t.Errorf("provedor %q: %s sumiu", tt.provider, f.Path)
				}
			}
			if !slices.IsSortedFunc(got, func(a, b File) int { return strings.Compare(a.Path, b.Path) }) {
				t.Error("ExpectedFor fora de ordem")
			}
			// Gerenciado não depende do provedor (o sync não o conhece).
			if !slices.Equal(ExpectedFor(Managed, allModules(), tt.provider), Expected(Managed, allModules())) {
				t.Errorf("provedor %q muda os gerenciados", tt.provider)
			}
		})
	}
	for _, f := range Expected(Project, allModules()) {
		if f.Provider != "" {
			t.Errorf("%s: Expected devolveu arquivo de provedor %q", f.Path, f.Provider)
		}
	}
	// O catálogo declara o provedor de cada CI.
	for p, want := range map[string]string{ciGitHub: "github", ciGitLab: "gitlab"} {
		if f := findFile(t, p); f.Provider != want || f.Class != Project {
			t.Errorf("%s: provedor %q, classe %v; quero %s, projeto", p, f.Provider, f.Class, want)
		}
	}
}

// yamlLine é uma linha não vazia e fora de comentário de um YAML gerado.
var (
	yamlKeyLine  = regexp.MustCompile(`^( *)(- )?([A-Za-z_][A-Za-z0-9_.-]*):( .*)?$`)
	yamlItemLine = regexp.MustCompile(`^( *)- \S.*$`)
)

// checkYAMLShape confere a forma do YAML gerado sem parser (a stdlib não tem):
// sem tab, indentação em múltiplos de 2, toda linha é chave ou item de lista e
// a indentação só cresce depois de uma linha que abre bloco (termina em ":").
func checkYAMLShape(t *testing.T, name, text string) {
	t.Helper()
	prevIndent, prevOpens := 0, false
	n := 0
	for i, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "\t") {
			t.Errorf("%s:%d: tab", name, i+1)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		n++
		if strings.TrimRight(line, " ") != line {
			t.Errorf("%s:%d: espaço no fim", name, i+1)
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent%2 != 0 {
			t.Errorf("%s:%d: indentação ímpar", name, i+1)
		}
		if !yamlKeyLine.MatchString(line) && !yamlItemLine.MatchString(line) {
			t.Errorf("%s:%d: linha não é chave nem item: %q", name, i+1, line)
		}
		if indent > prevIndent && (!prevOpens || indent-prevIndent > 4) {
			t.Errorf("%s:%d: indentação cresce sem bloco aberto", name, i+1)
		}
		// Abre bloco a chave sem valor; "- chave: valor" abre o mapa do item.
		code, _, _ := strings.Cut(trimmed, " #")
		prevIndent, prevOpens = indent, strings.HasSuffix(code, ":") || strings.HasPrefix(trimmed, "- ")
		if strings.HasPrefix(trimmed, "- ") && strings.Contains(trimmed, ":") {
			prevIndent = indent + 2
			prevOpens = strings.HasSuffix(code, ":")
		}
	}
	if n == 0 {
		t.Errorf("%s: vazio", name)
	}
}

// ghJob é um job do workflow: nome e linhas.
type ghJob struct {
	name  string
	lines []string
}

// workflowJobs separa os jobs (chaves com 2 espaços sob "jobs:").
func workflowJobs(text string) []ghJob {
	var jobs []ghJob
	in := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case line == "jobs:":
			in = true
		case in && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":"):
			jobs = append(jobs, ghJob{name: strings.TrimSuffix(strings.TrimSpace(line), ":")})
		case in && line != "" && !strings.HasPrefix(line, " "):
			in = false
		case in && len(jobs) > 0:
			jobs[len(jobs)-1].lines = append(jobs[len(jobs)-1].lines, line)
		}
	}
	return jobs
}

// runBlocks devolve o comando de cada "run:" do texto: o valor da linha ou,
// em bloco ("run: |"), as linhas mais indentadas que seguem.
func runBlocks(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		v, ok := strings.CutPrefix(trimmed, "run:")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v != "" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">") {
			out = append(out, v)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		var block []string
		for i+1 < len(lines) {
			next := lines[i+1]
			if strings.TrimSpace(next) != "" && len(next)-len(strings.TrimLeft(next, " ")) <= indent {
				break
			}
			block = append(block, next)
			i++
		}
		out = append(out, v+"\n"+strings.Join(block, "\n"))
	}
	return out
}

func TestRunBlocks(t *testing.T) {
	lines := strings.Split(`      - run: make tools
      - name: x
        run: |
          echo "${{ github.head_ref }}"
          echo b
        env:
          A: b
      - run: >
          echo c
`, "\n")
	got := runBlocks(lines)
	if len(got) != 3 || got[0] != "make tools" || !strings.Contains(got[1], "github.head_ref") ||
		strings.Contains(got[1], "A: b") || !strings.Contains(got[2], "echo c") {
		t.Errorf("runBlocks = %q", got)
	}
}

// ciUses são as linhas "uses:" do CI deste repositório: o CI gerado fixa as
// mesmas actions nos mesmos SHAs (o Dependabot daqui as atualiza).
func ciUses(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("CI do toolkit: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(line), "- "), "uses: "); ok {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		t.Fatal("CI do toolkit sem uses")
	}
	return out
}

var pinnedUse = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40} # v\d+(\.\d+)*$`)

func TestGitHubWorkflow(t *testing.T) {
	d := sampleData()
	d.Provider = "github"
	text := render(t, ciGitHub, d)
	checkYAMLShape(t, ciGitHub, text)
	lines := strings.Split(text, "\n")

	// Permissão mínima no nível do workflow.
	if !strings.Contains(text, "\npermissions:\n  contents: read\n") {
		t.Error("sem permissions: contents: read no nível do workflow")
	}
	// Actions fixadas por SHA, as mesmas do CI do toolkit.
	known := ciUses(t)
	nUses, nCheckout := 0, 0
	for _, line := range lines {
		v, ok := strings.CutPrefix(strings.TrimPrefix(strings.TrimSpace(line), "- "), "uses: ")
		if !ok {
			continue
		}
		nUses++
		if !pinnedUse.MatchString(v) {
			t.Errorf("action sem SHA de 40 hex e comentário de versão: %q", v)
		}
		if !slices.Contains(known, v) {
			t.Errorf("action %q diferente da fixada no CI do toolkit %v", v, known)
		}
		if strings.HasPrefix(v, "actions/checkout@") {
			nCheckout++
		}
	}
	if nUses == 0 || nCheckout == 0 {
		t.Fatalf("workflow sem actions (%d) ou sem checkout (%d)", nUses, nCheckout)
	}
	if n := strings.Count(text, "persist-credentials: false"); n != nCheckout {
		t.Errorf("persist-credentials: false em %d de %d checkouts", n, nCheckout)
	}

	// Nenhum run interpola expressão: nome de branch vem de quem abre a PR.
	runs := runBlocks(lines)
	if len(runs) == 0 {
		t.Fatal("workflow sem run")
	}
	for _, r := range runs {
		if strings.Contains(r, "${{") {
			t.Errorf("run com expressão interpolada: %q", r)
		}
	}
	for _, env := range []string{"HEAD_REF: ${{ github.head_ref }}", "BASE_REF: ${{ github.base_ref }}"} {
		if !strings.Contains(text, env) {
			t.Errorf("sem %q no env", env)
		}
	}

	// Dois jobs: gates (sensores) e validate (delivery em branch task/).
	jobs := workflowJobs(text)
	var gates, validate bool
	for _, j := range jobs {
		r := strings.Join(runBlocks(j.lines), "\n")
		body := strings.Join(j.lines, "\n")
		switch {
		case strings.Contains(r, "make tools") && strings.Contains(r, "bin/pipeline-claude sensors"):
			gates = true
		case strings.Contains(r, "make tools") && strings.Contains(r, `bin/pipeline-claude validate --base "origin/$BASE_REF" "${HEAD_REF#task/}"`):
			validate = true
			if !strings.Contains(body, "if: startsWith(github.head_ref, 'task/')") {
				t.Errorf("job %s valida fora de branch task/", j.name)
			}
			if !strings.Contains(body, "fetch-depth: 0") {
				t.Errorf("job %s sem fetch-depth: 0 (o diff é contra a base)", j.name)
			}
		}
	}
	if !gates || !validate {
		t.Errorf("jobs %v: gates = %v, validate = %v", jobs, gates, validate)
	}
	if !strings.Contains(text, "\non:\n  pull_request:\n") {
		t.Error("workflow não roda em pull_request")
	}
}

func TestGitLabCI(t *testing.T) {
	d := sampleData()
	d.Provider = "gitlab"
	text := render(t, ciGitLab, d)
	checkYAMLShape(t, ciGitLab, text)

	// A imagem é a do Go do go.mod gerado.
	var goVersion string
	for _, line := range strings.Split(render(t, "go.mod", d), "\n") {
		if v, ok := strings.CutPrefix(line, "go "); ok {
			goVersion = strings.TrimSpace(v)
		}
	}
	if goVersion == "" {
		t.Fatal("go.mod gerado sem diretiva go")
	}
	images := regexp.MustCompile(`(?m)^\s*image: (\S+)`).FindAllStringSubmatch(text, -1)
	if len(images) == 0 {
		t.Fatal("sem image")
	}
	for _, m := range images {
		if m[1] != "golang:"+goVersion {
			t.Errorf("image %s, quero golang:%s (o go.mod)", m[1], goVersion)
		}
	}

	jobs := map[string]string{}
	var cur string
	for _, line := range strings.Split(text, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			cur = strings.TrimSuffix(line, ":")
			jobs[cur] = ""
			continue
		}
		if cur != "" {
			jobs[cur] += line + "\n"
		}
	}
	all := jobs["default"]
	for _, want := range []string{
		`git config --global --add safe.directory "$CI_PROJECT_DIR"`,
		"make tools",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("default sem %q", want)
		}
	}
	gates, validate := jobs["gates"], jobs["delivery-validate"]
	if !strings.Contains(gates, "- bin/pipeline-claude sensors") {
		t.Errorf("job gates sem sensores:\n%s", gates)
	}
	for _, want := range []string{
		`- if: $CI_PIPELINE_SOURCE == "merge_request_event" && $CI_MERGE_REQUEST_SOURCE_BRANCH_NAME =~ /^task\//`,
		`GIT_DEPTH: "0"`,
		`- git fetch --no-tags origin "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"`,
		`- bin/pipeline-claude validate --base "origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME" "$ID"`,
	} {
		if !strings.Contains(validate, want) {
			t.Errorf("delivery-validate sem %q:\n%s", want, validate)
		}
	}
	if strings.Contains(text, "${{") {
		t.Error("expressão do GitHub no CI do GitLab")
	}
}

func TestGitignoreLocalSettings(t *testing.T) {
	lines := strings.Split(render(t, ".gitignore", sampleData()), "\n")
	for _, want := range []string{".claude/settings.local.json", "bin/"} {
		if !slices.Contains(lines, want) {
			t.Errorf(".gitignore sem a linha %q", want)
		}
	}
}
