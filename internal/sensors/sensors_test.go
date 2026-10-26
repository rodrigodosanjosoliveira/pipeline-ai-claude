package sensors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/config"
)

// helperEnv ativa o modo helper do binário de teste: com ela, TestHelperProcess
// age como sensor em vez de teste.
const helperEnv = "PIPELINE_CLAUDE_SENSORS_HELPER"

// TestHelperProcess não é teste de verdade: é o sensor executado pelos outros
// testes (os.Args[0] -test.run=^TestHelperProcess$ -- <modo>), sem depender de sh.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: modo ausente")
		os.Exit(90)
	}
	switch args[0] {
	case "ok":
		fmt.Println("saida-do-ok")
		os.Exit(0)
	case "exit3":
		fmt.Println("stdout-do-exit3")
		fmt.Fprintln(os.Stderr, "stderr-do-exit3")
		os.Exit(3)
	case "big":
		// 20 000 bytes: começa com INICIO, termina com FIM.
		var b strings.Builder
		b.WriteString("INICIO")
		for b.Len() < 20000-3 {
			b.WriteByte('x')
		}
		b.WriteString("FIM")
		fmt.Print(b.String())
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(91)
		}
		fmt.Print(wd)
		os.Exit(0)
	case "env":
		fmt.Print(os.Getenv("PIPELINE_CLAUDE_SENSORS_PROBE"))
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "helper: modo desconhecido %q\n", args[0])
	os.Exit(92)
}

// helperCmd devolve o argv de um sensor que roda o helper no modo informado.
func helperCmd(t *testing.T, mode string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return []string{exe, "-test.run=^TestHelperProcess$", "--", mode}
}

// loadConfig grava uma configuração válida com os sensores dados num diretório
// temporário e a carrega.
func loadConfig(t *testing.T, sensors []config.Sensor) *config.Config {
	t.Helper()
	t.Setenv(helperEnv, "1")
	body := map[string]any{
		"version":         1,
		"project_type":    "api",
		"database":        "postgres",
		"git_provider":    "github",
		"protected_paths": []string{"internal/"},
		"sensors":         sensors,
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func names(r Result) []string {
	out := make([]string, 0, len(r.Sensors))
	for _, s := range r.Sensors {
		out = append(out, s.Name)
	}
	return out
}

func TestRun(t *testing.T) {
	type want struct {
		ok        bool
		names     []string
		exitCodes []int
		sensorOK  []bool
		outHas    []string // por sensor; "" não confere
		outNot    []string // por sensor; "" não confere
		errHas    []string // por sensor; "" exige erro vazio
		truncated []bool
		maxDur    time.Duration
	}
	cases := []struct {
		name    string
		sensors func(t *testing.T) []config.Sensor
		opts    Options
		want    want
	}{
		{
			name:    "sucesso",
			sensors: func(t *testing.T) []config.Sensor { return []config.Sensor{{Name: "lint", Cmd: helperCmd(t, "ok")}} },
			want: want{
				ok: true, names: []string{"lint"}, exitCodes: []int{0}, sensorOK: []bool{true},
				outHas: []string{"saida-do-ok"}, errHas: []string{""}, truncated: []bool{false},
			},
		},
		{
			name:    "exit 3 reprova e junta stdout e stderr",
			sensors: func(t *testing.T) []config.Sensor { return []config.Sensor{{Name: "test", Cmd: helperCmd(t, "exit3")}} },
			want: want{
				ok: false, names: []string{"test"}, exitCodes: []int{3}, sensorOK: []bool{false},
				outHas: []string{"stdout-do-exit3\nstderr-do-exit3"}, errHas: []string{""}, truncated: []bool{false},
			},
		},
		{
			name:    "saída maior que o limite guarda só o fim",
			sensors: func(t *testing.T) []config.Sensor { return []config.Sensor{{Name: "big", Cmd: helperCmd(t, "big")}} },
			want: want{
				ok: true, names: []string{"big"}, exitCodes: []int{0}, sensorOK: []bool{true},
				outHas: []string{"FIM"}, outNot: []string{"INICIO"}, errHas: []string{""}, truncated: []bool{true},
			},
		},
		{
			name: "timeout mata o processo",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{{Name: "lento", Cmd: helperCmd(t, "sleep")}}
			},
			opts: Options{Timeout: 200 * time.Millisecond},
			want: want{
				ok: false, names: []string{"lento"}, exitCodes: []int{-1}, sensorOK: []bool{false},
				errHas: []string{"tempo esgotado"}, truncated: []bool{false}, maxDur: 10 * time.Second,
			},
		},
		{
			name: "comando inexistente",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{{Name: "fantasma", Cmd: []string{"pipeline-claude-sensor-inexistente-zz9"}}}
			},
			want: want{
				ok: false, names: []string{"fantasma"}, exitCodes: []int{-1}, sensorOK: []bool{false},
				errHas: []string{"não encontrado"}, truncated: []bool{false},
			},
		},
		{
			name: "caminho relativo inexistente",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{{Name: "fantasma", Cmd: []string{"./bin/nao-existe"}}}
			},
			want: want{
				ok: false, names: []string{"fantasma"}, exitCodes: []int{-1}, sensorOK: []bool{false},
				errHas: []string{"não encontrado"}, truncated: []bool{false},
			},
		},
		{
			name: "todos rodam mesmo depois de falha",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{
					{Name: "primeiro", Cmd: helperCmd(t, "exit3")},
					{Name: "segundo", Cmd: helperCmd(t, "ok")},
				}
			},
			want: want{
				ok: false, names: []string{"primeiro", "segundo"}, exitCodes: []int{3, 0},
				sensorOK: []bool{false, true}, errHas: []string{"", ""}, truncated: []bool{false, false},
			},
		},
		{
			name: "filtro por nome mantém a ordem da config",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{
					{Name: "a", Cmd: helperCmd(t, "ok")},
					{Name: "b", Cmd: helperCmd(t, "exit3")},
					{Name: "c", Cmd: helperCmd(t, "ok")},
				}
			},
			opts: Options{Names: []string{"c", "a"}},
			want: want{
				ok: true, names: []string{"a", "c"}, exitCodes: []int{0, 0},
				sensorOK: []bool{true, true}, errHas: []string{"", ""}, truncated: []bool{false, false},
			},
		},
		{
			name: "nome repetido no filtro roda uma vez",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{{Name: "a", Cmd: helperCmd(t, "ok")}, {Name: "b", Cmd: helperCmd(t, "ok")}}
			},
			opts: Options{Names: []string{"b", "b"}},
			want: want{
				ok: true, names: []string{"b"}, exitCodes: []int{0}, sensorOK: []bool{true},
				errHas: []string{""}, truncated: []bool{false},
			},
		},
		{
			name: "sem filtro roda todos na ordem da config",
			sensors: func(t *testing.T) []config.Sensor {
				return []config.Sensor{
					{Name: "z", Cmd: helperCmd(t, "ok")},
					{Name: "m", Cmd: helperCmd(t, "ok")},
					{Name: "a", Cmd: helperCmd(t, "ok")},
				}
			},
			want: want{
				ok: true, names: []string{"z", "m", "a"}, exitCodes: []int{0, 0, 0},
				sensorOK: []bool{true, true, true}, errHas: []string{"", "", ""}, truncated: []bool{false, false, false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, tc.sensors(t))
			start := time.Now()
			r := Run(context.Background(), cfg, tc.opts)
			elapsed := time.Since(start)

			if r.Err != nil {
				t.Fatalf("Err = %v, esperado nil", r.Err)
			}
			if r.OK != tc.want.ok {
				t.Errorf("OK = %v, esperado %v (%+v)", r.OK, tc.want.ok, r)
			}
			if got := names(r); strings.Join(got, ",") != strings.Join(tc.want.names, ",") {
				t.Fatalf("sensores = %v, esperado %v", got, tc.want.names)
			}
			for i, s := range r.Sensors {
				if s.ExitCode != tc.want.exitCodes[i] {
					t.Errorf("%s: exit_code = %d, esperado %d (erro %q, saída %q)", s.Name, s.ExitCode, tc.want.exitCodes[i], s.Error, s.Output)
				}
				if s.OK != tc.want.sensorOK[i] {
					t.Errorf("%s: ok = %v, esperado %v", s.Name, s.OK, tc.want.sensorOK[i])
				}
				if s.Truncated != tc.want.truncated[i] {
					t.Errorf("%s: truncated = %v, esperado %v", s.Name, s.Truncated, tc.want.truncated[i])
				}
				if want := tc.want.errHas[i]; want == "" && s.Error != "" {
					t.Errorf("%s: error = %q, esperado vazio", s.Name, s.Error)
				} else if !strings.Contains(s.Error, want) {
					t.Errorf("%s: error = %q, esperado conter %q", s.Name, s.Error, want)
				}
				if i < len(tc.want.outHas) && !strings.Contains(s.Output, tc.want.outHas[i]) {
					t.Errorf("%s: output = %q, esperado conter %q", s.Name, s.Output, tc.want.outHas[i])
				}
				if i < len(tc.want.outNot) && tc.want.outNot[i] != "" && strings.Contains(s.Output, tc.want.outNot[i]) {
					t.Errorf("%s: output contém %q, não devia", s.Name, tc.want.outNot[i])
				}
				if len(s.Output) > OutputLimit {
					t.Errorf("%s: output com %d bytes, limite %d", s.Name, len(s.Output), OutputLimit)
				}
				if s.DurationMS < 0 {
					t.Errorf("%s: duration_ms = %d negativo", s.Name, s.DurationMS)
				}
			}
			if tc.want.maxDur > 0 && elapsed > tc.want.maxDur {
				t.Errorf("Run levou %v, esperado até %v", elapsed, tc.want.maxDur)
			}
		})
	}
}

func TestRunBigOutputExactTail(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{{Name: "big", Cmd: helperCmd(t, "big")}})
	r := Run(context.Background(), cfg, Options{})
	if len(r.Sensors) != 1 {
		t.Fatalf("sensores = %d", len(r.Sensors))
	}
	s := r.Sensors[0]
	if len(s.Output) != OutputLimit {
		t.Fatalf("output com %d bytes, esperado exatamente %d", len(s.Output), OutputLimit)
	}
	if want := strings.Repeat("x", OutputLimit-3) + "FIM"; s.Output != want {
		t.Errorf("output não é o fim da saída")
	}
}

func TestRunDirAndEnv(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{
		{Name: "pwd", Cmd: helperCmd(t, "pwd")},
		{Name: "env", Cmd: helperCmd(t, "env")},
	})
	t.Setenv("PIPELINE_CLAUDE_SENSORS_PROBE", "herdado")
	r := Run(context.Background(), cfg, Options{})
	if !r.OK || len(r.Sensors) != 2 {
		t.Fatalf("resultado inesperado: %+v", r)
	}
	wantDir, err := filepath.EvalSymlinks(cfg.Root())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(r.Sensors[0].Output)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", r.Sensors[0].Output, err)
	}
	if gotDir != wantDir {
		t.Errorf("diretório do sensor = %q, esperado raiz %q", gotDir, wantDir)
	}
	if r.Sensors[1].Output != "herdado" {
		t.Errorf("ambiente não herdado: %q", r.Sensors[1].Output)
	}
}

func TestRunUnknownName(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{{Name: "lint", Cmd: helperCmd(t, "ok")}})
	r := Run(context.Background(), cfg, Options{Names: []string{"lint", "nao_existe"}})
	if !errors.Is(r.Err, ErrUnknownSensor) {
		t.Fatalf("Err = %v, esperado ErrUnknownSensor", r.Err)
	}
	if !strings.Contains(r.Error, "nao_existe") {
		t.Errorf("Error = %q, esperado citar o nome", r.Error)
	}
	if r.OK || len(r.Sensors) != 0 {
		t.Errorf("uso inválido não pode rodar sensor: %+v", r)
	}
	for _, asJSON := range []bool{false, true} {
		var buf bytes.Buffer
		if code := Print(&buf, r, asJSON); code != 2 {
			t.Errorf("Print(json=%v) = %d, esperado 2", asJSON, code)
		}
		if !strings.Contains(buf.String(), "nao_existe") {
			t.Errorf("Print(json=%v) não cita o nome: %q", asJSON, buf.String())
		}
	}
}

func TestRunNoSensors(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(t *testing.T) *config.Config
	}{
		{"config sem sensores", func(t *testing.T) *config.Config { return loadConfig(t, nil) }},
		{"config nil", func(t *testing.T) *config.Config { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Run(context.Background(), tc.cfg(t), Options{})
			if r.OK {
				t.Fatalf("OK = true, esperado reprovado (TD-19, TD-08)")
			}
			if errors.Is(r.Err, ErrUnknownSensor) {
				t.Errorf("não é uso inválido: %v", r.Err)
			}
			var buf bytes.Buffer
			if code := Print(&buf, r, false); code != 1 {
				t.Errorf("Print = %d, esperado 1", code)
			}
			if tc.name == "config sem sensores" && !strings.Contains(buf.String(), "nenhum sensor configurado") {
				t.Errorf("saída sem a mensagem: %q", buf.String())
			}
		})
	}
}

func TestRunCanceledContext(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{
		{Name: "a", Cmd: helperCmd(t, "sleep")},
		{Name: "b", Cmd: helperCmd(t, "ok")},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Run(ctx, cfg, Options{})
	if r.OK || len(r.Sensors) != 2 {
		t.Fatalf("resultado inesperado: %+v", r)
	}
	for _, s := range r.Sensors {
		if s.OK || !strings.Contains(s.Error, "cancelad") {
			t.Errorf("%s: ok=%v error=%q, esperado falha por cancelamento", s.Name, s.OK, s.Error)
		}
	}
}

func TestPrintJSON(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{
		{Name: "lint", Cmd: helperCmd(t, "ok")},
		{Name: "test", Cmd: helperCmd(t, "exit3")},
		{Name: "fantasma", Cmd: []string{"pipeline-claude-sensor-inexistente-zz9"}},
	})
	r := Run(context.Background(), cfg, Options{})
	var buf bytes.Buffer
	if code := Print(&buf, r, true); code != 1 {
		t.Fatalf("Print = %d, esperado 1", code)
	}
	var got struct {
		OK      *bool            `json:"ok"`
		Sensors []map[string]any `json:"sensors"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("JSON inválido: %v\n%s", err, buf.String())
	}
	if got.OK == nil || *got.OK {
		t.Fatalf("ok = %v, esperado false", got.OK)
	}
	if len(got.Sensors) != 3 {
		t.Fatalf("sensors = %d, esperado 3", len(got.Sensors))
	}
	for _, s := range got.Sensors {
		for _, k := range []string{"name", "ok", "exit_code", "duration_ms", "output", "truncated", "error"} {
			if _, ok := s[k]; !ok {
				t.Errorf("sensor %v sem a chave %q", s["name"], k)
			}
		}
	}
	if got.Sensors[1]["exit_code"] != float64(3) {
		t.Errorf("exit_code = %v, esperado 3", got.Sensors[1]["exit_code"])
	}
	if got.Sensors[2]["exit_code"] != float64(-1) {
		t.Errorf("exit_code = %v, esperado -1", got.Sensors[2]["exit_code"])
	}

	okOnly := Run(context.Background(), cfg, Options{Names: []string{"lint"}})
	buf.Reset()
	if code := Print(&buf, okOnly, true); code != 0 {
		t.Errorf("Print = %d, esperado 0", code)
	}
}

func TestPrintHuman(t *testing.T) {
	cfg := loadConfig(t, []config.Sensor{
		{Name: "lint", Cmd: helperCmd(t, "ok")},
		{Name: "test", Cmd: helperCmd(t, "exit3")},
		{Name: "fantasma", Cmd: []string{"pipeline-claude-sensor-inexistente-zz9"}},
	})
	r := Run(context.Background(), cfg, Options{})
	var buf bytes.Buffer
	if code := Print(&buf, r, false); code != 1 {
		t.Fatalf("Print = %d, esperado 1", code)
	}
	out := buf.String()
	for _, want := range []string{"[ok]", "lint", "[falha]", "test", "fantasma", "stderr-do-exit3", "não encontrado", "reprovado"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "saida-do-ok") {
		t.Errorf("saída de sensor ok não deve aparecer:\n%s", out)
	}
	if i, j := strings.Index(out, "lint"), strings.Index(out, "test"); i < 0 || j < i {
		t.Errorf("ordem errada:\n%s", out)
	}

	okOnly := Run(context.Background(), cfg, Options{Names: []string{"lint"}})
	buf.Reset()
	if code := Print(&buf, okOnly, false); code != 0 {
		t.Errorf("Print = %d, esperado 0", code)
	}
	if strings.Contains(buf.String(), "[falha]") {
		t.Errorf("saída inesperada:\n%s", buf.String())
	}
}

func TestTailBuffer(t *testing.T) {
	cases := []struct {
		name      string
		limit     int
		writes    []string
		want      string
		truncated bool
	}{
		{"vazio", 4, nil, "", false},
		{"cabe", 4, []string{"ab", "cd"}, "abcd", false},
		{"estoura em várias escritas", 4, []string{"abc", "def", "g"}, "defg", true},
		{"uma escrita maior que o limite", 4, []string{"abcdefghij"}, "ghij", true},
		{"escritas de um byte", 3, []string{"a", "b", "c", "d", "e"}, "cde", true},
		{"corte no meio de runa descarta o pedaço", 4, []string{"xé", "abc"}, "abc", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTailBuffer(tc.limit)
			total := 0
			for _, w := range tc.writes {
				n, err := b.Write([]byte(w))
				if err != nil || n != len(w) {
					t.Fatalf("Write(%q) = %d, %v", w, n, err)
				}
				total += n
			}
			got, truncated := b.result()
			if got != tc.want || truncated != tc.truncated {
				t.Errorf("result = %q, %v; esperado %q, %v", got, truncated, tc.want, tc.truncated)
			}
			if len(got) > tc.limit {
				t.Errorf("%d bytes acima do limite %d (total %s)", len(got), tc.limit, strconv.Itoa(total))
			}
		})
	}
}
