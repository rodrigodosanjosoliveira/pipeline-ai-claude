// Portado de github.com/rodrigodosanjosoliveira/pipeline-ai/internal/scaffold/sync.go @ 0eac73a (MIT).
// Alterações: plano por arquivo antes de gravar, com a tabela do TD-25 (só
// atualiza o que o toolkit gerou e o projeto não editou, conferido pelo hash do
// lock, TD-15 e TD-23) no lugar de sobrescrever tudo de .agents/; arquivos do
// catálogo gerenciado (TD-22) em .claude/ e .githooks/ (TD-26); disco conferido
// de novo antes de cada escrita; temporário + rename com o Mode do catálogo;
// symlink recusado em qualquer componente e destino confinado à raiz (TD-17);
// lock com caminho fora da área reprova tudo (TD-08); Release (--release); sem
// saída em io.Writer: o chamador apresenta os itens.

// Package syncer implementa o pipeline-claude sync: atualiza os arquivos
// gerenciados (TD-15) de um projeto a partir do catálogo de templates, sem
// tocar no que o projeto editou, assumiu (released) ou criou por conta própria
// (TD-25).
//
// Plan compara, para cada arquivo gerenciado esperado e cada entrada do lock,
// o disco, o lock e o template, e devolve um Item por caminho. Apply grava só
// criar, atualizar, recriar e apagar, conferindo o disco de novo logo antes de
// cada escrita, e reescreve o lock. Release passa caminhos do lock para
// released. Nada é gravado fora da área gerenciada nem através de symlink.
package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/manifest"
	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/templates"
)

// Action é o que o sync faz com um caminho (TD-25). Os valores são os que a
// CLI mostra e põe no JSON.
type Action string

// Ações do plano.
const (
	// Nada: o arquivo está igual ao lock e ao template.
	Nada Action = "nada"
	// Criar: esperado, fora do lock e ausente no disco.
	Criar Action = "criar"
	// Atualizar: no lock, intacto, e o template mudou.
	Atualizar Action = "atualizar"
	// Recriar: no lock e ausente no disco (restaura um editado).
	Recriar Action = "recriar"
	// Apagar: no lock, fora do esperado (template removido ou módulo
	// desligado) e intacto, ou já ausente no disco (só sai do lock).
	Apagar Action = "apagar"
	// Editado: no lock e diferente dele no disco; não é tocado.
	Editado Action = "editado"
	// NaoGerenciado: esperado, fora do lock e já existente no disco; não é
	// tocado.
	NaoGerenciado Action = "nao-gerenciado"
	// Liberado: em released; nunca é tocado.
	Liberado Action = "liberado"
)

// writes diz se a ação grava ou apaga no disco.
func (a Action) writes() bool {
	return a == Criar || a == Atualizar || a == Recriar || a == Apagar
}

// Item é a decisão para um caminho.
type Item struct {
	// Path é o caminho relativo à raiz, com "/", dentro da área gerenciada.
	Path string `json:"path"`
	// Action é a ação.
	Action Action `json:"action"`
	// Reason explica a ação; vazio só em Nada.
	Reason string `json:"reason,omitempty"`

	// Preenchidos por Plan; Apply recusa item que grava sem eles.
	planned  bool
	expected bool        // está em Expected(Managed, enabled_modules)
	content  []byte      // conteúdo renderizado, se expected
	mode     fs.FileMode // Mode do catálogo, se expected
}

// Plan monta o plano do sync para o projeto em root: um Item por arquivo
// gerenciado esperado (catálogo + cfg.EnabledModules) e por entrada do lock
// (files e released), ordenado por caminho. Não grava nada. Lock com caminho
// fora da área gerenciada, hash inválido ou caminho em files e released é erro
// (TD-23, TD-08), como erro de acesso ao disco.
func Plan(root string, cfg *config.Config, lock *manifest.Lock) ([]Item, error) {
	if cfg == nil {
		return nil, errors.New("sync: configuração ausente")
	}
	if lock == nil {
		return nil, errors.New("sync: lock ausente")
	}
	root, err := checkRoot(root)
	if err != nil {
		return nil, err
	}
	if err := checkLock(lock); err != nil {
		return nil, err
	}

	// A config já recusa módulo repetido (TD-28); uma config montada sem
	// config.Load também reprova aqui, antes da renderização.
	modules := slices.Clone(cfg.EnabledModules)
	slices.Sort(modules)
	for i := 1; i < len(modules); i++ {
		if modules[i] == modules[i-1] {
			return nil, fmt.Errorf("sync: enabled_modules: módulo %q repetido", modules[i])
		}
	}

	expected := map[string]Item{}
	for _, f := range templates.Expected(templates.Managed, modules) {
		if err := checkPath(f.Path); err != nil {
			return nil, fmt.Errorf("sync: catálogo: %w", err)
		}
		content, err := templates.Render(f, templates.Data{Modules: modules})
		if err != nil {
			return nil, fmt.Errorf("sync: %w", err)
		}
		expected[f.Path] = Item{Path: f.Path, planned: true, expected: true, content: content, mode: f.Mode}
	}

	paths := make([]string, 0, len(expected)+len(lock.Files)+len(lock.Released))
	for p := range expected {
		paths = append(paths, p)
	}
	for p := range lock.Files {
		paths = append(paths, p)
	}
	paths = append(paths, lock.Released...)
	slices.Sort(paths)
	paths = slices.Compact(paths)

	items := make([]Item, 0, len(paths))
	for _, p := range paths {
		it, ok := expected[p]
		if !ok {
			it = Item{Path: p, planned: true}
		}
		it.Action, it.Reason, err = classify(root, it, lock)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, nil
}

// classify aplica a tabela do TD-25 ao caminho de it, lendo o disco agora.
func classify(root string, it Item, lock *manifest.Lock) (Action, string, error) {
	if slices.Contains(lock.Released, it.Path) {
		return Liberado, "assumido pelo projeto (released)", nil
	}
	lockHash, inLock := lock.Files[it.Path]
	st, err := inspect(root, it.Path)
	if err != nil {
		return "", "", err
	}

	if !inLock {
		switch {
		case !it.expected:
			return "", "", fmt.Errorf("sync: %s não é esperado nem está no lock", it.Path)
		case st.kind == absent:
			return Criar, "novo no catálogo", nil
		case st.kind == other:
			return NaoGerenciado, st.reason + "; fora do lock", nil
		}
		return NaoGerenciado, "existe no disco e não está no lock", nil
	}

	switch {
	case st.kind == other:
		return Editado, st.reason, nil
	case st.kind == absent && it.expected:
		return Recriar, "ausente no disco", nil
	case st.kind == absent:
		return Apagar, "fora do catálogo e já ausente no disco; sai do lock", nil
	case manifest.Hash(st.content) != lockHash:
		if it.expected {
			return Editado, "diferente do lock (editado pelo projeto)", nil
		}
		return Editado, "fora do catálogo e diferente do lock; use --release para assumir", nil
	case !it.expected:
		return Apagar, "fora do catálogo (template removido ou módulo desligado)", nil
	case manifest.Hash(it.content) != lockHash:
		return Atualizar, "template mudou", nil
	}
	return Nada, "", nil
}

type kind int

const (
	absent kind = iota
	regular
	other
)

// state é o que há no disco num caminho.
type state struct {
	kind    kind
	content []byte // se regular
	reason  string // se other
}

// inspect olha o caminho rel (já validado) sob root sem seguir symlink:
// Lstat em cada componente. Symlink em qualquer componente, componente
// intermediário que não é diretório ou destino que não é arquivo regular dão
// other. Erro de acesso é erro (falha fechada).
func inspect(root, rel string) (state, error) {
	parts := strings.Split(rel, "/")
	cur := root
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		shown := strings.Join(parts[:i+1], "/")
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return state{kind: absent}, nil
		case err != nil:
			return state{}, fmt.Errorf("sync: falha ao verificar %s: %w", shown, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return state{kind: other, reason: shown + " é symlink"}, nil
		case i < len(parts)-1 && !info.IsDir():
			return state{kind: other, reason: shown + " não é diretório"}, nil
		case i == len(parts)-1 && !info.Mode().IsRegular():
			return state{kind: other, reason: "não é arquivo regular"}, nil
		}
	}
	data, err := os.ReadFile(cur)
	if err != nil {
		return state{}, fmt.Errorf("sync: falha ao ler %s: %w", rel, err)
	}
	return state{kind: regular, content: data}, nil
}

// Apply executa os itens de um plano de Plan em root e devolve os itens como
// ficaram, ordenados por caminho. Só criar, atualizar, recriar e apagar
// gravam; logo antes de cada escrita o caminho é classificado de novo, e se o
// disco mudou desde o plano o item vira o que a tabela do TD-25 diz agora
// (editado e não gerenciado não são tocados). No fim o lock é reescrito com
// toolkit_version = version e os hashes do que foi aplicado; os demais
// caminhos mantêm a entrada que tinham (ou nenhuma). lock não é alterado.
//
// Item que grava sem ter vindo de Plan, item fora da área gerenciada, caminho
// repetido, versão vazia ou lock inválido reprovam antes de qualquer escrita.
// Erro no meio ainda grava o lock com o que já foi aplicado (e a versão
// anterior), para o próximo plano não tomar o aplicado por editado.
func Apply(root string, items []Item, lock *manifest.Lock, version string) (_ []Item, err error) {
	if lock == nil {
		return nil, errors.New("sync: lock ausente; nada foi gravado")
	}
	if strings.TrimSpace(version) == "" {
		return nil, errors.New("sync: versão do toolkit vazia; nada foi gravado")
	}
	root, err = checkRoot(root)
	if err != nil {
		return nil, err
	}
	if err := checkLock(lock); err != nil {
		return nil, err
	}
	out := slices.Clone(items)
	slices.SortFunc(out, func(a, b Item) int { return strings.Compare(a.Path, b.Path) })
	for i, it := range out {
		if err := checkPath(it.Path); err != nil {
			return nil, fmt.Errorf("sync: item inválido, nada foi gravado: %w", err)
		}
		if i > 0 && out[i-1].Path == it.Path {
			return nil, fmt.Errorf("sync: %s repetido no plano; nada foi gravado", it.Path)
		}
		if it.Action.writes() && !it.planned {
			return nil, fmt.Errorf("sync: item %s (%s) não veio de Plan; nada foi gravado", it.Path, it.Action)
		}
	}

	next := &manifest.Lock{
		Version:        manifest.Version,
		ToolkitVersion: lock.ToolkitVersion,
		Files:          make(map[string]string, len(lock.Files)),
		Released:       slices.Clone(lock.Released),
	}
	for p, h := range lock.Files {
		next.Files[p] = h
	}
	defer func() {
		if err == nil {
			next.ToolkitVersion = version
		}
		if werr := manifest.Write(root, next); werr != nil {
			err = errors.Join(err, fmt.Errorf("sync: %w", werr))
		}
	}()

	for i := range out {
		it := &out[i]
		if !it.Action.writes() {
			continue
		}
		// Conferência de novo, logo antes de gravar.
		action, reason, err := classify(root, *it, lock)
		if err != nil {
			return nil, err
		}
		it.Action, it.Reason = action, reason
		switch action {
		case Criar, Atualizar, Recriar:
			if err := replaceFile(root, it.Path, it.content, it.mode); err != nil {
				return nil, err
			}
			next.Files[it.Path] = manifest.Hash(it.content)
		case Apagar:
			if err := deleteFile(root, it.Path); err != nil {
				return nil, err
			}
			delete(next.Files, it.Path)
		}
	}
	return out, nil
}

// replaceFile grava data em rel sob root por temporário + rename, com mode.
// Cria os diretórios que faltam, recusando symlink ou não diretório em
// qualquer componente.
func replaceFile(root, rel string, data []byte, mode fs.FileMode) error {
	dest, err := confine(root, rel)
	if err != nil {
		return err
	}
	if err := mkdirs(root, path.Dir(rel)); err != nil {
		return err
	}
	info, err := os.Lstat(dest)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("sync: %s não é arquivo regular; nada foi gravado", rel)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("sync: falha ao verificar %s: %w", rel, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".pipeline-claude-sync-*")
	if err != nil {
		return fmt.Errorf("sync: falha ao criar temporário para %s: %w", rel, err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(mode)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	werr = errors.Join(werr, tmp.Close())
	if werr == nil {
		werr = os.Rename(name, dest)
	}
	if werr != nil {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			werr = errors.Join(werr, rerr)
		}
		return fmt.Errorf("sync: falha ao gravar %s: %w", rel, werr)
	}
	return nil
}

// mkdirs garante os diretórios de relDir sob root, componente a componente,
// sem seguir symlink.
func mkdirs(root, relDir string) error {
	cur := root
	for _, part := range strings.Split(relDir, "/") {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return fmt.Errorf("sync: falha ao criar %s: %w", cur, err)
			}
		case err != nil:
			return fmt.Errorf("sync: falha ao verificar %s: %w", cur, err)
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("sync: %s é symlink; nada foi gravado através dele", cur)
		case !info.IsDir():
			return fmt.Errorf("sync: %s não é diretório", cur)
		}
	}
	return nil
}

// deleteFile apaga rel sob root (ausente não é erro) e remove os diretórios
// que ficaram vazios, só dentro da área gerenciada.
func deleteFile(root, rel string) error {
	dest, err := confine(root, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("sync: falha ao apagar %s: %w", rel, err)
	}
	for dir := path.Dir(rel); manifest.ManagedArea(dir); dir = path.Dir(dir) {
		full, err := confine(root, dir)
		if err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("sync: falha ao verificar %s: %w", dir, err)
		}
		if !info.IsDir() {
			return nil
		}
		entries, err := os.ReadDir(full)
		if err != nil {
			return fmt.Errorf("sync: falha ao ler %s: %w", dir, err)
		}
		if len(entries) > 0 {
			return nil
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("sync: falha ao remover o diretório vazio %s: %w", dir, err)
		}
	}
	return nil
}

// Release passa os caminhos dados de files para released no lock de root: o
// sync não os toca mais. Só aceita caminho que esteja em files; qualquer erro
// reprova tudo e nada é gravado. Nunca toca nos arquivos.
func Release(root string, paths []string) error {
	if len(paths) == 0 {
		return errors.New("sync: --release sem caminho")
	}
	lock, err := manifest.Read(root)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	for _, raw := range paths {
		p := path.Clean(filepath.ToSlash(raw))
		if err := checkPath(p); err != nil {
			return fmt.Errorf("sync: --release %q: %w; nada foi gravado", raw, err)
		}
		if slices.Contains(lock.Released, p) {
			return fmt.Errorf("sync: --release %q: já está liberado; nada foi gravado", raw)
		}
		if _, ok := lock.Files[p]; !ok {
			return fmt.Errorf("sync: --release %q: não está no lock; nada foi gravado", raw)
		}
		delete(lock.Files, p)
		lock.Released = append(lock.Released, p)
	}
	if err := manifest.Write(root, lock); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// checkRoot devolve root absoluto e limpo, exigindo um diretório existente.
func checkRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("sync: raiz do projeto vazia")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("sync: raiz %s: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("sync: raiz %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sync: raiz %s não é diretório", root)
	}
	return abs, nil
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// checkLock refaz, sobre o lock em memória, as regras de manifest.Read que
// limitam o que o sync pode tocar: caminhos válidos dentro da área, hashes no
// formato e nenhum caminho em files e released.
func checkLock(l *manifest.Lock) error {
	for p, h := range l.Files {
		if err := checkPath(p); err != nil {
			return fmt.Errorf("sync: lock inválido: files: %w", err)
		}
		if !hashRe.MatchString(h) {
			return fmt.Errorf("sync: lock inválido: files: %q: hash %q fora do formato", p, h)
		}
	}
	for _, p := range l.Released {
		if err := checkPath(p); err != nil {
			return fmt.Errorf("sync: lock inválido: released: %w", err)
		}
		if _, ok := l.Files[p]; ok {
			return fmt.Errorf("sync: lock inválido: %q em files e released", p)
		}
	}
	return nil
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:`)

// checkPath exige caminho relativo, com "/", limpo, sem "..", e dentro da área
// gerenciada.
func checkPath(p string) error {
	switch {
	case p == "":
		return errors.New("caminho vazio")
	case strings.ContainsFunc(p, unicode.IsControl):
		return fmt.Errorf("caminho %q tem caractere de controle", p)
	case strings.Contains(p, `\`):
		return fmt.Errorf(`caminho %q usa "\"`, p)
	case strings.HasPrefix(p, "/"), driveRe.MatchString(p):
		return fmt.Errorf("caminho %q é absoluto", p)
	case path.Clean(p) != p:
		return fmt.Errorf("caminho %q não está na forma limpa", p)
	case slices.Contains(strings.Split(p, "/"), ".."):
		return fmt.Errorf("caminho %q sai da raiz do projeto", p)
	case !manifest.ManagedArea(p):
		return fmt.Errorf("caminho %q fora da área gerenciada", p)
	}
	return nil
}

// confine devolve o caminho de rel sob root, conferido dentro de root depois
// de filepath.Clean (TD-17).
func confine(root, rel string) (string, error) {
	p := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	r, err := filepath.Rel(root, p)
	if err != nil || r == "." || !filepath.IsLocal(r) {
		return "", fmt.Errorf("sync: %s sai da raiz do projeto; nada foi gravado", rel)
	}
	return p, nil
}
