# _wip: trabalho parcial, não revisto

O Go ignora diretórios que começam por `_`, por isso nada aqui é compilado nem testado pelo `go test ./...`. Cada pacote volta para `internal/` quando estiver terminado, testado e revisto. O estado de cada um está no [PLAN.md §6](../docs/PLAN.md#6-trabalho-parcial-em-_wip-não-revisto-pode-não-compilar).

| Diretório | Destino |
|---|---|
| `driver/mongo` | `internal/driver/mongo` (precisa de `go get go.mongodb.org/mongo-driver/v2@v2.9.2`) |
| `driver/oracle` | `internal/driver/oracle` (precisa de `go get github.com/sijms/go-ora/v2`) |
| `importcsv` | `internal/importcsv` |
