// Package sensors executa os sensores declarados em .pipeline-claude.json:
// verificações determinísticas (lint, testes, build) que reprovam a delivery
// quando falham.
//
// Cada sensor roda o argv de Cmd, sem shell, na raiz do projeto, na ordem da
// configuração e com timeout próprio (TD-19). Todos rodam, mesmo depois de uma
// falha, para o relatório ficar completo. Comando ausente, timeout ou exit
// diferente de 0 reprovam; nenhum sensor configurado também reprova (TD-08).
//
// Uso típico pelo chamador (CLI ou MCP):
//
//	r := sensors.Run(ctx, cfg, sensors.Options{Names: names, Timeout: timeout})
//	os.Exit(sensors.Print(os.Stdout, r, asJSON))
//
// Print devolve 0 (todos ok), 1 (reprovado) ou 2 (uso inválido: nome de
// sensor desconhecido, detectável também por errors.Is(r.Err, ErrUnknownSensor)).
package sensors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// DefaultTimeout é o timeout por sensor quando Options.Timeout não é positivo.
const DefaultTimeout = 10 * time.Minute

// OutputLimit é o máximo de bytes de saída guardados por sensor: os últimos
// OutputLimit bytes de stdout e stderr juntos.
const OutputLimit = 8 << 10

// waitDelay é quanto Wait espera pelo fechamento dos pipes depois de matar o
// processo, para não pendurar quando um filho herdou stdout/stderr.
const waitDelay = 2 * time.Second

// ErrUnknownSensor indica que Options.Names pediu um sensor que não existe na
// configuração. É uso inválido: o chamador devolve exit 2.
var ErrUnknownSensor = errors.New("sensor desconhecido")

// ErrNoSensors indica que não há sensor a executar. Reprova (exit 1): nada foi
// verificado (TD-19, TD-08).
var ErrNoSensors = errors.New("nenhum sensor configurado")

// Options controla a execução.
type Options struct {
	// Names restringe a execução a esses sensores, na ordem da configuração.
	// Vazio executa todos. Nome repetido roda uma vez.
	Names []string
	// Timeout é o prazo de cada sensor. Zero ou negativo vale DefaultTimeout.
	Timeout time.Duration
}

// SensorResult é o resultado de um sensor.
type SensorResult struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	// ExitCode é o código de saída do processo; −1 se não rodou ou foi morto
	// (timeout, cancelamento).
	ExitCode   int   `json:"exit_code"`
	DurationMS int64 `json:"duration_ms"`
	// Output são os últimos OutputLimit bytes de stdout e stderr juntos.
	Output string `json:"output"`
	// Truncated indica que o começo da saída foi descartado.
	Truncated bool `json:"truncated"`
	// Error descreve por que o sensor não terminou normalmente (comando não
	// encontrado, timeout, cancelamento). Vazio quando o processo saiu.
	Error string `json:"error"`
}

// Result é o resultado da execução.
type Result struct {
	// OK é true só se ao menos um sensor rodou e todos passaram.
	OK      bool           `json:"ok"`
	Sensors []SensorResult `json:"sensors"`
	// Error é a mensagem de Err, para a saída JSON.
	Error string `json:"error,omitempty"`
	// Err é o erro que impediu a execução: ErrUnknownSensor (uso inválido),
	// ErrNoSensors ou configuração ausente. Nil quando os sensores rodaram.
	Err error `json:"-"`
}

// ExitCode devolve o código de saída do resultado: 0 ok, 1 reprovado, 2 uso
// inválido.
func (r Result) ExitCode() int {
	switch {
	case errors.Is(r.Err, ErrUnknownSensor):
		return 2
	case r.OK:
		return 0
	default:
		return 1
	}
}

// Run executa os sensores selecionados de cfg, um de cada vez, e devolve o
// resultado. Nunca devolve OK sem ter rodado ao menos um sensor. Nome
// desconhecido em opts.Names não roda nada e preenche Err com
// ErrUnknownSensor.
func Run(ctx context.Context, cfg *config.Config, opts Options) Result {
	if cfg == nil {
		return failed(errors.New("configuração ausente"))
	}
	selected, err := selectSensors(cfg.Sensors, opts.Names)
	if err != nil {
		return failed(err)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	r := Result{OK: true, Sensors: make([]SensorResult, 0, len(selected))}
	for _, s := range selected {
		sr := runOne(ctx, cfg.Root(), s, timeout)
		r.OK = r.OK && sr.OK
		r.Sensors = append(r.Sensors, sr)
	}
	return r
}

func failed(err error) Result {
	return Result{OK: false, Sensors: []SensorResult{}, Error: err.Error(), Err: err}
}

// selectSensors filtra all pelos nomes pedidos, mantendo a ordem de all.
func selectSensors(all []config.Sensor, names []string) ([]config.Sensor, error) {
	if len(names) == 0 {
		if len(all) == 0 {
			return nil, ErrNoSensors
		}
		return all, nil
	}
	known := make(map[string]bool, len(all))
	for _, s := range all {
		known[s.Name] = true
	}
	var unknown []string
	for _, n := range names {
		if !known[n] && !slices.Contains(unknown, n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSensor, strings.Join(unknown, ", "))
	}
	var out []config.Sensor
	for _, s := range all {
		if slices.Contains(names, s.Name) {
			out = append(out, s)
		}
	}
	return out, nil
}

// runOne executa um sensor com prazo próprio, na raiz do projeto.
func runOne(parent context.Context, root string, s config.Sensor, timeout time.Duration) SensorResult {
	res := SensorResult{Name: s.Name, ExitCode: -1}
	if len(s.Cmd) == 0 || s.Cmd[0] == "" {
		res.Error = "cmd vazio"
		return res
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	out := newTailBuffer(OutputLimit)
	cmd := exec.CommandContext(ctx, s.Cmd[0], s.Cmd[1:]...)
	cmd.Dir = root
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = waitDelay

	start := time.Now()
	err := cmd.Run()
	res.DurationMS = time.Since(start).Milliseconds()
	res.Output, res.Truncated = out.result()
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case err == nil:
		res.OK = true
	case parent.Err() != nil:
		res.ExitCode = -1
		res.Error = fmt.Sprintf("execução cancelada: %v", parent.Err())
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode = -1
		res.Error = fmt.Sprintf("tempo esgotado após %s; processo encerrado", timeout)
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		res.Error = fmt.Sprintf("comando não encontrado: %s", s.Cmd[0])
	case cmd.ProcessState == nil:
		res.Error = fmt.Sprintf("falha ao iniciar %s: %v", s.Cmd[0], err)
	default:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			// O processo saiu, mas algo além do exit falhou (ex.: WaitDelay).
			res.Error = fmt.Sprintf("falha ao aguardar %s: %v", s.Cmd[0], err)
		}
	}
	return res
}

// Print escreve r em w, legível ou em JSON ({"ok", "sensors": [...]}), e
// devolve o código de saída (ver Result.ExitCode). Falha ao escrever devolve 1,
// ou 2 se o resultado já era uso inválido.
func Print(w io.Writer, r Result, asJSON bool) int {
	code := r.ExitCode()
	fail := func() int {
		if code == 2 {
			return 2
		}
		return 1
	}
	if asJSON {
		if r.Sensors == nil {
			r.Sensors = []SensorResult{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return fail()
		}
		return code
	}

	var b strings.Builder
	if r.Err != nil {
		fmt.Fprintf(&b, "pipeline-claude sensors: %s\n", r.Err)
	}
	var failedN int
	for _, s := range r.Sensors {
		status := "[ok]"
		if !s.OK {
			status = "[falha]"
			failedN++
		}
		fmt.Fprintf(&b, "  %-8s %-20s %s\n", status, s.Name, formatDuration(s.DurationMS))
		if s.OK {
			continue
		}
		switch {
		case s.Error != "":
			fmt.Fprintf(&b, "  %-8s %-20s → %s\n", "", "", s.Error)
		default:
			fmt.Fprintf(&b, "  %-8s %-20s → exit %d\n", "", "", s.ExitCode)
		}
		if s.Output != "" {
			if s.Truncated {
				fmt.Fprintf(&b, "      (saída truncada: últimos %d bytes)\n", OutputLimit)
			}
			for _, line := range strings.Split(strings.TrimRight(s.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "      | %s\n", line)
			}
		}
	}
	verdict := "aprovado"
	if code != 0 {
		verdict = "reprovado"
	}
	fmt.Fprintf(&b, "\nResultado: %d sensor(es), %d falha(s): %s.\n", len(r.Sensors), failedN, verdict)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fail()
	}
	return code
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// tailBuffer guarda só os últimos limit bytes escritos, em anel, sem crescer
// além disso.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	next  int   // posição da próxima escrita quando o anel está cheio
	total int64 // bytes recebidos
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{buf: make([]byte, 0, limit), limit: limit}
}

// Write nunca falha e sempre consome p inteiro.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	t.total += int64(n)
	if t.limit <= 0 {
		return n, nil
	}
	if len(p) > t.limit {
		p = p[len(p)-t.limit:]
	}
	if room := t.limit - len(t.buf); room > 0 {
		k := min(room, len(p))
		t.buf = append(t.buf, p[:k]...)
		p = p[k:]
	}
	for len(p) > 0 {
		k := copy(t.buf[t.next:], p)
		p = p[k:]
		t.next = (t.next + k) % t.limit
	}
	return n, nil
}

// result devolve o conteúdo em ordem e se houve corte. Depois de um corte,
// descarta bytes de continuação UTF-8 no começo, para não abrir com runa
// partida.
func (t *tailBuffer) result() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]byte, 0, len(t.buf))
	out = append(out, t.buf[t.next:]...)
	out = append(out, t.buf[:t.next]...)
	truncated := t.total > int64(len(out))
	if truncated {
		for i := 0; i < utf8.UTFMax-1 && len(out) > 0 && !utf8.RuneStart(out[0]); i++ {
			out = out[1:]
		}
	}
	return string(out), truncated
}
