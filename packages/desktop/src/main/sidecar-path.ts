import { existsSync } from "node:fs"
import * as path from "node:path"
import { fileURLToPath } from "node:url"

export type SidecarPathOptions = {
  env?: NodeJS.ProcessEnv
  platform?: NodeJS.Platform
  resourcesPath?: string
  moduleUrl?: string
  exists?: (path: string) => boolean
}

export function resolveGoSidecarPath(options: SidecarPathOptions = {}): string | null {
  const env = options.env ?? process.env
  const platform = options.platform ?? process.platform
  const exists = options.exists ?? existsSync
  const fromEnv = env.OPENCODE_GO_SIDECAR
  if (fromEnv && exists(fromEnv)) return fromEnv

  const extension = platform === "win32" ? ".exe" : ""
  const pathAPI = platform === "win32" ? path.win32 : path.posix
  const root = pathAPI.dirname(fileURLToPath(options.moduleUrl ?? import.meta.url))
  const resourcesPath = options.resourcesPath ?? process.resourcesPath
  const candidates = [
    pathAPI.join(root, `opencode-sidecar${extension}`),
    pathAPI.join(root, "..", "bin", `opencode-sidecar${extension}`),
    pathAPI.join(resourcesPath ?? root, `opencode-sidecar${extension}`),
    pathAPI.join(resourcesPath ?? root, "bin", `opencode-sidecar${extension}`),
  ]
  return candidates.find((candidate) => exists(candidate)) ?? null
}
