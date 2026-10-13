import { createClient, type Client } from '@connectrpc/connect'
import { DevService } from '../gen/quarry/v1/dev_pb'
import { transport } from './transport'

let dev: Client<typeof DevService> | undefined

export function devClient(): Client<typeof DevService> {
  return (dev ??= createClient(DevService, transport))
}
