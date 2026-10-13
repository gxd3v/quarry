import { describe, expect, it } from 'vitest'
import { chordOf, cleanOverrides, effectiveBindings, Keymap, label, normalizeChord, type KeyLike } from './keymap'

const key = (key: string, mods: Partial<KeyLike> = {}): KeyLike => ({
  key,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  metaKey: false,
  ...mods,
})

describe('chordOf', () => {
  it('normalizes keys and modifiers', () => {
    expect(chordOf(key('t', { ctrlKey: true }), false)).toBe('Mod+T')
    expect(chordOf(key('T', { ctrlKey: true, shiftKey: true, code: 'KeyT' }), false)).toBe('Mod+Shift+T')
    expect(chordOf(key('Enter', { ctrlKey: true, shiftKey: true }), false)).toBe('Mod+Shift+Enter')
    expect(chordOf(key('¡', { altKey: true, code: 'Digit1' }), false)).toBe('Alt+1')
    expect(chordOf(key('Esc'), false)).toBe('Escape')
    expect(chordOf(key('Control', { ctrlKey: true }), false)).toBe('')
  })

  it('uses Cmd as Mod on macOS', () => {
    expect(chordOf(key('Enter', { metaKey: true }), true)).toBe('Mod+Enter')
    expect(chordOf(key('Enter', { ctrlKey: true }), true)).toBe('Ctrl+Enter')
    expect(label('Mod+W', true)).toBe('Cmd+W')
    expect(label('Mod+W', false)).toBe('Ctrl+W')
  })
})

describe('Keymap', () => {
  it('dispatches bound actions and lets inapplicable ones through', () => {
    const km = new Keymap({ 'Mod+T': 'tab.new', Escape: 'run.cancel' })
    const calls: string[] = []
    let running = false
    const off = km.register(
      { id: 'tab.new', title: 'New tab', run: () => void calls.push('new') },
      { id: 'run.cancel', title: 'Cancel', run: () => running && calls.push('cancel') > 0 },
    )
    expect(km.handle(key('t', { ctrlKey: true }))).toBe(true)
    expect(km.handle(key('Escape'))).toBe(false)
    running = true
    expect(km.handle(key('Escape'))).toBe(true)
    expect(km.handle(key('x'))).toBe(false)
    expect(calls).toEqual(['new', 'cancel'])
    expect(km.keysFor('tab.new')).toEqual(['Mod+T'])
    off()
    expect(km.handle(key('t', { ctrlKey: true }))).toBe(false)
  })
})

describe('overrides', () => {
  const defaults = { 'Mod+T': 'tab.new', 'Mod+W': 'tab.close', 'Mod+PageDown': 'tab.next', 'Mod+Tab': 'tab.next' }

  it('replace an action default chords and steal taken chords', () => {
    const b = effectiveBindings(defaults, { 'tab.next': 'Mod+T' })
    expect(Object.fromEntries(b)).toEqual({ 'Mod+W': 'tab.close', 'Mod+T': 'tab.next' })
    expect(Object.fromEntries(effectiveBindings(defaults, { 'tab.close': '' }))).toEqual({
      'Mod+T': 'tab.new',
      'Mod+PageDown': 'tab.next',
      'Mod+Tab': 'tab.next',
    })
  })

  it('apply to a keymap and report conflicts', () => {
    const km = new Keymap(defaults)
    let changes = 0
    km.onChange(() => changes++)
    km.setOverrides({ 'tab.new': 'Mod+Shift+K' })
    expect(changes).toBe(1)
    expect(km.keysFor('tab.new')).toEqual(['Mod+Shift+K'])
    expect(km.defaultsFor('tab.new')).toEqual(['Mod+T'])
    expect(km.boundTo('Mod+T')).toBeUndefined()
    expect(km.boundTo('Mod+W')).toBe('tab.close')
    expect(km.keysFor('tab.next')).toEqual(['Mod+PageDown', 'Mod+Tab'])
    km.setOverrides({})
    expect(km.keysFor('tab.new')).toEqual(['Mod+T'])
  })

  it('parse hand-written chords', () => {
    expect(normalizeChord('ctrl+shift+k')).toBe('Mod+Shift+K')
    expect(normalizeChord('Shift + Mod + enter')).toBe('Mod+Shift+Enter')
    expect(normalizeChord('alt+1')).toBe('Alt+1')
    expect(normalizeChord('esc')).toBe('Escape')
    expect(normalizeChord('ctrl+')).toBe('')
    expect(normalizeChord('a+b')).toBe('')
  })

  it('clean settings', () => {
    expect(cleanOverrides(null)).toEqual({})
    expect(cleanOverrides([1])).toEqual({})
    expect(
      cleanOverrides({ 'tab.new': 'ctrl+shift+k', 'tab.close': '', 'tab.next': 5, bad: 'x+y', same: 'Mod+W' }, { 'Mod+W': 'same' }),
    ).toEqual({ 'tab.new': 'Mod+Shift+K', 'tab.close': '' })
  })
})
