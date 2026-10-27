// Package manifest lê e grava o .pipeline-claude.lock, o registro dos arquivos
// que o toolkit gerou e do hash de cada um (TD-23).
//
// O lock decide o que o sync pode sobrescrever ou apagar (TD-25), então a
// leitura é estrita: campo desconhecido, versão diferente, hash fora do formato
// ou caminho fora da área gerenciada reprovam o arquivo inteiro. Um lock
// adulterado não pode levar o sync a gravar fora do que o toolkit gera.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// FileName é o nome do lock, na raiz do projeto.
const FileName = ".pipeline-claude.lock"

// Version é a única versão de formato aceita.
const Version = 1

// ErrNotFound indica que o projeto não tem lock.
var ErrNotFound = errors.New("manifest: " + FileName + " não encontrado")

// managedPrefixes são as áreas que o toolkit gera e atualiza (TD-15). Fora
// delas, os arquivos são do projeto. A comparação diferencia caixa: o lock é
// gerado pelo toolkit, sempre em minúsculas.
var managedPrefixes = []string{".claude/agents/", ".claude/skills/", ".githooks/"}

// Lock é o conteúdo do lock.
type Lock struct {
	// Version é a versão do formato; sempre 1.
	Version int `json:"version"`
	// ToolkitVersion é a versão do toolkit que gravou o lock.
	ToolkitVersion string `json:"toolkit_version"`
	// Files leva cada arquivo gerado (caminho relativo à raiz, com "/") ao
	// sha256 em hex do conteúdo gravado, calculado por Hash.
	Files map[string]string `json:"files"`
	// Released são os caminhos que o projeto assumiu: o sync não os toca mais.
	Released []string `json:"released"`
}

var (
	hashRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	driveRe = regexp.MustCompile(`^[A-Za-z]:`)
)

// Read lê e valida o lock na raiz root. Devolve ErrNotFound se o lock não
// existir, e erro se ele não for arquivo regular (diretório, symlink) ou não
// passar na validação. No lock devolvido, Files e Released nunca são nil e
// Released está ordenado e sem repetição.
func Read(root string) (*Lock, error) {
	file := filepath.Join(root, FileName)
	info, err := os.Lstat(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("manifest: falha ao verificar %s: %w", file, err)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("manifest: %s não é um arquivo regular", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("manifest: falha ao ler %s: %w", file, err)
	}
	l, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("manifest: %s: %w", file, err)
	}
	return l, nil
}

// decode interpreta e valida o conteúdo do lock e devolve a forma normalizada.
func decode(data []byte) (*Lock, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Lock
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON inválido: conteúdo depois do objeto do lock")
	}
	// encoding/json fica com o último valor de uma chave repetida; num lock
	// isso esconde uma das entradas, então reprova.
	if err := checkDuplicateKeys(data); err != nil {
		return nil, err
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	return l.normalized(), nil
}

// Write valida l e grava o lock na raiz root, por arquivo temporário no mesmo
// diretório + rename. Lock inválido nunca vai para o disco, e um lock existente
// que não seja arquivo regular não é substituído. A saída é determinística:
// JSON com indentação de duas posições, chaves de files ordenadas, released
// ordenado e sem repetição, LF e quebra de linha final. l não é alterado.
func Write(root string, l *Lock) (err error) {
	if l == nil {
		return errors.New("manifest: lock nil")
	}
	if err := l.validate(); err != nil {
		return fmt.Errorf("manifest: lock inválido, nada foi gravado: %w", err)
	}
	data, err := encode(l.normalized())
	if err != nil {
		return fmt.Errorf("manifest: falha ao serializar o lock: %w", err)
	}

	file := filepath.Join(root, FileName)
	info, err := os.Lstat(file)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("manifest: %s não é um arquivo regular; nada foi gravado", file)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("manifest: falha ao verificar %s: %w", file, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(file), "."+FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("manifest: falha ao criar temporário para %s: %w", file, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			if rerr := os.Remove(tmpName); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("manifest: falha ao remover o temporário %s: %w", tmpName, rerr))
			}
		}
	}()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(0o644)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if err := errors.Join(werr, tmp.Close()); err != nil {
		return fmt.Errorf("manifest: falha ao gravar temporário para %s: %w", file, err)
	}
	if err := os.Rename(tmpName, file); err != nil {
		return fmt.Errorf("manifest: falha ao substituir %s: %w", file, err)
	}
	return nil
}

// encode serializa l já normalizado. encoding/json ordena as chaves de mapa e
// segue a ordem dos campos da struct; Encode termina com "\n".
func encode(l *Lock) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(l); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash devolve o sha256 em hex minúsculo de content, com cada CRLF trocado por
// LF antes: um checkout com autocrlf no Windows não pode parecer arquivo
// editado. CR sozinho fica como está.
func Hash(content []byte) string {
	sum := sha256.Sum256(bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// ManagedArea diz se rel (relativo à raiz, com "/") é um arquivo dentro da
// área gerenciada: .claude/agents/, .claude/skills/ ou .githooks/. O prefixo
// sozinho não conta, e a comparação diferencia caixa.
func ManagedArea(rel string) bool {
	for _, p := range managedPrefixes {
		if len(rel) > len(p) && strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// validate confere versão, toolkit_version, hashes e caminhos.
func (l *Lock) validate() error {
	if l.Version != Version {
		return fmt.Errorf("version é obrigatória e precisa ser %d", Version)
	}
	if strings.TrimSpace(l.ToolkitVersion) == "" {
		return errors.New("toolkit_version é obrigatório")
	}
	keys := make([]string, 0, len(l.Files))
	for k := range l.Files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, p := range keys {
		if err := checkPath(p); err != nil {
			return fmt.Errorf("files: %w", err)
		}
		if h := l.Files[p]; !hashRe.MatchString(h) {
			return fmt.Errorf("files: %q: hash %q precisa ter 64 dígitos hex minúsculos", p, h)
		}
	}
	for i, p := range l.Released {
		if err := checkPath(p); err != nil {
			return fmt.Errorf("released[%d]: %w", i, err)
		}
		if _, ok := l.Files[p]; ok {
			return fmt.Errorf("released[%d]: %q aparece em files e released", i, p)
		}
	}
	return nil
}

// checkPath exige caminho relativo, com "/", na forma limpa e dentro da área
// gerenciada.
func checkPath(p string) error {
	switch {
	case p == "":
		return errors.New("caminho vazio")
	case strings.ContainsFunc(p, unicode.IsControl):
		return fmt.Errorf("caminho %q tem caractere de controle", p)
	case strings.Contains(p, `\`):
		return fmt.Errorf(`caminho %q usa "\"; o lock usa "/" como separador`, p)
	case strings.HasPrefix(p, "/"), driveRe.MatchString(p):
		return fmt.Errorf("caminho %q é absoluto; esperado relativo à raiz do projeto", p)
	case path.Clean(p) != p:
		return fmt.Errorf("caminho %q não está na forma limpa (%q)", p, path.Clean(p))
	case slices.Contains(strings.Split(p, "/"), ".."):
		return fmt.Errorf("caminho %q sai da raiz do projeto", p)
	case !ManagedArea(p):
		return fmt.Errorf("caminho %q fora da área gerenciada (%s)", p, strings.Join(managedPrefixes, ", "))
	}
	return nil
}

// normalized devolve uma cópia de l com Files e Released não nil e Released
// ordenado e sem repetição.
func (l *Lock) normalized() *Lock {
	files := make(map[string]string, len(l.Files))
	for k, v := range l.Files {
		files[k] = v
	}
	released := slices.Clone(l.Released)
	if released == nil {
		released = []string{}
	}
	slices.Sort(released)
	return &Lock{
		Version:        l.Version,
		ToolkitVersion: l.ToolkitVersion,
		Files:          files,
		Released:       slices.Compact(released),
	}
}

// checkDuplicateKeys percorre data, JSON já aceito pelo Decode, e reprova
// objeto com chave repetida.
func checkDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return walkValue(dec)
}

// walkValue consome um valor de dec, conferindo as chaves de cada objeto.
func walkValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("JSON inválido: %w", err)
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("JSON inválido: %w", err)
			}
			key, ok := kt.(string)
			if !ok {
				return fmt.Errorf("JSON inválido: chave %v", kt)
			}
			if seen[key] {
				return fmt.Errorf("chave %q repetida", key)
			}
			seen[key] = true
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for dec.More() {
			if err := walkValue(dec); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	if _, err := dec.Token(); err != nil { // fecha o objeto ou a lista
		return fmt.Errorf("JSON inválido: %w", err)
	}
	return nil
}
