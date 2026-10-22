.PHONY: build test vet fmt-check stdlib-only vuln check fixtures-check clean

BIN := bin/pipeline-claude
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/rodrigodosanjosoliveira/pipeline-ai-claude/internal/buildinfo.version=$(VERSION)

# govulncheck fixado e executado via `go run`: sempre compilado com o toolchain
# do módulo e fora do go.mod (o núcleo é só stdlib). Fonte única para CI e local.
GOVULNCHECK_VERSION := v1.8.0

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/pipeline-claude

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt pendente:"; echo "$$out"; exit 1; fi

# O núcleo é só stdlib (TD-03): o grafo de módulos tem de ter só o módulo
# principal e não pode existir go.sum.
stdlib-only:
	@n="$$(go list -m all | wc -l)"; if [ "$$n" -ne 1 ]; then echo "dependência externa no go.mod:"; go list -m all; exit 1; fi
	@if [ -e go.sum ]; then echo "go.sum não deveria existir (núcleo só stdlib)"; exit 1; fi

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# Fixtures com a lista privada de termos bloqueados (fora do repositório):
#   make fixtures-check BLOCKLIST=~/caminho/privado/blocklist.txt
fixtures-check:
	@test -n "$(BLOCKLIST)" || (echo "informe BLOCKLIST=<arquivo>"; exit 2)
	PIPELINE_CLAUDE_FIXTURE_BLOCKLIST="$(BLOCKLIST)" go test -count=1 -run TestFixturesVersionadas -v ./internal/fixtures/

check: fmt-check vet stdlib-only test

clean:
	rm -rf bin
