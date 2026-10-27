# Contrato de handoff

Todo subagente termina o turno com este bloco. Sem ele, o próximo subagente não
tem por onde começar e o `agent-auditor` marca `HANDOFF_RASO`.

## Formato

```
DELIVERY_ID: {id da delivery}
TASK_CODE: {código da tarefa}
AGENT: {nome-do-subagente}
STATUS: ok | bloqueado

CONFIRMADO:
  - {o que foi verificado no código, com caminho ou comando}

ASSUNCOES:
  - {o que foi suposto e não confirmado; vazio só se nada foi suposto}

ARQUIVOS:
  - {caminho} — {o que mudou}

RISCOS:
  - {risco residual específico, não genérico}

SINAIS:
  SECURITY: sim|nao — {motivo}
  SUPPLY_CHAIN: sim|nao — {motivo}
  MIGRATION: sim|nao — {motivo}
  PERFORMANCE: sim|nao — {motivo}

PROXIMO: {subagente}
MOTIVO: {por que este subagente e não outro}
```

## Regras

1. **`CONFIRMADO` exige evidência:** caminho de arquivo, comando executado ou
   saída real. "Verifiquei e está ok" não é evidência.
2. **`ASSUNCOES` vazio precisa ser verdade.** Se havia ambiguidade e o campo está
   vazio, é `DRIFT`.
3. **Os `SINAIS` determinam o roteamento.** `SECURITY: sim` obriga a passagem por
   `security-review` antes do QA; `SUPPLY_CHAIN: sim` obriga
   `supply-chain-review`. `MIGRATION` e `PERFORMANCE` acionam os módulos
   correspondentes, se habilitados em `enabled_modules`.
4. **`STATUS: bloqueado` interrompe o fluxo.** `PROXIMO` passa a ser quem pode
   desbloquear e `MOTIVO` diz o que falta.
5. **Um handoff não repete o anterior.** Carrega o que o receptor precisa, não o
   histórico inteiro.
