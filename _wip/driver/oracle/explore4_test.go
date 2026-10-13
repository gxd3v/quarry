package oracle

import (
	"context"
	"database/sql"
	sqldrv "database/sql/driver"
	"fmt"
	"io"
	"os"
	"testing"

	go_ora "github.com/sijms/go-ora/v2"
)

func TestExplore4(t *testing.T) {
	if os.Getenv("ORA_EXPLORE") == "" {
		t.Skip()
	}
	ctx := context.Background()
	cfg, err := go_ora.ParseConfig(go_ora.BuildUrl("localhost", 51521, "FREEPDB1", "system", "quarry", nil))
	if err != nil {
		t.Fatal(err)
	}
	c, err := go_ora.NewConnection("", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.OpenWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exec := func(q string, args ...any) {
		var nv []sqldrv.NamedValue
		for i, a := range args {
			nv = append(nv, sqldrv.NamedValue{Ordinal: i + 1, Value: a})
		}
		r, err := c.ExecContext(ctx, q, nv)
		var n int64 = -2
		if r != nil {
			n, _ = r.RowsAffected()
		}
		fmt.Printf("exec %.60q: %d %v\n", q, n, err)
	}
	query := func(q string) {
		rows, err := c.QueryContext(ctx, q, nil)
		if err != nil {
			fmt.Println("query err", err)
			return
		}
		defer rows.Close()
		cols := rows.Columns()
		for i, n := range cols {
			tn := rows.(sqldrv.RowsColumnTypeDatabaseTypeName).ColumnTypeDatabaseTypeName(i)
			p, s, _ := rows.(sqldrv.RowsColumnTypePrecisionScale).ColumnTypePrecisionScale(i)
			fmt.Println(" col", n, tn, p, s)
		}
		dest := make([]sqldrv.Value, len(cols))
		for {
			if err := rows.Next(dest); err != nil {
				if err != io.EOF {
					fmt.Println("next err", err)
				}
				break
			}
			for i, v := range dest {
				fmt.Printf("  %s: %T %#v\n", cols[i], v, v)
			}
		}
	}
	exec("drop table yat_types purge")
	exec(`create table yat_types (b boolean, j json, nv nvarchar2(10), nc nchar(3), tl timestamp with local time zone, l long, x xmltype, v varchar2(10 char), f float(10), bf binary_float, i integer, n number(5,0), n2 number(*,0), vec vector(3, float32))`)
	exec(`insert into yat_types values (true, '{"a":[1,2]}', 'nv', 'nc', timestamp '2024-01-02 03:04:05', 'long text', xmltype('<a>1</a>'), 'vv', 1.5, 0.1, 42, 7, 8, '[1,2,3]')`)
	query("select b, j, nv, nc, tl, v, f, bf, i, n, n2 from yat_types")
	query("select l from yat_types")
	query("select x from yat_types")
	query("select vec from yat_types")
	query("select json_serialize(j) js, rowid r, cast(rowid as urowid) ur from yat_types")
	// interleave
	rows, err := c.QueryContext(ctx, "select level from dual connect by level <= 1000", nil)
	fmt.Println("q", err)
	dest := make([]sqldrv.Value, 1)
	rows.Next(dest)
	fmt.Println("first", dest[0])
	exec("begin dbms_output.enable(null); dbms_output.put_line('x'); end;")
	var out go_ora.Clob
	exec(`DECLARE l DBMS_OUTPUT.CHARARR; n INTEGER := 1000; r CLOB; BEGIN DBMS_OUTPUT.GET_LINES(l, n); FOR i IN 1..n LOOP r := r || l(i) || CHR(10); END LOOP; :1 := r; END;`, sql.Out{Dest: &out})
	fmt.Printf("out %q\n", out.String)
	cnt := 1
	for rows.Next(dest) == nil {
		cnt++
	}
	fmt.Println("rows", cnt)
	rows.Close()
	exec("set transaction read only")
	exec("explain plan set statement_id = 'x1' for select * from dual")
	exec("commit")
	exec("create table yat_d (d date, t timestamp)")
	exec("insert into yat_d values (to_date(:1, 'YYYY-MM-DD HH24:MI:SS'), to_timestamp(:2, 'YYYY-MM-DD HH24:MI:SS.FF9'))", "2024-01-02", "2024-01-02 03:04")
	query("select * from yat_d")
	exec("drop table yat_d purge")
	exec("create table yat_i (id number primary key, a number, b varchar2(10), constraint u1 unique (b))")
	exec("create index yat_i_a on yat_i (a desc, upper(b))")
	exec("comment on table yat_i is 'c'")
	var ddl go_ora.Clob
	exec(`DECLARE r CLOB; BEGIN
DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'STORAGE', FALSE);
DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SEGMENT_ATTRIBUTES', FALSE);
DBMS_METADATA.SET_TRANSFORM_PARAM(DBMS_METADATA.SESSION_TRANSFORM, 'SQLTERMINATOR', TRUE);
r := DBMS_METADATA.GET_DDL('TABLE', 'YAT_I') || DBMS_METADATA.GET_DEPENDENT_DDL('INDEX', 'YAT_I') || DBMS_METADATA.GET_DEPENDENT_DDL('COMMENT', 'YAT_I'); :1 := r; END;`, sql.Out{Dest: &ddl})
	fmt.Println(ddl.String)
	exec("drop table yat_i purge")
	exec("begin raise_application_error(-20001, 'boom'); end;")
	exec("create or replace procedure yat_bad as begin nosuch; end;")
	exec("drop procedure yat_bad")
}
