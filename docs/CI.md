# Validação de delivery no CI

O hook protege a sessão do Claude Code; o CI é a última linha: confere o report
da delivery contra o diff real da MR/PR, mesmo quando alguém trabalhou fora da
sessão (ou com um agente que não passa pelo hook, como o GitLab Duo).

Projeto criado com `pipeline-claude init` já nasce com este CI, conforme o
`--provider`: `.github/workflows/delivery.yml` (GitHub) ou `.gitlab-ci.yml`
(GitLab), com o job de validação abaixo e um job de gates (`make tools` e
`bin/pipeline-claude sensors`). O arquivo é do projeto: o `sync` não o toca e
o `adopt` não o cria (projeto existente já tem o seu CI; use as receitas).

O job abaixo:

1. instala o `pipeline-claude` na versão fixada no `Makefile` (`make tools`);
2. tira o ID da delivery do nome da branch (`task/<ID>`, convenção do fluxo);
3. roda `pipeline-claude validate --base <branch alvo> <ID>`: esquema do report,
   registro no índice, formato do ID e as flags que o diff exige (`auto_flags`,
   `security_surface_paths`).

Exit 1 (reprovado) ou 2 (config inválida, ID ausente, git sem a base) falham o
job. Branch fora do padrão `task/` não roda o job: ajuste a regra se o projeto
usar outro.

## GitLab CI

```yaml
delivery-validate:
  stage: test
  image: golang:1.24          # a versão do go.mod do projeto
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event" && $CI_MERGE_REQUEST_SOURCE_BRANCH_NAME =~ /^task\//
  variables:
    GIT_DEPTH: "0"            # o diff é contra o merge-base com a branch alvo
  script:
    - make tools
    - git fetch --no-tags origin "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"
    - ID="${CI_MERGE_REQUEST_SOURCE_BRANCH_NAME#task/}"
    - bin/pipeline-claude validate --base "origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME" "$ID"
```

## GitHub Actions

```yaml
name: delivery
on: pull_request

permissions:
  contents: read

jobs:
  validate:
    if: startsWith(github.head_ref, 'task/')
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4          # fixe por SHA, como o resto do workflow
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make tools
      - name: validate
        env:
          HEAD_REF: ${{ github.head_ref }}
          BASE_REF: ${{ github.base_ref }}
        run: bin/pipeline-claude validate --base "origin/$BASE_REF" "${HEAD_REF#task/}"
```

`head_ref` e `base_ref` entram por variável de ambiente, nunca interpolados
direto no `run`: o nome da branch vem de quem abre a PR.

## Notas

- **Versão:** o job usa a mesma versão que a equipe usa localmente, porque as
  duas saem do `PIPELINE_CLAUDE_VERSION` do `Makefile`.
- **Delivery sem report:** branch `task/<ID>` sem `<deliveries_dir>/<ID>/report.md`
  reprova. Mudança que não é delivery usa outro prefixo de branch.
- **Sensores:** o job só valida o report. Os gates do projeto (`make test`,
  `make vet`…) continuam nos jobs que o CI já tem; `pipeline-claude sensors` pode
  substituí-los se o projeto quiser uma fonte só.
