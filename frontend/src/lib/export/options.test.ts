import { describe, expect, it } from 'vitest'
import { DEFAULT_FORM, exportOptions, fileName, validate, withExtension, type ExportForm } from './options'

const form = (f: Partial<ExportForm>): ExportForm => ({ ...DEFAULT_FORM, path: 'C:\\out\\x.csv', ...f })

describe('exportOptions', () => {
  it('maps CSV options with explicit booleans', () => {
    expect(exportOptions(form({ delimiter: ';', header: false, nullText: 'NULL', quoteAll: true, crlf: false, bom: true }))).toEqual({
      format: 'csv',
      delimiter: ';',
      header: false,
      nullText: 'NULL',
      quoteAll: true,
      crlf: false,
      bom: true,
      table: '',
      batchRows: 0,
    })
    expect(exportOptions(form({})).delimiter).toBe('')
    expect(exportOptions(form({ delimiter: '\\t' })).delimiter).toBe('\t')
  })

  it('drops options the format does not use', () => {
    const json = exportOptions(form({ format: 'json', delimiter: ';', quoteAll: true, nullText: 'x', table: 't', header: true }))
    expect(json).toMatchObject({ format: 'json', delimiter: '', header: false, quoteAll: false, nullText: '', table: '', batchRows: 0 })
    const sql = exportOptions(form({ format: 'sql', table: ' public.t2 ', batchRows: 250.7 }))
    expect(sql).toMatchObject({ format: 'sql', table: 'public.t2', batchRows: 250, header: false })
    expect(exportOptions(form({ format: 'markdown', header: true, nullText: '∅' }))).toMatchObject({ header: true, nullText: '∅' })
  })

  it('names files and validates the form', () => {
    expect(withExtension('C:\\out\\data.csv', 'ndjson')).toBe('C:\\out\\data.ndjson')
    expect(withExtension('/tmp/data.2026', 'sql')).toBe('/tmp/data.2026.sql')
    expect(withExtension('', 'csv')).toBe('')
    expect(fileName('public.orders', 'markdown')).toBe('public.orders.md')
    expect(fileName('a/b:c', 'csv')).toBe('a_b_c.csv')
    expect(validate(form({}))).toBe('')
    expect(validate(form({ path: '' }))).toMatch(/Choose/)
    expect(validate(form({ path: 'out.csv' }))).toMatch(/absolute/)
    expect(validate(form({ path: '/home/me/x.csv', delimiter: '::' }))).toMatch(/one character/)
  })
})
