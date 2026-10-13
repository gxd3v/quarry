import type { Interceptor, Transport } from '@connectrpc/connect'
import { createConnectTransport } from '@connectrpc/connect-web'
import { serverInfo } from './server'

let inner: Promise<Transport> | undefined

function resolveTransport(): Promise<Transport> {
  if (!inner) {
    inner = serverInfo().then(({ url, token }) => {
      const auth: Interceptor = (next) => (req) => {
        req.header.set('Authorization', `Bearer ${token}`)
        return next(req)
      }
      return createConnectTransport({ baseUrl: url, useBinaryFormat: true, interceptors: [auth] })
    })
    inner.catch(() => (inner = undefined))
  }
  return inner
}

// transport defers resolving the server address to the first call, so clients
// can be created synchronously at module load.
export const transport: Transport = {
  async unary(method, signal, timeoutMs, header, input, contextValues) {
    return (await resolveTransport()).unary(method, signal, timeoutMs, header, input, contextValues)
  },
  async stream(method, signal, timeoutMs, header, input, contextValues) {
    return (await resolveTransport()).stream(method, signal, timeoutMs, header, input, contextValues)
  },
}
