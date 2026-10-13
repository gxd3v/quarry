package sqlsafe

import (
	"strings"
	"testing"

	"github.com/gxd3v/quarry/internal/driver"
)

type tcase struct {
	stmt   string
	risk   Risk
	write  bool
	verb   string // checked when not empty
	reason string // substring, checked when not empty
}

func run(t *testing.T, lang driver.Language, tests []tcase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.stmt, func(t *testing.T) {
			r := Analyze(lang, tt.stmt)
			if r.Risk != tt.risk {
				t.Errorf("Risk = %v, want %v (%+v)", r.Risk, tt.risk, r)
			}
			if w := IsWrite(lang, tt.stmt); w != tt.write {
				t.Errorf("IsWrite = %v, want %v", w, tt.write)
			}
			if tt.verb != "" && r.Verb != tt.verb {
				t.Errorf("Verb = %q, want %q", r.Verb, tt.verb)
			}
			if tt.reason != "" && !strings.Contains(r.Reason, tt.reason) {
				t.Errorf("Reason = %q, want it to contain %q", r.Reason, tt.reason)
			}
			if r.Risk > None && r.Reason == "" {
				t.Errorf("risky report without reason: %+v", r)
			}
		})
	}
}

func TestSQLReads(t *testing.T) {
	run(t, driver.LangSQL, []tcase{
		{"", None, false, "", ""},
		{"  -- just a comment\n/* and another */", None, false, "", ""},
		{"select 1", None, false, "SELECT", ""},
		{"SELECT * FROM orders WHERE id = 1", None, false, "SELECT", ""},
		{"select * from orders for update", None, false, "SELECT", ""},
		{"select 'delete from orders' as s", None, false, "SELECT", ""},
		{"select 1 -- ; drop table orders", None, false, "SELECT", ""},
		{"select /* drop table x; */ 1", None, false, "SELECT", ""},
		{"select $$ ; delete from t $$", None, false, "SELECT", ""},
		{"select $tag$ drop table t; $tag$", None, false, "SELECT", ""},
		{`select E'it\'s; drop table t'`, None, false, "SELECT", ""},
		{"select q'[it's; drop table t]' from dual", None, false, "SELECT", ""},
		{"select x::text, y::int from t", None, false, "SELECT", ""},
		{"select \"delete\" from \"update\"", None, false, "SELECT", ""},
		{"values (1), (2)", None, false, "VALUES", ""},
		{"table orders", None, false, "TABLE", ""},
		{"show search_path", None, false, "SHOW", ""},
		{"set search_path = app", None, false, "SET", ""},
		{"SET TRANSACTION READ ONLY", None, false, "SET", ""},
		{"reset all", None, false, "RESET", ""},
		{"begin", None, false, "BEGIN", ""},
		{"BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE", None, false, "BEGIN", ""},
		{"start transaction", None, false, "START", ""},
		{"commit", None, false, "COMMIT", ""},
		{"rollback", None, false, "ROLLBACK", ""},
		{"savepoint a", None, false, "SAVEPOINT", ""},
		{"explain select * from orders", None, false, "EXPLAIN", ""},
		{"explain delete from orders", None, false, "EXPLAIN", ""},
		{"explain (costs off) update orders set x = 1", None, false, "EXPLAIN", ""},
		{"explain (analyze false) delete from orders", None, false, "EXPLAIN", ""},
		{"explain analyze select 1", None, false, "SELECT", ""},
		{"EXPLAIN PLAN FOR DELETE FROM orders", None, false, "EXPLAIN", ""},
		{"with a as (select 1) select * from a", None, false, "SELECT", ""},
		{"with recursive r(n) as (select 1 union all select n+1 from r) select * from r", None, false, "SELECT", ""},
		{"declare c cursor for select * from orders", None, false, "SELECT", ""},
		{"declare c no scroll cursor with hold for select 1", None, false, "SELECT", ""},
		{"fetch 10 from c", None, false, "FETCH", ""},
		{"copy orders to stdout", None, false, "COPY", ""},
		{"copy (select * from orders) to '/tmp/x'", None, false, "COPY", ""},
		{"alter session set current_schema = hr", None, false, "ALTER SESSION", ""},
		{"select * from ação where descrição = 'x'", None, false, "SELECT", ""},
		{"prepare q as select $1", None, false, "PREPARE", ""},
	})
}

func TestSQLWrites(t *testing.T) {
	run(t, driver.LangSQL, []tcase{
		{"insert into orders values (1)", Write, true, "INSERT", "orders"},
		{"INSERT INTO app.orders (id) SELECT id FROM old", Write, true, "INSERT", "app.orders"},
		{"insert into t values (1) on conflict (id) do update set x = 1", Write, true, "INSERT", ""},
		{"merge into t using s on (t.id = s.id) when matched then delete", Write, true, "MERGE", ""},
		{"update orders set x = 1 where id = 2", Write, true, "UPDATE", "orders"},
		{"UPDATE orders SET x = 1 WHERE true", Write, true, "UPDATE", ""},
		{"update orders set x = 1 where 1=1", Write, true, "UPDATE", ""},
		{"update only orders set x = 1 where id = 2", Write, true, "UPDATE", ""},
		{"update t set x = o.x from other o where t.id = o.id", Write, true, "UPDATE", ""},
		{"update t set x = 1 where current of c", Write, true, "UPDATE", ""},
		{"delete from orders where id = 1", Write, true, "DELETE", "orders"},
		{"delete from t using u where t.id = u.id", Write, true, "DELETE", ""},
		{"delete from t where id in (select id from u)", Write, true, "DELETE", ""},
		{"DELETE orders WHERE id = 1", Write, true, "DELETE", "orders"},
		{"delete from \"Order Lines\" where id = 1", Write, true, "DELETE", `"Order Lines"`},
		{"delete from pedidos_ação where id = 1", Write, true, "DELETE", "pedidos_ação"},
		{"delete from (select * from t where x = 1)", Write, true, "DELETE", ""},
		{"select * into backup from orders", Write, true, "SELECT INTO", "backup"},
		{"select * into temp table backup from orders", Write, true, "SELECT INTO", "backup"},
		{"copy orders from stdin", Write, true, "COPY FROM", "orders"},
		{"copy orders (a, b) from '/tmp/x' with (format csv)", Write, true, "COPY FROM", ""},
		{"call refresh_stats()", Write, true, "CALL", "refresh_stats"},
		{"execute q(1)", Write, true, "EXECUTE", ""},
		{"exec dbms_stats.gather_table_stats('HR', 'EMP')", Write, true, "EXEC", ""},
		{"do $$ begin perform 1; end $$", Write, true, "DO", ""},
		{"do $$ begin delete from t where id = 1; end $$", Write, true, "", ""},
		{"begin null; end;", Write, true, "BEGIN", ""},
		{"BEGIN DBMS_OUTPUT.PUT_LINE('x'); END;", Write, true, "BEGIN", ""},
		{"declare v number; begin select count(*) into v from t; end;", Write, true, "DECLARE", ""},
		{"begin\n  for r in (select * from t where x = 1) loop\n    update t set y = 1 where id = r.id;\n  end loop;\nend;", Write, true, "", ""},
		{"vacuum analyze orders", Write, true, "VACUUM", ""},
		{"refresh materialized view mv", Write, true, "REFRESH", ""},
		{"lock table orders in access exclusive mode", Write, true, "LOCK", ""},
		{"explain analyze insert into t values (1)", Write, true, "INSERT", ""},
		{"explain (analyze, buffers) update t set x = 1 where id = 1", Write, true, "UPDATE", ""},
		{"with d as (delete from t where id = 1 returning *) select * from d", Write, true, "DELETE", ""},
		{"with s as (select 1) insert into t select * from s", Write, true, "INSERT", ""},
		{"copy (delete from t where x = 1 returning *) to stdout", Write, true, "DELETE", ""},
		{"listen_to_me()", None, true, "", ""},
		{"notify chan", None, true, "NOTIFY", ""},
		{"frobnicate everything", None, true, "FROBNICATE", ""},
	})
}

func TestSQLUnbounded(t *testing.T) {
	run(t, driver.LangSQL, []tcase{
		{"delete from orders", UnboundedWrite, true, "DELETE", "DELETE without WHERE affects every row of orders"},
		{"DELETE FROM app.orders;", UnboundedWrite, true, "DELETE", "app.orders"},
		{"delete orders", UnboundedWrite, true, "DELETE", "orders"},
		{"update orders set x = 1", UnboundedWrite, true, "UPDATE", "UPDATE without WHERE affects every row of orders"},
		{"update t set x = (select y from u where u.id = t.id)", UnboundedWrite, true, "UPDATE", ""},
		{"update t set x = 'where'", UnboundedWrite, true, "UPDATE", ""},
		{"update t set x = 1 -- where id = 1", UnboundedWrite, true, "UPDATE", ""},
		{"delete from t /* where id = 1 */", UnboundedWrite, true, "DELETE", ""},
		{"delete from t returning *", UnboundedWrite, true, "DELETE", ""},
		{"update t set x = 1 from u", UnboundedWrite, true, "UPDATE", ""},
		{"delete from t using u", UnboundedWrite, true, "DELETE", ""},
		{"with d as (delete from t returning *) select count(*) from d", UnboundedWrite, true, "DELETE", ""},
		{"with x as (select 1) update t set a = 1", UnboundedWrite, true, "UPDATE", ""},
		{"explain analyze delete from t", UnboundedWrite, true, "DELETE", ""},
		{"select 1; delete from t", UnboundedWrite, true, "DELETE", ""},
		{"do $$ begin delete from t; end $$", UnboundedWrite, true, "DELETE", ""},
		{"DO 'BEGIN UPDATE t SET x = 1; END'", UnboundedWrite, true, "UPDATE", ""},
		{"begin\n  delete from t;\n  commit;\nend;", UnboundedWrite, true, "DELETE", ""},
		{"declare\n  n number;\nbegin\n  if n > 0 then\n    update t set x = 1;\n  end if;\nend;", UnboundedWrite, true, "UPDATE", ""},
		{"delete from \"Ação\"", UnboundedWrite, true, "DELETE", `"Ação"`},
	})
}

func TestSQLSchema(t *testing.T) {
	run(t, driver.LangSQL, []tcase{
		{"create table t (id int)", DDL, true, "CREATE TABLE", "t"},
		{"create table if not exists t (id int)", DDL, true, "CREATE TABLE", ""},
		{"create table t as select * from u", DDL, true, "CREATE TABLE", ""},
		{"create or replace view v as select 1", DDL, true, "CREATE VIEW", ""},
		{"create unique index concurrently i on t (x)", DDL, true, "CREATE INDEX", ""},
		{"create materialized view mv as select 1", DDL, true, "CREATE VIEW", ""},
		{"create function f() returns void as $$ delete from t $$ language sql", DDL, true, "CREATE FUNCTION", ""},
		{"create function f() returns void language plpgsql as $$ begin delete from t where x = 1; end $$", DDL, true, "CREATE FUNCTION", ""},
		{"create function f() returns int language sql begin atomic delete from t; select 1; end", DDL, true, "CREATE FUNCTION", ""},
		{"CREATE OR REPLACE PROCEDURE p IS BEGIN DELETE FROM a; UPDATE b SET x = 1; END;", DDL, true, "CREATE PROCEDURE", ""},
		{"create or replace trigger trg before insert on t for each row begin :new.id := 1; end;", DDL, true, "CREATE TRIGGER", ""},
		{"create or replace package body pk as procedure p is begin delete from t; end; end;", DDL, true, "CREATE PACKAGE", ""},
		{"alter table t add column c int", DDL, true, "ALTER TABLE", "t"},
		{"alter table t alter column c drop not null", DDL, true, "ALTER TABLE", ""},
		{"alter table t alter column c drop default", DDL, true, "ALTER TABLE", ""},
		{"alter table t rename to u", DDL, true, "ALTER TABLE", ""},
		{"alter system set work_mem = '64MB'", DDL, true, "ALTER SYSTEM", ""},
		{"comment on table t is 'drop me'", DDL, true, "COMMENT", ""},
		{"grant select on t to bob", DDL, true, "GRANT", "privileges"},
		{"revoke all on t from bob", DDL, true, "REVOKE", ""},
		{"rename emp to employees", DDL, true, "RENAME", ""},
	})
}

func TestSQLDestructive(t *testing.T) {
	run(t, driver.LangSQL, []tcase{
		{"drop table orders", Destructive, true, "DROP TABLE", "orders"},
		{"DROP TABLE IF EXISTS app.orders CASCADE", Destructive, true, "DROP TABLE", "app.orders"},
		{"drop materialized view mv", Destructive, true, "DROP MATERIALIZED VIEW", "mv"},
		{"drop index concurrently if exists i", Destructive, true, "DROP INDEX", "i"},
		{"drop package body pk", Destructive, true, "DROP PACKAGE BODY", "pk"},
		{"drop package pk", Destructive, true, "DROP PACKAGE", "pk"},
		{"drop schema app cascade", Destructive, true, "DROP SCHEMA", "app"},
		{"drop database prod", Destructive, true, "DROP DATABASE", "prod"},
		{"drop owned by app", Destructive, true, "DROP OWNED BY", "app"},
		{"truncate orders", Destructive, true, "TRUNCATE", "TRUNCATE removes every row of orders"},
		{"TRUNCATE TABLE ONLY app.orders RESTART IDENTITY", Destructive, true, "TRUNCATE", "app.orders"},
		{"alter table t drop column c", Destructive, true, "ALTER TABLE", "DROP"},
		{"alter table t drop c", Destructive, true, "ALTER TABLE", ""},
		{"alter table if exists t drop constraint t_pk", Destructive, true, "ALTER TABLE", "t"},
		{"ALTER TABLE sales TRUNCATE PARTITION p1", Destructive, true, "ALTER TABLE", ""},
		{"purge recyclebin", Destructive, true, "PURGE", ""},
		{"select 1; drop table t; delete from u", Destructive, true, "DROP TABLE", ""},
		{"begin execute immediate 'x'; drop table t; end;", Destructive, true, "DROP TABLE", ""},
		{"do $$ begin truncate t; end $$", Destructive, true, "TRUNCATE", ""},
		{"drop table \"Pedidos\"", Destructive, true, "DROP TABLE", `"Pedidos"`},
	})
}

func TestMongo(t *testing.T) {
	run(t, driver.LangMongo, []tcase{
		{"", None, false, "", ""},
		{"db.orders.find({})", None, false, "find", ""},
		{"db.orders.find({status: 'A'}).sort({x: 1}).limit(10)", None, false, "find", ""},
		{"db.orders.findOne({_id: 1})", None, false, "findOne", ""},
		{"db.orders.countDocuments({})", None, false, "countDocuments", ""},
		{"db.orders.aggregate([{$match: {a: 1}}])", None, false, "aggregate", ""},
		{"db.getCollection('orders').find()", None, false, "find", ""},
		{"db['orders'].find()", None, false, "find", ""},
		{"db.getCollectionNames()", None, false, "getCollectionNames", ""},
		{"db.stats()", None, false, "stats", ""},
		{"show collections", None, false, "show", ""},
		{"use shop", None, false, "use", ""},
		{"db.orders.find({note: 'db.orders.drop()'})", None, false, "find", ""},
		{"// db.orders.drop()\ndb.orders.find()", None, false, "find", ""},
		{"db.orders.explain().deleteMany({})", None, false, "", ""},
		{"db.runCommand({ping: 1})", None, false, "runCommand ping", ""},
		{"db.orders.insertOne({a: 1})", Write, true, "insertOne", "orders"},
		{"db.orders.insertMany([{a: 1}, {a: 2}])", Write, true, "insertMany", ""},
		{"db.orders.updateOne({_id: 1}, {$set: {a: 1}})", Write, true, "updateOne", ""},
		{"db.orders.updateMany({status: 'A'}, {$set: {a: 1}})", Write, true, "updateMany", ""},
		{"db.orders.deleteMany({status: 'A'})", Write, true, "deleteMany", ""},
		{"db.orders.replaceOne({}, {a: 1})", Write, true, "replaceOne", ""},
		{"db.orders.findOneAndUpdate({_id: 1}, {$inc: {n: 1}})", Write, true, "findOneAndUpdate", ""},
		{"db.orders.bulkWrite([{insertOne: {document: {}}}])", Write, true, "bulkWrite", ""},
		{"db.orders.aggregate([{$match: {}}, {$out: 'copy'}])", Write, true, "aggregate", "$out"},
		{"db.orders.aggregate([{\"$merge\": {into: 'x'}}])", Write, true, "aggregate", ""},
		{"db.runCommand({insert: 'orders', documents: [{}]})", Write, true, "runCommand insert", ""},
		{"db.orders.deleteMany({})", UnboundedWrite, true, "deleteMany", "deleteMany with an empty filter affects every document in orders"},
		{"db.orders.deleteMany( {   } )", UnboundedWrite, true, "deleteMany", ""},
		{"db.orders.deleteMany({ /* all */ })", UnboundedWrite, true, "deleteMany", ""},
		{"db.orders.updateMany({}, {$set: {a: 1}})", UnboundedWrite, true, "updateMany", ""},
		{"db.orders.updateOne({}, {$set: {a: 1}})", UnboundedWrite, true, "updateOne", ""},
		{"db.orders.remove({})", UnboundedWrite, true, "remove", ""},
		{"db.getCollection(\"order items\").deleteMany({})", UnboundedWrite, true, "deleteMany", "order items"},
		{"db.orders.find().forEach(d => db.archive.deleteMany({}))", UnboundedWrite, true, "deleteMany", "archive"},
		{"db.orders.createIndex({a: 1})", DDL, true, "createIndex", ""},
		{"db.orders.dropIndex('a_1')", DDL, true, "dropIndex", ""},
		{"db.createCollection('logs')", DDL, true, "createCollection", ""},
		{"db.orders.renameCollection('old')", DDL, true, "renameCollection", ""},
		{"db.orders.drop()", Destructive, true, "drop", "orders"},
		{"db.dropDatabase()", Destructive, true, "dropDatabase", ""},
		{"db.getSiblingDB('shop').dropDatabase()", Destructive, true, "dropDatabase", ""},
		{"db.runCommand({drop: 'orders'})", Destructive, true, "runCommand drop", ""},
		{"db.adminCommand({shutdown: 1})", Destructive, true, "adminCommand shutdown", ""},
		{"db.orders.find(); db.orders.drop()", Destructive, true, "drop", ""},
		{"db.orders.frobnicate()", None, true, "frobnicate", ""},
		{"var x = 1", None, true, "", ""},
		{"rs.stepDown()", None, true, "", ""},
		{"db.orders.find({name: /^a'b/}).limit(1)", None, false, "find", ""},
	})
}

func TestRedis(t *testing.T) {
	run(t, driver.LangRedis, []tcase{
		{"", None, false, "", ""},
		{"GET user:1", None, false, "GET", ""},
		{"get user:1", None, false, "GET", ""},
		{"HGETALL user:1", None, false, "HGETALL", ""},
		{"KEYS *", None, false, "KEYS", ""},
		{"SCAN 0 MATCH user:* COUNT 100", None, false, "SCAN", ""},
		{"INFO memory", None, false, "INFO", ""},
		{"PING", None, false, "PING", ""},
		{"SELECT 2", None, false, "SELECT", ""},
		{"CONFIG GET maxmemory", None, false, "CONFIG GET", ""},
		{"CLIENT LIST", None, false, "CLIENT LIST", ""},
		{"SORT mylist ALPHA", None, false, "SORT", ""},
		{"# FLUSHALL\nGET a", None, false, "GET", ""},
		{"GET \"FLUSHALL\"", None, false, "GET", ""},
		{"SET user:1 \"hello world\"", Write, true, "SET", "user:1"},
		{"DEL a b c", Write, true, "DEL", "a"},
		{"HSET user:1 name bob", Write, true, "HSET", ""},
		{"LPUSH q job", Write, true, "LPUSH", ""},
		{"EXPIRE session:1 60", Write, true, "EXPIRE", ""},
		{"INCR counter", Write, true, "INCR", ""},
		{"ZADD board 1 bob", Write, true, "ZADD", ""},
		{"SORT mylist STORE out", Write, true, "SORT", "STORE"},
		{"EVAL \"return 1\" 0", Write, true, "EVAL", ""},
		{"CONFIG REWRITE", Write, true, "CONFIG REWRITE", ""},
		{"CLIENT KILL ID 5", Write, true, "CLIENT KILL", ""},
		{"FLUSHALL", Destructive, true, "FLUSHALL", "every key"},
		{"flushdb async", Destructive, true, "FLUSHDB", ""},
		{"CONFIG SET maxmemory 1gb", Destructive, true, "CONFIG SET", ""},
		{"config set appendonly no", Destructive, true, "CONFIG SET", ""},
		{"SHUTDOWN NOSAVE", Destructive, true, "SHUTDOWN", ""},
		{"GET a\nFLUSHALL\nGET b", Destructive, true, "FLUSHALL", ""},
		{"FROBNICATE x", None, true, "FROBNICATE", ""},
	})
}

func TestUnknownLanguage(t *testing.T) {
	if !IsWrite("cql", "select 1") {
		t.Error("unknown language should be treated as a write")
	}
	if r := Analyze("cql", "drop table x"); r.Risk != None {
		t.Errorf("Analyze on unknown language = %+v", r)
	}
}

func TestRiskOrder(t *testing.T) {
	if !(None < Write && Write < DDL && DDL < UnboundedWrite && UnboundedWrite < Destructive) {
		t.Fatal("risk levels out of order")
	}
	if Destructive.String() != "destructive" {
		t.Errorf("String = %q", Destructive.String())
	}
}

func FuzzAnalyze(f *testing.F) {
	for _, s := range []string{
		"delete from t", "with d as (delete from t) select 1", "do $$ begin delete from t; end $$",
		"select 'x", "$$", "q'[", "E'\\", "/* /*", "((((", "))))", "begin", "declare",
		"create function f() begin atomic", "explain (", "copy (", "db.c.deleteMany({})",
		"db[", "db.c.find(/", "FLUSHALL", "SET \"a", "alter table t drop", "drop",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, lang := range []driver.Language{driver.LangSQL, driver.LangMongo, driver.LangRedis} {
			r := Analyze(lang, s)
			if r.Risk < None || r.Risk > Destructive {
				t.Fatalf("risk out of range: %+v", r)
			}
			if r.Risk > None && !IsWrite(lang, s) {
				t.Fatalf("risky but not a write: %+v", r)
			}
		}
	})
}
