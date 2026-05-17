import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process"
import { DEFAULT_SERVER_URL_KEY, WSL_ENABLED_KEY } from "./constants"
import { getUserShell, loadShellEnv } from "./shell-env"
import { resolveGoSidecarPath } from "./sidecar-path"
import { getStore } from "./store"
import type { SqliteMigrationProgress } from "../preload/types"

export type WslConfig = { enabled: boolean }

export type HealthCheck = { wait: Promise<void> }

export type SidecarListener = { stop: () => Promise<void> }

const SIDECAR_START_STALL_TIMEOUT = 60_000
const SIDECAR_STOP_TIMEOUT = 6_000

type SpawnLocalServerOptions = {
  needsMigration: boolean
  userDataPath: string
  onSqliteProgress?: (progress: SqliteMigrationProgress) => void
  onStdout?: (message: string) => void
  onStderr?: (message: string) => void
  onExit?: (code: number) => void
}

export function getDefaultServerUrl(): string | null {
  const value = getStore().get(DEFAULT_SERVER_URL_KEY)
  return typeof value === "string" ? value : null
}

export function setDefaultServerUrl(url: string | null) {
  if (url) {
    getStore().set(DEFAULT_SERVER_URL_KEY, url)
    return
  }

  getStore().delete(DEFAULT_SERVER_URL_KEY)
}

export function getWslConfig(): WslConfig {
  const value = getStore().get(WSL_ENABLED_KEY)
  return { enabled: typeof value === "boolean" ? value : false }
}

export function setWslConfig(config: WslConfig) {
  getStore().set(WSL_ENABLED_KEY, config.enabled)
}

export function preferAppEnv(userDataPath: string) {
  const shell = process.platform === "win32" ? null : getUserShell()
  Object.assign(process.env, {
    ...(shell ? loadShellEnv(shell) : null),
    OPENCODE_EXPERIMENTAL_ICON_DISCOVERY: "true",
    OPENCODE_EXPERIMENTAL_FILEWATCHER: "true",
    OPENCODE_CLIENT: "desktop",
    XDG_STATE_HOME: process.env.XDG_STATE_HOME ?? userDataPath,
  })
}

export async function spawnLocalServer(
  hostname: string,
  port: number,
  password: string,
  options: SpawnLocalServerOptions,
) {
  const goSidecar = resolveGoSidecarPath()
  if (!goSidecar) {
    throw new Error("Go sidecar binary not found. Run `bun --cwd packages/desktop predev` or `prebuild` first.")
  }
  return spawnGoLocalServer(goSidecar, hostname, port, password, options)
}

async function spawnGoLocalServer(
  sidecar: string,
  hostname: string,
  port: number,
  password: string,
  options: SpawnLocalServerOptions,
) {
  const env = createSidecarEnv()
  Object.assign(env, {
    OPENCODE_SERVER_USERNAME: "opencode",
    OPENCODE_SERVER_PASSWORD: password,
    XDG_STATE_HOME: env.XDG_STATE_HOME ?? options.userDataPath,
  })
  const child = spawn(sidecar, ["--hostname", hostname, "--port", String(port)], {
    cwd: process.cwd(),
    env,
    stdio: "pipe",
  })
  return await waitForGoSidecar(child, sidecar, hostname, port, password, options)
}

async function waitForGoSidecar(
  child: ChildProcessWithoutNullStreams,
  sidecar: string,
  hostname: string,
  port: number,
  password: string,
  options: SpawnLocalServerOptions,
) {
  let exited = false
  const exit = defer<number>()
  child.once("exit", (code) => {
    exited = true
    options.onExit?.(code ?? 0)
    exit.resolve(code ?? 0)
  })
  child.once("error", (error) => options.onStderr?.(`go sidecar error: ${serializeError(error).message}`))
  child.stdout.on("data", (chunk: Buffer) => options.onStdout?.(chunk.toString("utf8").trimEnd()))
  child.stderr.on("data", (chunk: Buffer) => options.onStderr?.(chunk.toString("utf8").trimEnd()))

  const url = `http://${hostname}:${port}`
  try {
    await Promise.race([
      waitForHealth(url, password),
      delay(SIDECAR_START_STALL_TIMEOUT).then(() => {
        throw new Error(`Go sidecar did not become healthy within ${SIDECAR_START_STALL_TIMEOUT}ms: ${sidecar}`)
      }),
      exit.promise.then((code) => {
        throw new Error(`Go sidecar exited before healthy with code ${code}`)
      }),
    ])
  } catch (error) {
    if (!exited) child.kill()
    throw error
  }

  let stopping: Promise<void> | undefined
  return {
    listener: {
      stop: () => {
        if (stopping) return stopping
        if (exited) return Promise.resolve()
        child.kill("SIGTERM")
        stopping = Promise.race([
          exit.promise.then(() => undefined),
          delay(SIDECAR_STOP_TIMEOUT).then(() => {
            if (!exited) child.kill("SIGKILL")
          }),
        ])
        return stopping
      },
    },
    health: { wait: Promise.resolve() },
  }
}

async function waitForHealth(url: string, password: string) {
  while (true) {
    if (await checkHealth(url, password)) return
    await delay(100)
  }
}

export async function checkHealth(url: string, password?: string | null): Promise<boolean> {
  let healthUrl: URL
  try {
    healthUrl = new URL("/global/health", url)
  } catch {
    return false
  }

  const headers = new Headers()
  if (password) {
    const auth = Buffer.from(`opencode:${password}`).toString("base64")
    headers.set("authorization", `Basic ${auth}`)
  }

  try {
    const res = await fetch(healthUrl, {
      method: "GET",
      headers,
      signal: AbortSignal.timeout(3000),
    })
    return res.ok
  } catch {
    return false
  }
}

function createSidecarEnv(): Record<string, string> {
  const env = Object.fromEntries(
    Object.entries(process.env).flatMap(([key, value]) => (value === undefined ? [] : [[key, String(value)]])),
  )
  delete env.DEBUG
  if (process.platform === "linux") delete env.LD_PRELOAD
  return env
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms))
}

function serializeError(error: unknown) {
  if (error instanceof Error) return { message: error.message, stack: error.stack }
  return { message: String(error) }
}

function defer<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
