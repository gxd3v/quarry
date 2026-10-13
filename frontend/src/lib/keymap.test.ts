import { describe, expect, it } from 'vitest'
import { chordOf, Keymap, label, type KeyLike } from './keymap'

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
