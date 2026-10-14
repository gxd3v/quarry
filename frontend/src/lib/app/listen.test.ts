import { describe, expect, it } from 'vitest'
import { parseChannels } from './listen.svelte'

describe('listen', () => {
  it('splits channel lists', () => {
    expect(parseChannels(' jobs, events,jobs , Mixed Case ')).toEqual(['jobs', 'events', 'Mixed Case'])
    expect(parseChannels(' , ')).toEqual([])
  })
})
