import { createClient, type Client } from '@connectrpc/connect'
import { ConnectionService } from '../gen/quarry/v1/connection_pb'
import { DataService } from '../gen/quarry/v1/data_pb'
import { DevService } from '../gen/quarry/v1/dev_pb'
import { ExplorerService } from '../gen/quarry/v1/explorer_pb'
import { ExportService } from '../gen/quarry/v1/export_pb'
import { QueryService } from '../gen/quarry/v1/query_pb'
import { ShellService } from '../gen/quarry/v1/shell_pb'
import { WorkspaceService } from '../gen/quarry/v1/workspace_pb'
import { transport } from './transport'

let dev: Client<typeof DevService> | undefined
let connection: Client<typeof ConnectionService> | undefined
let explorer: Client<typeof ExplorerService> | undefined
let query: Client<typeof QueryService> | undefined
let workspace: Client<typeof WorkspaceService> | undefined
let data: Client<typeof DataService> | undefined
let exporter: Client<typeof ExportService> | undefined
let shell: Client<typeof ShellService> | undefined

export function devClient(): Client<typeof DevService> {
  return (dev ??= createClient(DevService, transport))
}

export function connectionClient(): Client<typeof ConnectionService> {
  return (connection ??= createClient(ConnectionService, transport))
}

export function explorerClient(): Client<typeof ExplorerService> {
  return (explorer ??= createClient(ExplorerService, transport))
}

export function queryClient(): Client<typeof QueryService> {
  return (query ??= createClient(QueryService, transport))
}

export function workspaceClient(): Client<typeof WorkspaceService> {
  return (workspace ??= createClient(WorkspaceService, transport))
}

export function dataClient(): Client<typeof DataService> {
  return (data ??= createClient(DataService, transport))
}

export function exportClient(): Client<typeof ExportService> {
  return (exporter ??= createClient(ExportService, transport))
}

export function shellClient(): Client<typeof ShellService> {
  return (shell ??= createClient(ShellService, transport))
}
