// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/scaffold/sync_test.go @ 0eac73a (MIT).
// Alterações: casos de TestSyncAgents mantidos em TestSyncPortedCases e
// adaptados ao lock e ao plano (TD-23, TD-25), com comentário onde o
// comportamento mudou; tabela do TD-25, conferência antes da escrita, symlink,
// lock adulterado, CRLF, release, idempotência e FuzzPlan novos.

package syncer

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

const (
	testVersion = "v0.1.0"
	newVersion  = "v0.2.0"

	reviewer  = ".claude/agents/code-reviewer.md" // gerenciado, sempre esperado
	dataModel = ".claude/agents/data-model.md"    // gerenciado, módulo data-model
	prePush   = ".githooks/pre-push"              // gerenciado, 0755
	oldSkill  = ".claude/skills/antiga/SKILL.md"  // "template removido": nunca está no catálogo
)

// project é um projeto gerado em diretório temporário: arquivos gerenciados
// renderizados, lock com o hash de cada um.
type project struct {
	root string
	cfg  *config.Config
	lock *manifest.Lock
}

// newProject gera os arquivos gerenciados de Expected(Managed, modules) e o
// lock, como o init faria. Não depende do pacote scaffold.
func newProject(t *testing.T, modules ...string) *project {
	t.Helper()
	root := t.TempDir()
	lock := &manifest.Lock{Version: manifest.Version, ToolkitVersion: testVersion, Files: map[string]string{}}
	for _, f := range templates.Expected(templates.Managed, modules) {
		data := render(t, f.Path, modules)
		writeFile(t, root, f.Path, data, f.Mode)
		lock.Files[f.Path] = manifest.Hash(data)
	}
	if err := manifest.Write(root, lock); err != nil {
		t.Fatal(err)
	}
	return &project{root: root, cfg: &config.Config{EnabledModules: modules}, lock: readLock(t, root)}
}

// render renderiza o arquivo gerenciado rel com os módulos dados.
func render(t *testing.T, rel string, modules []string) []byte {
	t.Helper()
	for _, f := range templates.Catalog() {
		if f.Path == rel {
			data, err := templates.Render(f, templates.Data{Modules: modules})
			if err != nil {
				t.Fatal(err)
			}
			return data
		}
	}
	t.Fatalf("%s fora do catálogo", rel)
	return nil
}

func writeFile(t *testing.T, root, rel string, data []byte, mode fs.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, rel string) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return data, true
}

func readLock(t *testing.T, root string) *manifest.Lock {
	t.Helper()
	l, err := manifest.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func lockBytes(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (p *project) plan(t *testing.T) []Item {
	t.Helper()
	items, err := Plan(p.root, p.cfg, p.lock)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return items
}

func (p *project) apply(t *testing.T, items []Item) []Item {
	t.Helper()
	out, err := Apply(p.root, items, p.lock, newVersion)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	p.lock = readLock(t, p.root)
	return out
}

func find(items []Item, rel string) (Item, bool) {
	for _, it := range items {
		if it.Path == rel {
			return it, true
		}
	}
	return Item{}, false
}

func actionOf(t *testing.T, items []Item, rel string) Action {
	t.Helper()
	it, ok := find(items, rel)
	if !ok {
		t.Fatalf("plano sem %s: %v", rel, items)
	}
	return it.Action
}

// snapshot devolve todo arquivo sob dir (caminho → conteúdo), sem seguir
// symlink.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "-> " + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[rel] = info.Mode().String() + " " + string(data)
		case d.IsDir():
			out[rel+"/"] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink indisponível: %v", err)
	}
}

// TestPlanTD25 é a tabela do TD-25, uma linha por situação, e o efeito de
// Apply sobre o caminho.
func TestPlanTD25(t *testing.T) {
	old := []byte("# versão antiga do template\n")
	edited := []byte("# editado pelo projeto\n")

	cases := []struct {
		name    string
		modules []string // enabled_modules da config no plano
		setup   func(t *testing.T, p *project)
		rel     string
		want    Action
		// after confere o disco e o lock depois do Apply.
		after func(t *testing.T, p *project, rel string)
	}{
		{
			name: "no lock, disco = lock, template igual: nada",
			rel:  reviewer, want: Nada,
			after: wantRendered(nil),
		},
		{
			name: "no lock, disco = lock, template mudou: atualiza",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, reviewer, old, 0o644)
				p.lock.Files[reviewer] = manifest.Hash(old)
			},
			rel: reviewer, want: Atualizar,
			after: wantRendered(nil),
		},
		{
			name: "no lock, disco ≠ lock: editado, não toca",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, reviewer, edited, 0o644)
			},
			rel: reviewer, want: Editado,
			after: wantContent(edited, true),
		},
		{
			name: "no lock, ausente no disco: recria",
			setup: func(t *testing.T, p *project) {
				removeFile(t, p.root, reviewer)
			},
			rel: reviewer, want: Recriar,
			after: wantRendered(nil),
		},
		{
			name:    "fora do lock, ausente no disco: cria",
			modules: []string{"data-model"},
			rel:     dataModel, want: Criar,
			after: wantRendered([]string{"data-model"}),
		},
		{
			name:    "fora do lock, existe no disco: não gerenciado, não toca",
			modules: []string{"data-model"},
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, dataModel, edited, 0o644)
			},
			rel: dataModel, want: NaoGerenciado,
			after: wantContent(edited, false),
		},
		{
			name: "em released, esperado e editado: nada, nunca",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, reviewer, edited, 0o644)
				release(p, reviewer)
			},
			rel: reviewer, want: Liberado,
			after: wantReleased(edited),
		},
		{
			name: "em released, esperado e ausente: não recria",
			setup: func(t *testing.T, p *project) {
				removeFile(t, p.root, reviewer)
				release(p, reviewer)
			},
			rel: reviewer, want: Liberado,
			after: wantReleased(nil),
		},
		{
			name: "em released, fora do esperado e intacto: não apaga",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, oldSkill, old, 0o644)
				p.lock.Released = append(p.lock.Released, oldSkill)
			},
			rel: oldSkill, want: Liberado,
			after: wantReleased(old),
		},
		{
			name: "no lock, fora do esperado (módulo desligado), disco = lock: apaga",
			setup: func(t *testing.T, p *project) {
				data := render(t, dataModel, []string{"data-model"})
				writeFile(t, p.root, dataModel, data, 0o644)
				p.lock.Files[dataModel] = manifest.Hash(data)
			},
			rel: dataModel, want: Apagar,
			after: wantGone,
		},
		{
			name: "no lock, fora do esperado (template removido), disco = lock: apaga",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, oldSkill, old, 0o644)
				p.lock.Files[oldSkill] = manifest.Hash(old)
			},
			rel: oldSkill, want: Apagar,
			after: func(t *testing.T, p *project, rel string) {
				wantGone(t, p, rel)
				// Diretório que ficou vazio sai; o pai fora da área fica.
				if _, err := os.Lstat(filepath.Join(p.root, ".claude/skills/antiga")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("diretório vazio .claude/skills/antiga ficou: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(p.root, ".claude/skills")); err != nil {
					t.Errorf(".claude/skills foi removido: %v", err)
				}
			},
		},
		{
			name: "idem, disco ≠ lock: editado, não toca, entrada fica",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, dataModel, edited, 0o644)
				p.lock.Files[dataModel] = manifest.Hash(render(t, dataModel, []string{"data-model"}))
			},
			rel: dataModel, want: Editado,
			after: wantContent(edited, true),
		},
		{
			// Fora da tabela: entrada do lock fora do esperado e já ausente
			// no disco sai do lock, sem tocar em arquivo.
			name: "no lock, fora do esperado, ausente no disco: apaga (só do lock)",
			setup: func(t *testing.T, p *project) {
				p.lock.Files[oldSkill] = manifest.Hash(old)
			},
			rel: oldSkill, want: Apagar,
			after: wantGone,
		},
		{
			name: "no lock, caminho é diretório: editado",
			setup: func(t *testing.T, p *project) {
				removeFile(t, p.root, reviewer)
				if err := os.Mkdir(filepath.Join(p.root, reviewer), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			rel: reviewer, want: Editado,
			after: func(t *testing.T, p *project, rel string) {
				if info, err := os.Lstat(filepath.Join(p.root, rel)); err != nil || !info.IsDir() {
					t.Errorf("diretório %s foi tocado: %v", rel, err)
				}
			},
		},
		{
			name:    "fora do lock, caminho é diretório: não gerenciado",
			modules: []string{"data-model"},
			setup: func(t *testing.T, p *project) {
				if err := os.MkdirAll(filepath.Join(p.root, dataModel), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			rel: dataModel, want: NaoGerenciado,
			after: func(t *testing.T, p *project, rel string) {
				if info, err := os.Lstat(filepath.Join(p.root, rel)); err != nil || !info.IsDir() {
					t.Errorf("diretório %s foi tocado: %v", rel, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			p.cfg = &config.Config{EnabledModules: tc.modules}
			if tc.setup != nil {
				tc.setup(t, p)
			}
			items := p.plan(t)
			if got := actionOf(t, items, tc.rel); got != tc.want {
				t.Fatalf("ação de %s = %q, quer %q", tc.rel, got, tc.want)
			}
			if it, _ := find(items, tc.rel); it.Action != Nada && it.Reason == "" {
				t.Errorf("item %s sem motivo", tc.rel)
			}
			before := lockEntry(p.lock, tc.rel)
			applied := p.apply(t, items)
			if got := actionOf(t, applied, tc.rel); got != tc.want {
				t.Errorf("ação aplicada de %s = %q, quer %q", tc.rel, got, tc.want)
			}
			tc.after(t, p, tc.rel)
			if p.lock.ToolkitVersion != newVersion {
				t.Errorf("toolkit_version = %q, quer %q", p.lock.ToolkitVersion, newVersion)
			}
			if tc.want == Editado || tc.want == NaoGerenciado {
				if got := lockEntry(p.lock, tc.rel); got != before {
					t.Errorf("entrada do lock de %s mudou: %q → %q", tc.rel, before, got)
				}
			}
		})
	}
}

func lockEntry(l *manifest.Lock, rel string) string {
	h, ok := l.Files[rel]
	if !ok {
		return "<ausente>"
	}
	return h
}

func removeFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

func release(p *project, rel string) {
	delete(p.lock.Files, rel)
	p.lock.Released = append(p.lock.Released, rel)
}

// wantRendered confere que o arquivo tem o conteúdo do catálogo com os
// módulos dados, o Mode do catálogo e entrada no lock com o hash dele.
func wantRendered(modules []string) func(t *testing.T, p *project, rel string) {
	return func(t *testing.T, p *project, rel string) {
		t.Helper()
		want := render(t, rel, modules)
		got, ok := readFile(t, p.root, rel)
		if !ok || !bytes.Equal(got, want) {
			t.Errorf("%s não tem o conteúdo do template", rel)
		}
		if h := p.lock.Files[rel]; h != manifest.Hash(want) {
			t.Errorf("lock de %s = %q, quer o hash do template", rel, h)
		}
	}
}

func wantContent(content []byte, inLock bool) func(t *testing.T, p *project, rel string) {
	return func(t *testing.T, p *project, rel string) {
		t.Helper()
		got, ok := readFile(t, p.root, rel)
		if !ok || !bytes.Equal(got, content) {
			t.Errorf("%s foi alterado: %q", rel, got)
		}
		if _, ok := p.lock.Files[rel]; ok != inLock {
			t.Errorf("%s no lock = %v, quer %v", rel, ok, inLock)
		}
	}
}

func wantReleased(content []byte) func(t *testing.T, p *project, rel string) {
	return func(t *testing.T, p *project, rel string) {
		t.Helper()
		got, ok := readFile(t, p.root, rel)
		if ok != (content != nil) || !bytes.Equal(got, content) {
			t.Errorf("%s liberado foi tocado: existe=%v %q", rel, ok, got)
		}
		if !slices.Contains(p.lock.Released, rel) {
			t.Errorf("%s saiu de released", rel)
		}
		if _, ok := p.lock.Files[rel]; ok {
			t.Errorf("%s liberado voltou para files", rel)
		}
	}
}

func wantGone(t *testing.T, p *project, rel string) {
	t.Helper()
	if _, ok := readFile(t, p.root, rel); ok {
		t.Errorf("%s não foi apagado", rel)
	}
	if _, ok := p.lock.Files[rel]; ok {
		t.Errorf("%s ficou no lock", rel)
	}
}

func TestPlanFreshProjectAllNada(t *testing.T) {
	for _, modules := range [][]string{nil, {"data-model", "tenant-isolation"}, allModules()} {
		p := newProject(t, modules...)
		// Arquivos do projeto e configuração reais: o plano não os vê.
		d := templates.Data{
			Name: "biblioteca-bairro", Module: "example.com/biblioteca-bairro",
			Summary: "API do acervo da biblioteca do bairro", Provider: "github",
			ToolkitVersion: testVersion, Modules: modules,
		}
		for _, f := range templates.Expected(templates.Project, modules) {
			dest, err := templates.DestPath(f, d)
			if err != nil {
				t.Fatal(err)
			}
			data, err := templates.Render(f, d)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, p.root, dest, data, f.Mode)
		}
		cfg, err := config.Load(p.root)
		if err != nil {
			t.Fatal(err)
		}
		items, err := Plan(p.root, cfg, p.lock)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != len(templates.Expected(templates.Managed, modules)) {
			t.Errorf("módulos %v: %d itens, quer %d", modules, len(items), len(templates.Expected(templates.Managed, modules)))
		}
		for _, it := range items {
			if it.Action != Nada {
				t.Errorf("módulos %v: %s = %q (%s), quer nada", modules, it.Path, it.Action, it.Reason)
			}
		}
	}
}

func allModules() []string {
	var out []string
	for m := range config.Modules {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func TestPlanSortedAndUnique(t *testing.T) {
	p := newProject(t, "release-ops")
	p.cfg = &config.Config{EnabledModules: []string{"data-model", "release-ops"}}
	p.lock.Released = append(p.lock.Released, oldSkill)
	p.lock.Files[".claude/skills/zz/SKILL.md"] = manifest.Hash([]byte("x"))
	items := p.plan(t)
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.Path
	}
	if !slices.IsSorted(paths) {
		t.Errorf("plano fora de ordem: %v", paths)
	}
	if len(slices.Compact(slices.Clone(paths))) != len(paths) {
		t.Errorf("caminho repetido no plano: %v", paths)
	}
}

func TestPlanDuplicateModulesInConfig(t *testing.T) {
	// A config não reprova módulo repetido; o plano não pode falhar nem
	// duplicar item por isso.
	p := newProject(t, "data-model")
	p.cfg = &config.Config{EnabledModules: []string{"data-model", "data-model"}}
	for _, it := range p.plan(t) {
		if it.Action != Nada {
			t.Errorf("%s = %q, quer nada", it.Path, it.Action)
		}
	}
}

func TestTemplateChangedUpdates(t *testing.T) {
	// Template "novo" simulado: o lock e o disco têm a versão anterior.
	p := newProject(t)
	old := []byte("#!/bin/sh\necho antigo\n")
	writeFile(t, p.root, prePush, old, 0o644)
	p.lock.Files[prePush] = manifest.Hash(old)
	items := p.plan(t)
	if got := actionOf(t, items, prePush); got != Atualizar {
		t.Fatalf("ação = %q, quer atualizar", got)
	}
	p.apply(t, items)
	wantRendered(nil)(t, p, prePush)
	info, err := os.Stat(filepath.Join(p.root, prePush))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("modo de %s = %v, quer 0755 do catálogo", prePush, info.Mode().Perm())
	}
}

func TestModuleToggle(t *testing.T) {
	t.Run("ligado na config: cria", func(t *testing.T) {
		p := newProject(t)
		p.cfg = &config.Config{EnabledModules: []string{"query-performance"}}
		items := p.plan(t)
		rel := ".claude/agents/query-performance.md"
		if got := actionOf(t, items, rel); got != Criar {
			t.Fatalf("ação = %q, quer criar", got)
		}
		p.apply(t, items)
		wantRendered([]string{"query-performance"})(t, p, rel)
	})
	t.Run("desligado e intacto: apaga", func(t *testing.T) {
		p := newProject(t, "query-performance")
		p.cfg = &config.Config{}
		rel := ".claude/agents/query-performance.md"
		items := p.plan(t)
		if got := actionOf(t, items, rel); got != Apagar {
			t.Fatalf("ação = %q, quer apagar", got)
		}
		p.apply(t, items)
		wantGone(t, p, rel)
		if _, err := os.Stat(filepath.Join(p.root, ".claude/agents")); err != nil {
			t.Errorf(".claude/agents sumiu: %v", err)
		}
	})
	t.Run("desligado e editado: editado", func(t *testing.T) {
		p := newProject(t, "query-performance")
		p.cfg = &config.Config{}
		rel := ".claude/agents/query-performance.md"
		writeFile(t, p.root, rel, []byte("ajustado\n"), 0o644)
		items := p.plan(t)
		if got := actionOf(t, items, rel); got != Editado {
			t.Fatalf("ação = %q, quer editado", got)
		}
		p.apply(t, items)
		wantContent([]byte("ajustado\n"), true)(t, p, rel)
	})
}

func TestApplyRechecksBeforeWrite(t *testing.T) {
	old := []byte("# anterior\n")
	cases := []struct {
		name    string
		modules []string
		setup   func(t *testing.T, p *project) // antes do plano
		between func(t *testing.T, p *project) // entre plano e Apply
		rel     string
		planned Action
		want    Action
		content []byte // conteúdo esperado no fim (nil = ausente)
	}{
		{
			name: "atualizar editado depois do plano",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, reviewer, old, 0o644)
				p.lock.Files[reviewer] = manifest.Hash(old)
			},
			between: func(t *testing.T, p *project) { writeFile(t, p.root, reviewer, []byte("meu\n"), 0o644) },
			rel:     reviewer, planned: Atualizar, want: Editado, content: []byte("meu\n"),
		},
		{
			name: "apagar editado depois do plano",
			setup: func(t *testing.T, p *project) {
				writeFile(t, p.root, oldSkill, old, 0o644)
				p.lock.Files[oldSkill] = manifest.Hash(old)
			},
			between: func(t *testing.T, p *project) { writeFile(t, p.root, oldSkill, []byte("meu\n"), 0o644) },
			rel:     oldSkill, planned: Apagar, want: Editado, content: []byte("meu\n"),
		},
		{
			name:    "recriar com arquivo novo depois do plano",
			setup:   func(t *testing.T, p *project) { removeFile(t, p.root, reviewer) },
			between: func(t *testing.T, p *project) { writeFile(t, p.root, reviewer, []byte("meu\n"), 0o644) },
			rel:     reviewer, planned: Recriar, want: Editado, content: []byte("meu\n"),
		},
		{
			name:    "criar com arquivo novo depois do plano",
			modules: []string{"data-model"},
			between: func(t *testing.T, p *project) { writeFile(t, p.root, dataModel, []byte("meu\n"), 0o644) },
			rel:     dataModel, planned: Criar, want: NaoGerenciado, content: []byte("meu\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			p.cfg = &config.Config{EnabledModules: tc.modules}
			if tc.setup != nil {
				tc.setup(t, p)
			}
			items := p.plan(t)
			if got := actionOf(t, items, tc.rel); got != tc.planned {
				t.Fatalf("plano = %q, quer %q", got, tc.planned)
			}
			before := lockEntry(p.lock, tc.rel)
			tc.between(t, p)
			applied := p.apply(t, items)
			if got := actionOf(t, applied, tc.rel); got != tc.want {
				t.Errorf("aplicado = %q, quer %q", got, tc.want)
			}
			got, ok := readFile(t, p.root, tc.rel)
			if ok != (tc.content != nil) || !bytes.Equal(got, tc.content) {
				t.Errorf("%s alterado: %q", tc.rel, got)
			}
			if after := lockEntry(p.lock, tc.rel); after != before {
				t.Errorf("entrada do lock mudou: %q → %q", before, after)
			}
		})
	}
}

func TestSymlinkOutsideNeverWritten(t *testing.T) {
	t.Run("diretório .claude/agents é symlink para fora", func(t *testing.T) {
		p := newProject(t)
		outside := t.TempDir()
		// Fora da raiz, uma cópia do diretório com o template "antigo".
		old := []byte("# antigo\n")
		agents := filepath.Join(p.root, ".claude", "agents")
		for _, f := range templates.Expected(templates.Managed, nil) {
			if strings.HasPrefix(f.Path, ".claude/agents/") {
				writeFile(t, outside, path.Base(f.Path), old, 0o644)
				p.lock.Files[f.Path] = manifest.Hash(old)
			}
		}
		if err := os.RemoveAll(agents); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, outside, agents)
		p.cfg = &config.Config{EnabledModules: []string{"data-model"}}
		before := snapshot(t, outside)

		items := p.plan(t)
		for _, it := range items {
			if strings.HasPrefix(it.Path, ".claude/agents/") && it.Action != Editado && it.Action != NaoGerenciado {
				t.Errorf("%s = %q através de symlink, quer editado ou não gerenciado", it.Path, it.Action)
			}
		}
		if _, err := Apply(p.root, items, p.lock, newVersion); err != nil {
			t.Logf("Apply recusou: %v", err)
		}
		if after := snapshot(t, outside); !mapsEqual(before, after) {
			t.Errorf("Apply gravou fora da raiz:\nantes %v\ndepois %v", before, after)
		}
	})
	t.Run("arquivo é symlink para fora com o conteúdo do lock", func(t *testing.T) {
		p := newProject(t)
		outside := t.TempDir()
		old := []byte("# antigo\n")
		writeFile(t, outside, "alvo.md", old, 0o644)
		removeFile(t, p.root, reviewer)
		symlinkOrSkip(t, filepath.Join(outside, "alvo.md"), filepath.Join(p.root, reviewer))
		p.lock.Files[reviewer] = manifest.Hash(old)
		before := snapshot(t, outside)
		items := p.plan(t)
		if got := actionOf(t, items, reviewer); got != Editado {
			t.Errorf("ação = %q, quer editado", got)
		}
		if _, err := Apply(p.root, items, p.lock, newVersion); err != nil {
			t.Logf("Apply recusou: %v", err)
		}
		if after := snapshot(t, outside); !mapsEqual(before, after) {
			t.Errorf("Apply gravou fora da raiz")
		}
	})
	t.Run("symlink criado entre plano e Apply", func(t *testing.T) {
		p := newProject(t)
		p.cfg = &config.Config{EnabledModules: []string{"data-model"}}
		removeFile(t, p.root, reviewer)
		items := p.plan(t)
		if got := actionOf(t, items, dataModel); got != Criar {
			t.Fatalf("plano = %q, quer criar", got)
		}
		outside := t.TempDir()
		agents := filepath.Join(p.root, ".claude", "agents")
		if err := os.RemoveAll(agents); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, outside, agents)
		applied, err := Apply(p.root, items, p.lock, newVersion)
		if err != nil {
			t.Logf("Apply recusou: %v", err)
		}
		for _, it := range applied {
			if strings.HasPrefix(it.Path, ".claude/agents/") && (it.Action == Criar || it.Action == Recriar || it.Action == Atualizar) {
				t.Errorf("%s aplicado como %q através de symlink", it.Path, it.Action)
			}
		}
		if after := snapshot(t, outside); len(after) != 1 { // só o próprio diretório
			t.Errorf("Apply gravou fora da raiz: %v", after)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestLockOutsideManagedArea(t *testing.T) {
	hash := manifest.Hash([]byte("x"))
	bad := []string{"cmd/biblioteca/main.go", "../fora.md", ".claude/agents/../../go.mod", "/etc/passwd",
		".claude/agents", ".Claude/agents/x.md", `.claude\agents\x.md`, ".claude/agents/./x.md", "Makefile"}
	t.Run("lock no disco é rejeitado na leitura", func(t *testing.T) {
		for _, b := range bad {
			root := t.TempDir()
			raw := `{"version":1,"toolkit_version":"v0.1.0","files":{` + jsonString(b) + `:"` + hash + `"},"released":[]}`
			if err := os.WriteFile(filepath.Join(root, manifest.FileName), []byte(raw+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := manifest.Read(root); err == nil {
				t.Errorf("%q: manifest.Read aceitou caminho fora da área", b)
			}
		}
	})
	for _, b := range bad {
		for _, field := range []string{"files", "released"} {
			t.Run(field+" "+b, func(t *testing.T) {
				p := newProject(t)
				p.cfg = &config.Config{EnabledModules: []string{"data-model"}}
				removeFile(t, p.root, reviewer)
				if field == "files" {
					p.lock.Files[b] = hash
				} else {
					p.lock.Released = append(p.lock.Released, b)
				}
				before := snapshot(t, p.root)
				if _, err := Plan(p.root, p.cfg, p.lock); err == nil {
					t.Errorf("Plan aceitou %q em %s", b, field)
				}
				// Apply com itens válidos e lock adulterado: nada gravado.
				good := newProject(t)
				good.cfg = p.cfg
				removeFile(t, good.root, reviewer)
				items := good.plan(t)
				if _, err := Apply(p.root, items, p.lock, newVersion); err == nil {
					t.Errorf("Apply aceitou lock com %q em %s", b, field)
				}
				if after := snapshot(t, p.root); !mapsEqual(before, after) {
					t.Errorf("nada deveria ter sido gravado")
				}
			})
		}
	}
}

func jsonString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func TestApplyFailsClosed(t *testing.T) {
	t.Run("item que não veio do plano", func(t *testing.T) {
		p := newProject(t)
		before := snapshot(t, p.root)
		for _, a := range []Action{Criar, Atualizar, Recriar, Apagar} {
			items := []Item{{Path: reviewer, Action: a}}
			if _, err := Apply(p.root, items, p.lock, newVersion); err == nil {
				t.Errorf("Apply aceitou %q montado à mão", a)
			}
		}
		if after := snapshot(t, p.root); !mapsEqual(before, after) {
			t.Errorf("nada deveria ter sido gravado")
		}
	})
	t.Run("item fora da área", func(t *testing.T) {
		p := newProject(t)
		items := p.plan(t)
		items = append(items, Item{Path: "go.mod", Action: Nada})
		before := snapshot(t, p.root)
		if _, err := Apply(p.root, items, p.lock, newVersion); err == nil {
			t.Error("Apply aceitou item fora da área gerenciada")
		}
		if after := snapshot(t, p.root); !mapsEqual(before, after) {
			t.Errorf("nada deveria ter sido gravado")
		}
	})
	t.Run("item repetido", func(t *testing.T) {
		p := newProject(t)
		items := p.plan(t)
		items = append(items, items[0])
		if _, err := Apply(p.root, items, p.lock, newVersion); err == nil {
			t.Error("Apply aceitou item repetido")
		}
	})
	t.Run("versão vazia", func(t *testing.T) {
		p := newProject(t)
		removeFile(t, p.root, reviewer)
		items := p.plan(t)
		before := snapshot(t, p.root)
		if _, err := Apply(p.root, items, p.lock, " "); err == nil {
			t.Error("Apply aceitou versão vazia")
		}
		if after := snapshot(t, p.root); !mapsEqual(before, after) {
			t.Errorf("nada deveria ter sido gravado")
		}
	})
	t.Run("lock nil e config nil", func(t *testing.T) {
		p := newProject(t)
		if _, err := Plan(p.root, p.cfg, nil); err == nil {
			t.Error("Plan aceitou lock nil")
		}
		if _, err := Plan(p.root, nil, p.lock); err == nil {
			t.Error("Plan aceitou config nil")
		}
		if _, err := Apply(p.root, nil, nil, newVersion); err == nil {
			t.Error("Apply aceitou lock nil")
		}
	})
	t.Run("hash inválido no lock em memória", func(t *testing.T) {
		p := newProject(t)
		p.lock.Files[reviewer] = "xyz"
		if _, err := Plan(p.root, p.cfg, p.lock); err == nil {
			t.Error("Plan aceitou hash inválido")
		}
	})
	t.Run("mesmo caminho em files e released", func(t *testing.T) {
		p := newProject(t)
		p.lock.Released = append(p.lock.Released, reviewer)
		if _, err := Plan(p.root, p.cfg, p.lock); err == nil {
			t.Error("Plan aceitou caminho em files e released")
		}
	})
}

func TestCRLFIsNotEdited(t *testing.T) {
	p := newProject(t)
	data := render(t, reviewer, nil)
	writeFile(t, p.root, reviewer, bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n")), 0o644)
	items := p.plan(t)
	if got := actionOf(t, items, reviewer); got != Nada {
		t.Errorf("CRLF no disco = %q, quer nada", got)
	}
}

func TestReleaseThenPlan(t *testing.T) {
	p := newProject(t)
	edited := []byte("# do projeto agora\n")
	writeFile(t, p.root, reviewer, edited, 0o644)
	if got := actionOf(t, p.plan(t), reviewer); got != Editado {
		t.Fatalf("antes do release = %q, quer editado", got)
	}
	if err := Release(p.root, []string{reviewer}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readFile(t, p.root, reviewer); !bytes.Equal(got, edited) {
		t.Error("Release tocou no arquivo")
	}
	p.lock = readLock(t, p.root)
	if !slices.Contains(p.lock.Released, reviewer) {
		t.Errorf("released = %v", p.lock.Released)
	}
	if _, ok := p.lock.Files[reviewer]; ok {
		t.Error("caminho liberado ficou em files")
	}
	items := p.plan(t)
	if got := actionOf(t, items, reviewer); got != Liberado {
		t.Errorf("depois do release = %q, quer liberado", got)
	}
	p.apply(t, items)
	wantReleased(edited)(t, p, reviewer)
}

func TestReleaseErrors(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		setup func(t *testing.T, root string)
	}{
		{name: "sem caminho", paths: nil},
		{name: "fora de files", paths: []string{".claude/agents/data-model.md"}},
		{name: "fora da área", paths: []string{"go.mod"}},
		{name: "um válido e um inválido", paths: []string{reviewer, "go.mod"}},
		{name: "já liberado", paths: []string{reviewer}, setup: func(t *testing.T, root string) {
			if err := Release(root, []string{reviewer}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "sem lock", paths: []string{reviewer}, setup: func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, manifest.FileName)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			if tc.setup != nil {
				tc.setup(t, p.root)
			}
			before := snapshot(t, p.root)
			if err := Release(p.root, tc.paths); err == nil {
				t.Error("Release aceitou")
			}
			if after := snapshot(t, p.root); !mapsEqual(before, after) {
				t.Error("Release com erro gravou algo")
			}
		})
	}
	t.Run("caminho do sistema operacional", func(t *testing.T) {
		p := newProject(t)
		if err := Release(p.root, []string{filepath.FromSlash("./" + reviewer)}); err != nil {
			t.Fatal(err)
		}
		if l := readLock(t, p.root); !slices.Contains(l.Released, reviewer) {
			t.Errorf("released = %v", l.Released)
		}
	})
}

func TestApplyIdempotent(t *testing.T) {
	p := newProject(t, "release-ops")
	p.cfg = &config.Config{EnabledModules: []string{"data-model"}}
	removeFile(t, p.root, prePush)
	old := []byte("# antigo\n")
	writeFile(t, p.root, reviewer, old, 0o644)
	p.lock.Files[reviewer] = manifest.Hash(old)

	p.apply(t, p.plan(t))
	first := lockBytes(t, p.root)
	disk := snapshot(t, p.root)

	items := p.plan(t)
	for _, it := range items {
		if it.Action != Nada {
			t.Errorf("segundo plano: %s = %q", it.Path, it.Action)
		}
	}
	p.apply(t, items)
	if !bytes.Equal(first, lockBytes(t, p.root)) {
		t.Error("segundo Apply mudou os bytes do lock")
	}
	if !mapsEqual(disk, snapshot(t, p.root)) {
		t.Error("segundo Apply mudou o disco")
	}
}

// Casos portados de pipeline-ai/internal/scaffold/sync_test.go (TestSyncAgents).
func TestSyncPortedCases(t *testing.T) {
	t.Run("diretório sem projeto falha", func(t *testing.T) {
		// Original: Sync sem .agents/ retorna erro. Aqui o marcador do projeto é
		// o lock (TD-23): sem lock não há plano (TD-25, exit 2 na CLI).
		root := t.TempDir()
		if _, err := manifest.Read(root); !errors.Is(err, manifest.ErrNotFound) {
			t.Fatalf("manifest.Read = %v, quer ErrNotFound", err)
		}
		if _, err := Plan(root, &config.Config{}, nil); err == nil {
			t.Error("Plan sem lock deveria falhar")
		}
		if _, err := Plan(filepath.Join(root, "nao-existe"), &config.Config{}, &manifest.Lock{Version: 1, ToolkitVersion: testVersion}); err == nil {
			t.Error("Plan em raiz inexistente deveria falhar")
		}
	})
	t.Run("regra customizada e código de negócio ficam intactos", func(t *testing.T) {
		p := newProject(t)
		custom := ".claude/agents/minha-regra-customizada.md"
		writeFile(t, p.root, custom, []byte("# Regra do Usuário"), 0o644)
		writeFile(t, p.root, "main.go", []byte("package main\nfunc main() {}\n"), 0o644)
		// Original: o sync sobrescrevia tudo de .agents/. Aqui (TD-25) só o
		// que está no catálogo ou no lock entra no plano.
		removeFile(t, p.root, reviewer)
		items := p.plan(t)
		if _, ok := find(items, custom); ok {
			t.Errorf("arquivo customizado entrou no plano")
		}
		p.apply(t, items)
		if got, _ := readFile(t, p.root, custom); string(got) != "# Regra do Usuário" {
			t.Errorf("sync apagou ou corrompeu regra customizada: %q", got)
		}
		if got, _ := readFile(t, p.root, "main.go"); string(got) != "package main\nfunc main() {}\n" {
			t.Errorf("sync modificou código de negócio")
		}
		// Original: as skills oficiais existem depois do sync.
		wantRendered(nil)(t, p, reviewer)
		for _, f := range templates.Expected(templates.Managed, nil) {
			if _, ok := readFile(t, p.root, f.Path); !ok {
				t.Errorf("sync não deixou %s", f.Path)
			}
		}
	})
}

// FuzzPlan: para lock e disco arbitrários dentro de uma raiz temporária, Plan
// nunca entra em pânico e nunca propõe caminho fora da área gerenciada.
func FuzzPlan(f *testing.F) {
	f.Add(reviewer+"\n"+oldSkill, "", reviewer+"\n.claude/skills/x/SKILL.md", []byte("x"), uint8(0), false)
	f.Add("../fora.md", "go.mod", "../fora.md", []byte("x"), uint8(3), true)
	f.Add(dataModel, prePush, dataModel+"\n"+prePush, []byte("\r\n"), uint8(255), true)
	f.Add(".claude/agents/a.md\n.claude/agents/a.md", ".claude/agents/a.md", ".claude/agents", []byte{}, uint8(1), false)
	f.Fuzz(func(t *testing.T, lockPaths, released, diskPaths string, content []byte, mods uint8, hashOfContent bool) {
		root := t.TempDir()
		for _, p := range strings.Split(diskPaths, "\n") {
			if !filepath.IsLocal(p) || strings.Contains(p, "\x00") {
				continue
			}
			full := filepath.Join(root, p)
			if os.MkdirAll(filepath.Dir(full), 0o755) != nil {
				continue
			}
			_ = os.WriteFile(full, content, 0o644)
		}
		h := manifest.Hash([]byte("outro"))
		if hashOfContent {
			h = manifest.Hash(content)
		}
		lock := &manifest.Lock{Version: 1, ToolkitVersion: testVersion, Files: map[string]string{}}
		for _, p := range strings.Split(lockPaths, "\n") {
			if p != "" {
				lock.Files[p] = h
			}
		}
		for _, p := range strings.Split(released, "\n") {
			if p != "" {
				lock.Released = append(lock.Released, p)
			}
		}
		all := allModules()
		var modules []string
		for i, m := range all {
			if mods&(1<<i) != 0 {
				modules = append(modules, m)
			}
		}
		items, err := Plan(root, &config.Config{EnabledModules: modules}, lock)
		if err != nil {
			return
		}
		for _, it := range items {
			if !manifest.ManagedArea(it.Path) || path.Clean(it.Path) != it.Path || strings.Contains(it.Path, "..") {
				t.Fatalf("plano propôs %q fora da área gerenciada", it.Path)
			}
			full := filepath.Clean(filepath.Join(root, filepath.FromSlash(it.Path)))
			if rel, err := filepath.Rel(root, full); err != nil || !filepath.IsLocal(rel) {
				t.Fatalf("plano propôs %q fora da raiz", it.Path)
			}
		}
	})
}

// Defesas que o plano já cobre, testadas direto: valem mesmo se o disco mudar
// entre a classificação e a escrita.
func TestWriteGuards(t *testing.T) {
	t.Run("replaceFile recusa diretório symlink", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, outside, filepath.Join(root, ".claude", "agents"))
		if err := replaceFile(root, ".claude/agents/x.md", []byte("x"), 0o644); err == nil {
			t.Error("replaceFile gravou através de symlink")
		}
		if got := snapshot(t, outside); len(got) != 1 {
			t.Errorf("gravou fora da raiz: %v", got)
		}
	})
	t.Run("replaceFile recusa destino symlink", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "alvo.md")
		writeFile(t, outside, "alvo.md", []byte("fora"), 0o644)
		if err := os.MkdirAll(filepath.Join(root, ".claude", "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		symlinkOrSkip(t, target, filepath.Join(root, ".claude", "agents", "x.md"))
		if err := replaceFile(root, ".claude/agents/x.md", []byte("x"), 0o644); err == nil {
			t.Error("replaceFile substituiu symlink")
		}
		if got, _ := readFile(t, outside, "alvo.md"); string(got) != "fora" {
			t.Errorf("alvo fora da raiz alterado: %q", got)
		}
	})
	t.Run("confine recusa saída da raiz", func(t *testing.T) {
		root := t.TempDir()
		for _, rel := range []string{"../fora.md", ".claude/../../fora.md", ".", ""} {
			if _, err := confine(root, rel); err == nil {
				t.Errorf("confine aceitou %q", rel)
			}
			if err := replaceFile(root, rel, []byte("x"), 0o644); err == nil {
				t.Errorf("replaceFile aceitou %q", rel)
			}
			if err := deleteFile(root, rel); err == nil {
				t.Errorf("deleteFile aceitou %q", rel)
			}
		}
	})
	t.Run("deleteFile só remove diretório vazio dentro da área", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, ".claude/skills/x/SKILL.md", []byte("x"), 0o644)
		if err := deleteFile(root, ".claude/skills/x/SKILL.md"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(root, ".claude/skills/x")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf(".claude/skills/x ficou: %v", err)
		}
		for _, dir := range []string{".claude/skills", ".claude"} {
			if _, err := os.Lstat(filepath.Join(root, dir)); err != nil {
				t.Errorf("%s fora da área foi removido: %v", dir, err)
			}
		}
	})
}
