package oracle

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/network"
)

func TestExplore(t *testing.T) {
	if os.Getenv("ORA_EXPLORE") == "" {
		t.Skip()
	}
	ctx := context.Background()
	dsn := go_ora.BuildUrl("localhost", 51521, "FREEPDB1", "system", "quarry", map[string]string{"PREFETCH_ROWS": "100"})
	db := sql.OpenDB(go_ora.NewConnector(dsn))
	defer db.Close()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q := `select 1 a, 1.5 b, cast(12 as number(10,2)) c, cast(3 as number(5)) d, to_binary_float(1.25) e, to_binary_double(2.5) f,
	'txt' g, cast('c' as char(3)) h, n'nc' i, to_clob('clob') j, to_nclob('nclob') k, rowid l, interval '1-2' year to month m,
	interval '1 02:03:04.5' day to second n, date '2024-01-02' o, timestamp '2024-01-02 03:04:05.123' p,
	timestamp '2024-01-02 03:04:05 +02:00' q, hextoraw('00ff') r, to_blob(hextoraw('0102')) s, cast(1e30 as number) u, cast(null as number) v,
	cast(5 as float) w, json('{"a":1}') x, true y, localtimestamp z, 123456789012345678901234567890 aa
	from dual`
	rows, err := c.QueryContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	cts, _ := rows.ColumnTypes()
	for _, ct := range cts {
		p, s, ok := ct.DecimalSize()
		l, lok := ct.Length()
		fmt.Println(ct.Name(), ct.DatabaseTypeName(), p, s, ok, l, lok, ct.ScanType())
	}
	vals := make([]any, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			fmt.Printf("%s: %T %#v\n", cts[i].Name(), v, v)
		}
	}
	fmt.Println("rows err", rows.Err())
	rows.Close()

	_, err = c.ExecContext(ctx, "select * from\n nosuch_table x")
	fmt.Printf("err: %T %v\n", err, err)
	if oe, ok := err.(*network.OracleError); ok {
		fmt.Println("pos", oe.ErrPos(), oe.ErrCode, oe.ErrMsg)
	}
	_, err = c.QueryContext(ctx, "select 1/0 from dual")
	fmt.Printf("err: %T %v\n", err, err)

	_, err = c.ExecContext(ctx, "create or replace procedure yat_bad as begin nosuch; end;")
	fmt.Printf("compile err: %T %v\n", err, err)

	cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	start := time.Now()
	_, err = c.ExecContext(cctx, "BEGIN DBMS_SESSION.SLEEP(30); END;")
	cancel()
	fmt.Printf("cancel: %v after %v\n", err, time.Since(start))
	var n int
	err = c.QueryRowContext(ctx, "select 7 from dual").Scan(&n)
	fmt.Println("after cancel", n, err)

	_, err = c.ExecContext(ctx, "begin dbms_output.enable(null); dbms_output.put_line('hello'); dbms_output.put_line('world'); end;")
	fmt.Println("dbms", err)
	var out go_ora.Clob
	var cnt int64
	_, err = c.ExecContext(ctx, `DECLARE l DBMS_OUTPUT.CHARARR; n INTEGER := 1000; r CLOB; BEGIN DBMS_OUTPUT.GET_LINES(l, n); FOR i IN 1..n LOOP r := r || l(i) || CHR(10); END LOOP; :1 := r; :2 := n; END;`,
		sql.Out{Dest: &out}, sql.Out{Dest: &cnt})
	fmt.Printf("lines %v %q %d\n", err, out.String, cnt)
}
