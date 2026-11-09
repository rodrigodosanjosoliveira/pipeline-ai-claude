package doctor

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// projectAgent é um subagente do projeto (fora dos templates) com o corpo dado.
func projectAgent(name, body string) string {
	return "---\nname: " + name + "\ntools: Read, Grep, mcp__pipeline-claude__pipeline_sensors\n---\n\n" + body
}

func TestParseAgentSensors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "sem chamada", body: "Leia o diff.\n", want: nil},
		{name: "chamada em bloco de código", body: "```\npipeline_sensors  names: [\"rls_suite\"]\n```\n", want: []string{"rls_suite"}},
		{name: "chamada em prosa do template", body: "2. Rodar pelo sensor (`pipeline_sensors` com `names: [\"vuln\"]`).\n", want: []string{"vuln"}},
		{name: "vários nomes na linha, em ordem", body: "pipeline_sensors names: [\"rls_mutations_selftest\", \"rls_mutations\"]  # prova\n", want: []string{"rls_mutations", "rls_mutations_selftest"}},
		{name: "várias chamadas, sem repetir", body: "pipeline_sensors names: [\"gates\", \"rls\"]\npipeline_sensors names: [\"rls\"]\n", want: []string{"gates", "rls"}},
		// Prosa quebrada como nos templates do migration-reviewer e do agent-auditor.
		{name: "names na linha seguinte", body: "rodar o sensor (`pipeline_sensors` com\n   `names: [\"test\"]`) e ler\n", want: []string{"test"}},
		{name: "names duas linhas depois não conta", body: "`pipeline_sensors` com\noutra linha\n`names: [\"test\"]`\n", want: nil},
		{name: "names na linha seguinte só no início", body: "`pipeline_sensors` com\nveja names: [\"test\"]\n", want: nil},
		{name: "aspas simples", body: "pipeline_sensors names: ['vuln']\n", want: []string{"vuln"}},
		{name: "CRLF", body: "pipeline_sensors names: [\"vuln\"]\r\nfim\r\n", want: []string{"vuln"}},
		// names: sem pipeline_sensors na mesma linha não é pedido de sensor.
		{name: "names sem pipeline_sensors", body: "outra_ferramenta names: [\"vuln\"]\n", want: nil},
		{name: "pipeline_sensors sem names", body: "Use pipeline_sensors sem nomes para os gates.\n", want: nil},
		// Placeholder de documentação não é nome de sensor (formato da config).
		{name: "placeholder ignorado", body: "pipeline_sensors names: [\"<nome>\", \"Vuln\", \"\"]\n", want: nil},
		{name: "lista sem fechamento", body: "pipeline_sensors names: [\"vuln\"\n", want: nil},
		{name: "lista vazia", body: "pipeline_sensors names: []\n", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAgentSensors([]byte(projectAgent("x", tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("= %q, quero %q", got, tt.want)
			}
		})
	}
}

func TestCheckSensorsAgentesDoProjeto(t *testing.T) {
	const tenant = "pipeline_sensors  names: [\"rls_suite\"]\npipeline_sensors  names: [\"rls_mutations_selftest\", \"rls_mutations\"]\n"
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
		status Status
		msg    []string
		noMsg  []string
		action []string
	}{
		{
			name: "agente do projeto pede sensores ausentes",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "tenant-isolation", projectAgent("tenant-isolation", tenant))
			},
			status: StatusWarn,
			msg:    []string{"rls_mutations (tenant-isolation)", "rls_mutations_selftest (tenant-isolation)", "rls_suite (tenant-isolation)"},
			noMsg:  []string{"vuln ("},
			// Sem entrada pronta: o comando é do projeto.
			action: []string{"rls_mutations, rls_mutations_selftest, rls_suite", "comando do projeto"},
		},
		{
			name: "agente do projeto com sensor declarado",
			mutate: func(t *testing.T, root string) {
				writeAgent(t, root, "tenant-isolation", projectAgent("tenant-isolation", "pipeline_sensors names: [\"vuln\"]\n"))
			},
			status: StatusOK,
			msg:    []string{"mod_verify", "vuln"},
		},
		{
			name: "agente liberado que passa a pedir sensor novo",
			mutate: func(t *testing.T, root string) {
				appendRel(t, root, agentsDir+"/security-review.md", "\npipeline_sensors names: [\"gates\"]\n")
			},
			status: StatusWarn,
			msg:    []string{"gates (security-review)"},
		},
		{
			name: "sensor do template e do projeto ausentes juntos",
			mutate: func(t *testing.T, root string) {
				dropSensors(t, root, "vuln")
				writeAgent(t, root, "tenant-isolation", projectAgent("tenant-isolation", "pipeline_sensors names: [\"vuln\", \"rls\"]\n"))
			},
			status: StatusWarn,
			msg:    []string{"rls (tenant-isolation)", "vuln (security-review, supply-chain-review, tenant-isolation)"},
			action: []string{`"name": "vuln"`, "rls"},
		},
		{
			name: "symlink não é seguido",
			mutate: func(t *testing.T, root string) {
				target := filepath.Join(t.TempDir(), "fora.md")
				if err := os.WriteFile(target, []byte(projectAgent("fora", tenant)), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(agentsDir), "fora.md")); err != nil {
					t.Skipf("symlink indisponível: %v", err)
				}
			},
			status: StatusOK,
			noMsg:  []string{"rls_suite"},
		},
		{
			name: "arquivo que não é .md é ignorado",
			mutate: func(t *testing.T, root string) {
				writeRel(t, root, agentsDir+"/LEIAME.txt", tenant)
			},
			status: StatusOK,
			noMsg:  []string{"rls_suite"},
		},
		{
			name: "sem .claude/agents: só os dos templates",
			mutate: func(t *testing.T, root string) {
				if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(agentsDir))); err != nil {
					t.Fatal(err)
				}
			},
			status: StatusOK,
			msg:    []string{"mod_verify", "vuln"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := scaffolded(t)
			tt.mutate(t, root)
			c := find(t, collect(root, healthyEnv().env()), checkSensors)
			if c.Status != tt.status {
				t.Fatalf("status = %s, quero %s (%s → %s)", c.Status, tt.status, c.Message, c.Action)
			}
			rest := c.Message
			for _, s := range tt.msg {
				i := strings.Index(rest, s)
				if i < 0 {
					t.Errorf("mensagem %q sem %q (ou fora de ordem)", c.Message, s)
					continue
				}
				rest = rest[i+len(s):]
			}
			for _, s := range tt.noMsg {
				if strings.Contains(c.Message, s) {
					t.Errorf("mensagem %q com %q", c.Message, s)
				}
			}
			for _, s := range tt.action {
				if !strings.Contains(c.Action, s) {
					t.Errorf("ação %q sem %q", c.Action, s)
				}
			}
		})
	}
}

// FuzzAgentSensors: entrada arbitrária nunca entra em pânico; todo nome devolvido
// segue o formato de nome de sensor da config, ordenado e sem repetição.
func FuzzAgentSensors(f *testing.F) {
	for _, s := range []string{
		"",
		"pipeline_sensors names: [\"vuln\"]",
		"pipeline_sensors names: [\"a\", 'b', \"<c>\"]\r\n",
		"pipeline_sensors names: [",
		"pipeline_sensors names: ]\"x\"[",
		"names: [\"x\"] pipeline_sensors",
	} {
		f.Add(s)
	}
	valid := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	f.Fuzz(func(t *testing.T, s string) {
		got := parseAgentSensors([]byte(s))
		for _, n := range got {
			if !valid.MatchString(n) {
				t.Errorf("nome %q fora do formato", n)
			}
		}
		if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
			t.Errorf("%q não está ordenado e sem repetição", got)
		}
	})
}
