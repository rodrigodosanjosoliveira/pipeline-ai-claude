## Contexto

<!-- Por que esta mudança existe. Fase/fatia do plano e TASK-NNN. -->

## O que mudou

<!-- Arquivos e decisões (TD-xx) envolvidas. Código portado: origem e alterações, e linha no NOTICE. -->

## Como testar

<!-- Comandos e saídas reais: make check, testes novos, e2e quando houver. -->

## Checklist

- [ ] Teste escrito antes do código, falhando sem a mudança
- [ ] `make check` verde (fmt, vet, stdlib-only, test -race)
- [ ] Sem dependência externa nova
- [ ] Falha fechada preservada (hook bloqueia, validador reprova em erro)
- [ ] Código portado com cabeçalho de origem e linha no `NOTICE`
- [ ] Nada de projeto real (nomes, caminhos, reports, IDs) em código, testes ou fixtures
