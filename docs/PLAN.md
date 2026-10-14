# Quarry — Spec & Plano (app desktop)

_Quarry chamava-se YATUI ("Yet Another Terminal User Interface") enquanto foi uma TUI._

> Substitui o doc "YATUI — Spec & Plano" (versão TUI) do claude.ai. A versão TUI foi abandonada a 2026‑10‑13; o código reaproveitável ficou em `internal/`. O ponto de situação e os próximos passos imediatos estão em [`HANDOFF.md`](HANDOFF.md).

## 1. Visão e princípios

Quarry é um IDE de bases de dados **desktop**, rápido e bonito, para substituir DBeaver/DataGrip no dia a dia com **PostgreSQL, Oracle, MongoDB e Redis**. O problema de partida: o DBeaver é lento, feio e não deixa claro em que conexão um script vai correr.

1. **Contexto sempre visível.** Cada script está ligado a exatamente uma conexão e a um scope (database/schema). A cor do ambiente (local/dev/qa/prod) aparece na tab, na barra de contexto e na status bar. Não há scripts soltos.
2. **Mouse-first, keyboard-complete.** Tudo se faz com o rato (cliques, arrasto, menus de contexto, painéis redimensionáveis). Todas as ações têm atalho e estão na command palette.
3. **A UI nunca bloqueia.** Todo o I/O é assíncrono e cancelável. Os resultados chegam em streaming.
4. **Seguro por defeito em PROD.** Há confirmações, modo read-only e transações visíveis.
5. **Sem bloat.** Um binário, arranque rápido, sem Electron.
6. **Linux-first, Windows suportado.** macOS fica em best-effort.

## 2. Decisões de stack

| Camada | Escolha | Porquê |
|---|---|---|
| Shell desktop | **Wails v2** (migrar para v3 quando estabilizar) | O dono trabalha em Go há 4 anos. Webview nativa, um binário, sem Rust. A v3 (multi-janela, tray) ainda estava em beta em 2026‑10. |
| Backend | **Go 1.27** | Reaproveita os pacotes já feitos e testados (drivers, store, workspace, export…). Oracle em Go puro (`go-ora`), sem Instant Client. |
| API UI↔backend | **Connect** (`connectrpc.com/connect`), protobuf gerado com **buf** | O dono gosta de gRPC. O contrato `.proto` gera tipos Go e TS. Server streaming para resultados e eventos. Fica pronto para um modo servidor/remoto no futuro. |
| Frontend | **Svelte 5** (runes) + **TypeScript** + **Vite** (SPA, sem SvelteKit) | Leve e reativo, com pouco boilerplate. |
| Editor | **CodeMirror 6** (`@codemirror/lang-sql`, …) | Mais leve que o Monaco e funciona bem no WebKitGTK. |
| Cliente RPC | `@connectrpc/connect-web` + `@bufbuild/protobuf` (protobuf‑es v2) | Suporta unary e server streaming sobre `fetch`. |
| Grelha | Grelha virtual própria (DOM virtualizado; canvas se o protótipo o exigir) | Tem de aguentar 1M de linhas no WebKitGTK. Validar na fase A0. |
| Tema | Catppuccin Mocha como CSS custom properties (Latte/Frappé/Macchiato depois) | Escolha do dono. |

**Rejeitados:**
- **Tauri:** obriga a Rust; o sidecar Go só acrescenta complexidade.
- **GraphQL:** não há rede e as colunas são dinâmicas.
- **WebSockets "à mão":** substituídos pelo streaming do Connect.

### Limitações conhecidas e assumidas
- **Na webview só há unary e server streaming.** Client streaming e bidi não existem sobre `fetch`. Cancelar = abortar o stream (`AbortController` → `ctx` cancelado no Go) ou um `Cancel(run_id)` unary. Input interativo (ex.: o terminal) vai por chamadas unary.
- **CGO no Linux e macOS.** O Wails liga ao WebKitGTK e ao WebKit, por isso deixa de haver "sem CGO" nesses sistemas. No Linux é preciso `libgtk-3-dev` e `libwebkit2gtk-4.1-dev`; nas distros só com webkit2gtk‑4.1 usa‑se a build tag `webkit2_41` (confirmado: `go build -tags desktop,production,webkit2_41` ou `wails build -tags webkit2_41`; o `wails doctor` ainda procura a 4.0, ignorar). No Windows não precisa de CGO (WebView2).
- **WebKitGTK é mais lento que o Chromium.** A grelha e o editor têm de ser validados cedo no Linux.

## 3. Arquitetura

```
┌──────────────────────── Svelte 5 + TS (webview) ─────────────────────────┐
│ Explorer · Tabs · ContextBar · Editor (CM6) · Grid virtual · Painéis     │
│ stores (*.svelte.ts) ── api/ (clientes Connect gerados pelo buf)         │
└───────────────┬──────────────────────────────────────────────────────────┘
                │ Connect: unary + server streaming (protobuf)
┌───────────────▼──────────────── Go (um processo) ────────────────────────┐
│ main.go: Wails v2 (janela, menus nativos, diálogos de ficheiros, eventos)│
│ server/: http.Handler com os handlers Connect (+ token de sessão)        │
│ service/: ConnectionService, ExplorerService, QueryService, …            │
│   └─ estado: conexões vivas, sessões por tab, runs, cache de catálogo    │
│ internal/: driver/* · store · workspace · export · sqlsafe · params ·    │
│            shell · complete   (já existentes, ver §5)                   │
└──────────────────────────────────────────────────────────────────────────┘
```

**Transporte Connect: decidido na fase A0 → `127.0.0.1:0` + token**
- O pacote `server/` serve o Connect num `http.Server` em `127.0.0.1:0` (porta aleatória), com um token aleatório por arranque (`crypto/rand.Text`). O frontend obtém `{url, token}` pelo binding Wails `App.ServerInfo()` e envia `Authorization: Bearer …` em todos os pedidos.
- CORS só aceita as origens da webview (`wails://wails` no Linux/macOS, `http://wails.localhost` no Windows) e páginas de loopback (`wails dev`). Quem protege a API é o token, não o CORS.
- **Porque não o AssetServer:** no Wails v2.16 o `responseWriter` do Windows (`pkg/assetserver/webview/responsewriter_windows.go`) acumula o corpo num `bytes.Buffer` e só o entrega ao WebView2 no `Finish()`. O streaming partia-se no Windows (no Linux usa um pipe e funcionaria). O teste `server.TestStreamArrivesProgressively` garante que os lotes chegam aos poucos.
- **HTTP/1.1:** a webview fala HTTP/1.1 com o servidor (sem h2c no `fetch`), com ~6 sockets por origem. Cada stream aberto ocupa um socket, por isso: um só `WatchStatus` para todas as conexões, e os runs longos não devem ficar abertos sem necessidade.

**Modelo de sessões** (igual ao da TUI, já implementado nos drivers):
- Cada tab tem uma `driver.Session` dedicada.
- O explorer e o catálogo usam a ligação de metadados da `driver.Conn`.
- Os runs são identificados por `run_id`, e as páginas seguintes de um resultado pedem‑se com `Fetch(run_id, result_index, n)` ou chegam pelo stream.

## 4. Estrutura do repositório (alvo)

```
/                     go.mod (module github.com/gxd3v/quarry) · main.go (Wails) · wails.json
proto/quarry/v1/       *.proto  (buf.yaml, buf.gen.yaml na raiz)
gen/                  código gerado Go (connect + protobuf)
server/               montagem Connect (auth, CORS, interceptors de log/recover)
service/              implementação dos serviços (usa internal/)
internal/             pacotes reaproveitados (§5)
frontend/             Vite + Svelte 5 + TS (src/lib/api gerado pelo buf)
docs/                 PLAN.md (este), HANDOFF.md
```

## 5. Backend já feito (reaproveitado)

Todos estes pacotes têm testes e passam `go vet` e `-race`. O Postgres passa também os testes de integração contra o container `quarry-pg` (§13).

| Pacote | O que faz | API principal | Notas para a app |
|---|---|---|---|
| `internal/driver` | Contrato de backends | `Driver`, `Conn` (`Children`, `NewSession(scope)`), `Session` (`Split`, `Execute`), `Result` (`Columns`, `Next(n)`, `Affected`), `Scope`, `QueryError` (com posição), capacidades opcionais `Transactional`, `MessageSource`, `Catalog`, `DataViewer`, `Describer`, `Editable`, `Explainer`; `Register`/`Lookup`. `Config` com `ID uuid`, `Env`, `Group`, `Favorite`, `ReadOnly`, `AskPassword`, `Options`. | Valores normalizados: nil, bool, int64, float64, `Number`, string, []byte, time.Time, `JSON`, []any. Mapeiam 1:1 para a mensagem protobuf `Value`. |
| `internal/driver/postgres` | Driver PG (pgx v5) | `New()`, `Split`, `StatementAt`, `BuiltinFunctions`; Conn: explorer completo, `Objects`, `DataQuery`, `Describe` (DDL fiel), `RowKey`/`Apply`, `Ping`; Session: cancelamento server‑side (a sessão sobrevive), notices, transações, `SearchPath`, `Alive`. | Também `Explainer` (EXPLAIN JSON, ANALYZE sempre revertido), `Importer` (COPY) e `Listener` (LISTEN/NOTIFY). Pede PG ≥ 12. |
| `internal/store` | Config e segredos | `ConfigDir`, `StateDir`, `ConnectionsFile`, `Load/SaveConnections` (TOML atómico, 0600), `Validate`, `DefaultPort`, `ParseURI`/`FormatURI`, `Secrets` (`Keyring()`, `NewMemory()`), `ErrNoSecret`, `ErrKeyringUnavailable`, `Envs` | `Validate` aceita local/dev/qa/prod. |
| `internal/workspace` | Ficheiros, scratches, sessão, histórico | `Open(stateDir)`, `ReadFile`/`WriteFile` (atómico, CRLF/BOM), `Bind`/`Binding` (ficheiro→conexão, fora do ficheiro), `NewScratch`, `Scratches`, `Load/SaveSession`, `AppendHistory`/`History`/`ClearHistory` (JSONL com rotação) | `Session`/`TabState` têm campos de layout da TUI (SideW, ResH…). Na app, adaptar para guardar o layout do frontend como JSON opaco. |
| `internal/export` | Export em streaming | `Write`/`WriteFile(ctx, path, cols, src, opts, progress)` para CSV, TSV, JSON, NDJSON, SQL INSERT (PG/Oracle/genérico) e Markdown; `Text`, `JSONValue`, `SQLLiteral`, `QuoteIdent`, `UniqueNames`, `FormatFromExt` | Memória constante (1M linhas ≈ 1,3 MB). |
| `internal/sqlsafe` | Classificação de risco | `Analyze(lang, stmt) Report{Risk, Verb, Reason}`, `IsWrite` | Para as confirmações PROD e o read-only. SQL, Mongo e Redis. |
| `internal/params` | Bind parameters | `Find(lang, stmt)`, `Substitute(lang, stmt, values, dialect)` | `:nome`, `$1`, Oracle `:1`. |
| `internal/shell` | CLI nativa autenticada | `Command(cfg, password, scope) *Launch{Cmd, Cleanup, Display}`, `Find` | psql, `sql`/`sqlplus`, `mongosh`, `redis-cli`; credenciais nunca no argv. Na app: correr num terminal embutido (PTY + xterm.js, §8) ou abrir o emulador do sistema. |
| `internal/complete` | Motor de autocomplete | `Suggest(Request{Lang, Text, Cursor, Objects, SearchPath}) Result{Start, End, Prefix, Items}` | SQL com aliases, qualificação e quoting; Mongo; Redis. Exposto por RPC ao CM6 (`CompletionSource` assíncrono). |

## 6. Pacotes acrescentados na app (2026-10-14)

O `_wip/` foi esvaziado e removido; tudo passou para `internal/` com testes (unitários e de integração).

| Pacote | O que faz |
|---|---|
| `internal/driver/oracle` | go-ora v2: splitter PL/SQL com `/`, comandos SQL*Plus, DBMS_OUTPUT, cancelamento, catálogo, `Describe` com DDL, edição por ROWID, `EXPLAIN PLAN`. Testes contra `gvenzl/oracle-free` (`QUARRY_TEST_ORA_DSN`). |
| `internal/driver/mongo` | mongo-driver v2: subconjunto de mongosh (lexer/parser próprios), cursores, transações (replica set), edição por `_id`, `explain`. Testes contra `mongo:7 --replSet rs0` (`QUARRY_TEST_MONGO_DSN`). |
| `internal/driver/redis` | go-redis v9: single/cluster/sentinel, árvore por `:`, valores por tipo, `SetTTL`. |
| `internal/tunnel` | Túnel SSH (jump hosts, known_hosts com TOFU opcional, keepalive, religação); aberto pelo `service/connection` quando há opções `ssh_*`. |
| `internal/importers` | `.pgpass`, `pg_service.conf`, `tnsnames.ora` → `ConnectionService.DiscoverConnections`. |
| `internal/importcsv` | Deteção de formato/encoding, leitura em streaming e mapeamento de colunas → `ImportService` (`driver.Importer`, só PG). |

## 7. API Connect (esboço inicial)

`proto/quarry/v1/`, package `quarry.v1`, `go_package` em `github.com/gxd3v/quarry/gen/quarry/v1`.

```proto
message Value { oneof kind {
  bool null = 1; bool bool = 2; int64 int = 3; double float = 4; string number = 5; // decimal exato
  string text = 6; bytes bytes = 7; google.protobuf.Timestamp time = 8; string json = 9; ValueList array = 10; } }
message Row { repeated Value values = 1; }
message Column { string name = 1; string type = 2; }

service ConnectionService {
  rpc List(ListConnectionsRequest) returns (ListConnectionsResponse);          // configs + estado
  rpc Save(SaveConnectionRequest) returns (SaveConnectionResponse);            // valida, grava, keyring
  rpc Delete(DeleteConnectionRequest) returns (DeleteConnectionResponse);
  rpc Test(TestConnectionRequest) returns (TestConnectionResponse);
  rpc ParseURI(ParseURIRequest) returns (ParseURIResponse);
  rpc Connect(ConnectRequest) returns (ConnectResponse);                       // password opcional (AskPassword)
  rpc Disconnect(DisconnectRequest) returns (DisconnectResponse);
  rpc WatchStatus(WatchStatusRequest) returns (stream ConnectionStatus);       // ligado/caiu/latência/reconexão
}
service ExplorerService {
  rpc Children(ChildrenRequest) returns (ChildrenResponse);                    // conn_id + node path
  rpc Catalog(CatalogRequest) returns (CatalogResponse);                       // objetos p/ autocomplete e "go to object"
  rpc Describe(DescribeRequest) returns (DescribeResponse);                    // colunas, índices, constraints, triggers, stats, DDL
}
service QueryService {
  rpc OpenTab(OpenTabRequest) returns (OpenTabResponse);                       // tab_id ↔ (conn, scope); sessão abre lazy
  rpc Rebind(RebindRequest) returns (RebindResponse);
  rpc CloseTab(CloseTabRequest) returns (CloseTabResponse);
  rpc Analyze(AnalyzeRequest) returns (AnalyzeResponse);                       // split + statementAt + riscos (sqlsafe) + params
  rpc Run(RunRequest) returns (stream RunEvent);                              // StatementStarted | Columns | RowBatch | Done{affected,tag,elapsed} | Error{msg,code,position} | Message
  rpc Fetch(FetchRequest) returns (stream RowBatch);                          // mais páginas de um resultado aberto
  rpc Cancel(CancelRequest) returns (CancelResponse);
  rpc SetAutoCommit / Commit / Rollback (...)
  rpc Complete(CompleteRequest) returns (CompleteResponse);                    // internal/complete
}
service DataService {      // vista de tabela
  rpc Open(OpenDataRequest) returns (stream RunEvent);                        // DataViewer.DataQuery(filter, order)
  rpc RowKey(RowKeyRequest) returns (RowKeyResponse);
  rpc Apply(ApplyRequest) returns (ApplyResponse);                            // RowChange[]
}
service ExportService { rpc Export(ExportRequest) returns (stream ExportProgress); }
service WorkspaceService { /* ficheiros, bindings, scratches, sessão (layout JSON opaco), histórico */ }
service ShellService { /* Start → stream TerminalOutput; Input/Resize unary; Stop */ }
```

Regras:
- **Erros:** `connect.Error` com códigos (`InvalidArgument`, `Unavailable`, `FailedPrecondition`…) e detalhes protobuf para a `QueryError` (position, code, detail, hint).
- **Linhas em lotes:** primeiro lote pequeno, para o primeiro pixel aparecer depressa (ex.: 200 linhas); depois lotes maiores (ex.: 2000).
- **Cancelamento:** fechar o stream cancela o run, e `Cancel(run_id)` existe para cancelar sem fechar.

## 8. Frontend

**Layout (como na TUI, agora com pixels):**
- **Topo:** menu (ou menus nativos do Wails) e barra de tabs; cada tab tem um ponto com a cor do ambiente, o título, `@conexão` e um ✕.
- **Barra de contexto:** badge com o ambiente, conexão › scope (clicável para trocar), AUTO-COMMIT/MANUAL, `● TX [Commit] [Rollback]`, ▶ Run, ■ Stop e Shell.
- **Explorer à esquerda:** lazy, com filtro, grupos, ★ favoritos e menus de contexto; arrastar um objeto para o editor insere o nome.
- **Centro:** o editor (CM6) ou a vista de tabela.
- **Em baixo:** resultados (Result N / Messages / History).
- **Status bar:** conexão, latência e mensagens.
- **Painéis** redimensionáveis por arrasto (ver uma lib de split panes compatível com Svelte 5, ou fazer à medida).

**Componentes:**
- `Explorer`, `TabBar`, `ContextBar`, `Editor` (CM6 com highlighting por dialeto, autocomplete por RPC, procurar/substituir nativo, ir para linha, marca de erro na posição devolvida pelo servidor);
- `ResultGrid` (virtual, colunas redimensionáveis, seleção de células e intervalos, copiar TSV/CSV/JSON/Markdown/INSERT, ordenação, colunas congeladas, edição inline com alterações pendentes);
- `TableView` (sub-tabs Data/Columns/Indexes/Constraints/Triggers/DDL/Stats, barra de filtro WHERE, filtro rápido);
- `CellViewer`/`CellEditor` (JSON formatado, hex);
- `CommandPalette` e "Go to object" (fuzzy);
- `ConnectionDialog` (colar um URI preenche o formulário, Test, keyring);
- diálogos de confirmação (PROD/risco), formulário de parâmetros, toasts;
- `ExplainView` (árvore com barras de custo e tempo);
- `Terminal` (xterm.js) para a shell.

**Design tokens: Catppuccin Mocha**

```css
--rosewater:#f5e0dc; --flamingo:#f2cdcd; --pink:#f5c2e7; --mauve:#cba6f7; --red:#f38ba8; --maroon:#eba0ac;
--peach:#fab387; --yellow:#f9e2af; --green:#a6e3a1; --teal:#94e2d5; --sky:#89dceb; --sapphire:#74c7ec;
--blue:#89b4fa; --lavender:#b4befe; --text:#cdd6f4; --subtext1:#bac2de; --subtext0:#a6adc8;
--overlay2:#9399b2; --overlay1:#7f849c; --overlay0:#6c7086; --surface2:#585b70; --surface1:#45475a;
--surface0:#313244; --base:#1e1e2e; --mantle:#181825; --crust:#11111b;
/* papéis */ --accent: var(--mauve); --muted: var(--overlay1); --faint: var(--overlay0); --border: var(--surface0);
/* ambientes */ --env-prod: var(--red); --env-qa: var(--peach); --env-dev: var(--green); --env-local: var(--blue);
```

Highlighting SQL ao estilo Catppuccin:
- keywords em mauve, tipos em yellow, funções em blue;
- strings em green, números em peach;
- comentários em overlay1 itálico, operadores em sky;
- identificadores entre aspas em flamingo, parâmetros em maroon.

## 9. Features (IDs herdados da spec original)

Estado do backend: **✓ pronto**, **○ por fazer**. A UI de A1–A3 está feita; o resto está na ordem da §10.

| Área | IDs | Backend |
|---|---|---|
| Conexões: gestor, TOML + keyring, colar URI, várias ativas, ambiente/cor, read-only, TLS (sslmode) | C-01..C-08 | ✓ |
| Resiliência: ping/latência, reconexão | C-09 | ✓ (`Ping`/`Alive`; a lógica de reconexão é para refazer no `service/`) |
| SSH tunnel | C-10 | ✓ (`tunnel`) |
| Grupos/favoritos | C-11 | ✓ (campos em `Config`) |
| Importar pgpass/pg_service/tnsnames | C-12 | ✓ (`importers`, `DiscoverConnections`) |
| Explorer lazy, cache de metadados, ações, filtro, go to object | E-01..E-04 | ✓ (PG) |
| Gerar SQL, viewer de objeto, DDL, stats, arrastar para o editor | E-05..E-09 | ✓ (PG: `Describe`) |
| Editor: edição, seleção, clipboard, undo, highlighting, procurar/substituir | D-01..D-03, D-06..D-08, D-10 | CM6 (frontend) |
| Splitter, statement no cursor | D-04, D-05 | ✓ (PG, Oracle, Mongo, Redis) |
| Autocomplete | D-09 | ✓ (`complete` + `Catalog`) |
| Marca de erro na posição | D-11 | ✓ (`QueryError.Position`) |
| Ficheiros e scratches com binding à conexão | D-12 | ✓ (`workspace`) |
| Parâmetros | D-13 | ✓ (`params`) |
| Formatador SQL | D-14 | ○ (frontend) |
| Execução assíncrona, cancelamento, mensagens, vários resultados, fetch progressivo, tipos | X-01..X-08 | ✓ (PG) |
| Viewer de célula, copiar como, histórico | X-09..X-11 | ✓ (`export`, `workspace`) |
| Ordenar/congelar, transações visíveis, proteções PROD | X-12..X-14 | ✓ (`sqlsafe`, `Transactional`) |
| EXPLAIN visual | X-15 | ✓ (`QueryService.Explain`: PG, Oracle, Mongo) |
| Vista de dados com filtro, ordenação e filtro rápido | V-01..V-04 | ✓ (`DataViewer`) |
| Edição inline (alterações pendentes, Apply numa transação, chave PK/unique/ROWID/_id) | V-05..V-08 | ✓ (PG `Editable`) |
| Export CSV/JSON/NDJSON/INSERT/Markdown em streaming | V-09, V-10 | ✓ |
| Import CSV | V-11 | ✓ (`ImportService`, PG) |
| Documentos Mongo, valores Redis | V-12, V-13 | ✓ (drivers) |
| Shell nativa autenticada | S-01..S-05 | ✓ (`shell`); a UI tem de passar a terminal embutido |
| Terminal embutido | S-06 | ✓ (`ShellService` com PTY; frontend: xterm.js) |
| Temas (Latte/Frappé/Macchiato), ícones | U-12, U-13 | ○ (CSS) |
| Command palette, keymap configurável, restauro do workspace | U-06..U-11 | ✓ (`workspace`) / frontend |

## 10. Plano por fases

Cada fase fecha com um critério de aceitação testável. Implementação: agentes em paralelo, um por pacote ou área disjunta; depois a sessão principal integra, corre a suite e faz commit por componente.

**A0 · Fundação e validação (feita a 2026-10-13).** Aceite quando a app abre no Linux e no Windows, faz stream de 100k linhas por Connect para a grelha virtual a ≥ 50 fps de scroll no WebKitGTK, e o CM6 edita um script de 5k linhas sem lag.
- [x] Wails v2.16 na raiz (`main.go`, `app.go`, `wails.json`, `build/`); frontend Svelte 5 + Vite 8 + TypeScript 6 escrito de raiz (o template ainda usava a API do Svelte 4).
- [x] `buf.yaml` + `buf.gen.yaml` com plugins locais: `go tool protoc-gen-go`/`protoc-gen-connect-go` (fixados no `go.mod` com `tool`) → `gen/`; `@bufbuild/protoc-gen-es` do `frontend/node_modules` → `frontend/src/lib/gen`. O código gerado vai para o git; o CI verifica que está atualizado.
- [x] `proto/quarry/v1/common.proto` (Value/Row/Column) e `DevService.StreamRows` + `ReportBench`.
- [x] `server/`: `127.0.0.1:0` + token (ver §3; o AssetServer acumula a resposta no Windows).
- [x] Frontend: cliente Connect (binário), grelha virtual (linhas e colunas virtualizadas, buffer compacto de 1 string por linha), CM6 com SQL e tema Mocha.
- [x] Medido com `quarry --bench-out <ficheiro>`: o frontend corre o benchmark sozinho na webview real e o Go grava o relatório JSON e fecha a app.

| 100k linhas × 12 colunas | Windows (WebView2 154) | Linux (WebKitGTK 2.52, WSLg, render por software) |
|---|---|---|
| Primeiro lote / stream completo | 17 ms / 0,57 s | 41 ms / 0,93 s |
| Scroll da grelha, passos de 66 px | 144 fps (limite do ecrã) | 62 fps (limite do ecrã) |
| Scroll da grelha, saltos de 4000 px | 76 fps | 28 fps |
| Escrita no script de 5k linhas (2 frames por tecla) | mediana 14 ms | mediana 21 ms, p95 33 ms |
| Scroll do editor | 144 fps | 66 fps |

Notas: no WSLg o WebKitGTK não tinha acesso a `/dev/dri` (render por software), por isso os números do Linux são o pior caso. A descodificação protobuf corre na thread da UI e causa 4–5 frames longos (até 300 ms) durante o stream de 100k linhas; se incomodar, passar a descodificação para um Web Worker.

**A1 · PostgreSQL de ponta a ponta (feita).** Aceite quando se cria uma conexão PG, se navegam os schemas e se corre e cancela uma query de 1M linhas sem a UI parar.
- Serviços Connection/Explorer/Query sobre `internal/`.
- UI: diálogo de conexão, explorer, tabs com binding, barra de contexto, editor, grelha, Messages, Stop, toasts.

**A2 · Editor IDE e produtividade (feita).** Aceite com um dia de trabalho real em PG sem abrir o DBeaver.
- Autocomplete por RPC, marca de erro, ficheiros/scratches, restauro da sessão, histórico, command palette, keymap configurável, go to object, viewer de célula, arrastar para o editor, grupos e favoritos.

**A3 · Dados e segurança (feita a 2026-10-14).** Aceite quando se editam e aplicam 50 alterações numa transação e se exporta 1M linhas para CSV em streaming.
- Vista de tabela (filtro, ordenação no servidor, filtro rápido, edição, Apply, confirmação PROD), Describe/DDL, export com progresso, guardas de risco e read-only, parâmetros, transações visíveis, reconexão.

**A4 · Oracle e shell (feita a 2026-10-14).** Aceite com A1–A3 a funcionar contra Oracle Free e a shell a abrir autenticada no scope.
- [x] Driver Oracle (`internal/driver/oracle`), com CI contra Oracle Free.
- [x] Backend do terminal: `ShellService` com PTY (creack/pty, conpty).
- [x] UI: terminal embutido com xterm.js (tabs que mantêm o scrollback, até 3 abertos, xterm carregado a pedido).

**A5 · MongoDB e Redis (feita a 2026-10-14).** Aceite quando se consultam, filtram e editam documentos e keys.
- [x] Drivers MongoDB e Redis, com CI.
- [x] UI de documentos (árvore/JSON, edição do documento inteiro) e de keys por tipo, com TTL.

**A6 · Polimento e distribuição (v1.0).** Aceite com a v1.0 publicada (deb/rpm/AppImage, AUR, Homebrew, Scoop, winget).
- [x] Backend: SSH tunnel, EXPLAIN (PG/Oracle/Mongo), LISTEN/NOTIFY, import CSV, importers de conexões.
- [x] UI: campos SSH, EXPLAIN visual, painel de notificações, import CSV, importar conexões, formatador SQL (sql-formatter), temas Catppuccin.
- [x] Auto-update: verificação diária da última release no GitHub, com aviso e link (a app não se substitui; ver `internal/update`).
- [ ] Migração para Wails v3: adiada, a 2026-10-14 o v3 ainda está em beta (v3.0.0-beta.29).
- [x] Empacotamento: workflow `release.yml` (deb, rpm, AppImage, zip e instalador NSIS, app macOS universal) e manifestos AUR/Homebrew/Scoop/winget; ver `docs/RELEASING.md`.
- [ ] Publicar a v1.0: precisa de licença, das contas dos gestores de pacotes e da tua aprovação.

## 11. Requisitos não funcionais

| Área | Requisito |
|---|---|
| Arranque | Janela utilizável em < 1 s; conexões em segundo plano |
| Fluidez | Nenhum I/O no thread da UI; grelha virtual com scroll suave a 100k linhas carregadas no WebKitGTK |
| Memória | Buffer de resultados limitado (ex.: 100k linhas, configurável); export em streaming |
| Segurança | Segredos só no keyring; nunca em logs, histórico ou argv. API local autenticada (token), ou sem porta (AssetServer). |
| Robustez | Uma conexão perdida nunca fecha a app; recover de panics nos handlers Connect (interceptor) |
| Logs | `log/slog` para `StateDir/quarry.log` com rotação; `--debug` |
| Testes | Go: unit + integração (containers). Frontend: Vitest + Testing Library; e2e com Playwright contra o `wails dev` no browser. |
| Distribuição | `wails build`; pacotes por plataforma (A6) |

## 12. Riscos e decisões em aberto

| Risco | Mitigação |
|---|---|
| Performance do WebKitGTK (grelha, editor) | Validar na A0; canvas se o DOM virtual não chegar |
| Streaming pelo AssetServer do Wails v2 | Testar na A0; fallback para `127.0.0.1:0` + token |
| Wails v3 ainda beta | Ficar na v2; isolar o código Wails em `main.go`/`app.go` |
| Oracle (tipos, PL/SQL, go-ora) | Integração contra Oracle Free desde o primeiro dia da A4 |

Decisões em aberto:
- Licença: MIT ou Apache-2.0?
- ~~Nome final do produto~~ → **Quarry** (decidido a 2026-10-13).
- Estado partilhado do layout: só no frontend (localStorage) ou guardado pelo `workspace` (recomendado, por ser portável)?

## 13. Ambiente de desenvolvimento

- **Máquina:** Windows 11 (Git Bash + PowerShell); produto Linux-first. WSL Ubuntu disponível (Stopped), útil para testar no WebKitGTK com WSLg.
- **Go:** 1.27 (o `go.mod` fixa a toolchain `go1.27.2`, descarregada sozinha).
- **Node:** 24.21, npm 11 (sem pnpm). `buf` 1.37 instalado. gcc (MinGW) disponível. Wails CLI ainda não instalado (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`, depois `wails doctor`).
- **Postgres de testes:** o container antigo chama-se `yatui-pg` (password `yatui`). Recriar uma vez com o nome novo: `docker rm -f yatui-pg; docker run -d --name quarry-pg -e POSTGRES_PASSWORD=quarry -p 55432:5432 postgres:17`. Daí em diante: `docker start quarry-pg`, depois `QUARRY_TEST_PG_DSN='postgres://postgres:quarry@localhost:55432/postgres?sslmode=disable' go test -race ./...`. O fixture `quarry_it` é criado pelos testes de integração. O Docker Desktop já caiu sob pressão de memória: reiniciar com `wsl --shutdown` e voltar a abrir o Docker Desktop.
