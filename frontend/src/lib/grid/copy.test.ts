import { describe, expect, it } from 'vitest'
import { copyPlain, formatCopy, guessTable, isoTime, sqlIdent, sqlLiteral, uniqueNames, type CopyColumn } from './copy'

const cols: CopyColumn[] = [
  { name: 'id', type: 'int8', numeric: true, kind: 'int' },
  { name: 'name', type: 'text', numeric: false, kind: 'text' },
  { name: 'ok', type: 'bool', numeric: false, kind: 'bool' },
]
const rows = [
  ['1', 'alice', 'true'],
  ['2', 'say "hi", bob', null],
  ['3', '', 'false'],
  ['10', 'multi\nline', 'false'],
]

describe('copyPlain', () => {
  it('copies one cell verbatim and ranges as TSV', () => {
    expect(copyPlain([['a\tb']])).toBe('a\tb')
    expect(copyPlain([[null]])).toBe('')
    expect(copyPlain([['1', null], ['x\ty', '']])).toBe('1\t\n"x\ty"\t""')
  })
})

describe('formatCopy', () => {
  it('writes CSV like export (CRLF, quoting, NULL vs empty)', () => {
    expect(formatCopy('csv', cols, rows)).toBe(
      'id,name,ok\r\n1,alice,true\r\n2,"say ""hi"", bob",\r\n3,"",false\r\n10,"multi\nline",false\r\n',
    )
    expect(formatCopy('csv', cols, rows.slice(0, 1), { header: false })).toBe('1,alice,true\r\n')
  })

  it('writes TSV with LF', () => {
    expect(formatCopy('tsv', cols, rows.slice(0, 2))).toBe('id\tname\tok\n1\talice\ttrue\n2\tsay "hi", bob\t\n'.replace('say "hi", bob', '"say ""hi"", bob"'))
  })

  it('writes JSON with typed values', () => {
    const c: CopyColumn[] = [
      ...cols,
      { name: 'doc', type: 'jsonb', numeric: false, kind: 'json' },
      { name: 'f', type: 'float8', numeric: true, kind: 'float' },
      { name: 'id', type: 'numeric', numeric: true, kind: 'number' },
    ]
    expect(
      formatCopy('json', c, [
        ['1', 'a', 'true', '{"a": [1, 2]}', '1.5', '12345678901234567890.5'],
        [null, null, 'false', 'not json', 'NaN', 'NaN'],
      ]),
    ).toBe(
      '[\n' +
        '{"id":1,"name":"a","ok":true,"doc":{"a":[1,2]},"f":1.5,"id_2":12345678901234567890.5},\n' +
        '{"id":null,"name":null,"ok":false,"doc":"not json","f":null,"id_2":"NaN"}\n' +
        ']\n',
    )
    expect(formatCopy('json', cols, [])).toBe('[]\n')
  })

  it('writes a Markdown table', () => {
    expect(formatCopy('markdown', cols, rows)).toBe(
      '|  id | name          | ok    |\n' +
        '| --: | ------------- | ----- |\n' +
        '|   1 | alice         | true  |\n' +
        '|   2 | say "hi", bob | NULL  |\n' +
        '|   3 |               | false |\n' +
        '|  10 | multi<br>line | false |\n',
    )
    expect(formatCopy('markdown', [{ name: 'a|b', type: 'text', numeric: false }], [['x\\y']])).toBe(
      '| a\\|b |\n| ---- |\n| x\\\\y |\n',
    )
  })

  it('writes SQL INSERT statements', () => {
    expect(formatCopy('sql', cols, rows.slice(0, 3), { table: 'public.users' })).toBe(
      'INSERT INTO public.users (id, name, ok) VALUES (1, \'alice\', TRUE);\n' +
        'INSERT INTO public.users (id, name, ok) VALUES (2, \'say "hi", bob\', NULL);\n' +
        "INSERT INTO public.users (id, name, ok) VALUES (3, '', FALSE);\n",
    )
    expect(formatCopy('sql', [{ name: 'Order', type: 'text', numeric: false }], [["it's"]])).toBe(
      'INSERT INTO t ("Order") VALUES (\'it\'\'s\');\n',
    )
  })
})

describe('copy helpers', () => {
  it('quotes identifiers and literals per dialect', () => {
    expect(sqlIdent('user', 'postgres')).toBe('"user"')
    expect(sqlIdent('created_at', 'postgres')).toBe('created_at')
    expect(sqlIdent('NAME', 'oracle')).toBe('NAME')
    expect(sqlIdent('name', 'oracle')).toBe('"name"')
    const bytes: CopyColumn = { name: 'b', type: 'bytea', numeric: false, kind: 'bytes' }
    expect(sqlLiteral(bytes, '\\x0102', 'postgres')).toBe("'\\x0102'")
    expect(sqlLiteral(bytes, '\\x0102', 'oracle')).toBe("HEXTORAW('0102')")
    const bool: CopyColumn = { name: 'b', type: 'bool', numeric: false, kind: 'bool' }
    expect(sqlLiteral(bool, 'true', 'oracle')).toBe('1')
    const f: CopyColumn = { name: 'f', type: 'float8', numeric: true, kind: 'float' }
    expect(sqlLiteral(f, 'NaN', 'postgres')).toBe("'NaN'")
    expect(sqlLiteral(f, '-Infinity', 'oracle')).toBe('-BINARY_DOUBLE_INFINITY')
    const t: CopyColumn = { name: 't', type: 'timestamptz', numeric: false, kind: 'time' }
    expect(sqlLiteral(t, '2026-01-02 03:04:05', 'postgres')).toMatch(/^'2026-01-02 03:04:05([+-]\d\d:\d\d)'$/)
    expect(sqlLiteral(t, 'not a time', 'postgres')).toBe("'not a time'")
  })

  it('formats times and unique names', () => {
    expect(isoTime('2026-01-02 03:04:05.5')).toMatch(/^2026-01-02T03:04:05\.5(Z|[+-]\d\d:\d\d)$/)
    expect(isoTime('x')).toBe('x')
    expect(uniqueNames([cols[0], cols[0], cols[1], cols[0]])).toEqual(['id', 'id_2', 'name', 'id_3'])
  })

  it('guesses the table of simple selects', () => {
    expect(guessTable('SELECT * FROM public.orders')).toBe('public.orders')
    expect(guessTable('select id, name from "My Table" where id > 1;')).toBe('"My Table"')
    expect(guessTable('select * from a join b on true')).toBe('')
    expect(guessTable('update t set x = 1')).toBe('')
    expect(guessTable('SELECT * FROM orders ORDER BY id LIMIT 10')).toBe('orders')
  })
})
