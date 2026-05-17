import { describe, expect, test } from "bun:test"

import { resolveGoSidecarPath } from "./sidecar-path"

const moduleUrl = "file:///app/out/main/index.js"

describe("desktop Go sidecar", () => {
  test("uses explicit OPENCODE_GO_SIDECAR when present", () => {
    const path = resolveGoSidecarPath({
      env: { OPENCODE_GO_SIDECAR: "/opt/opencode-sidecar" },
      moduleUrl,
      resourcesPath: "/resources",
      exists: (candidate) => candidate === "/opt/opencode-sidecar",
    })

    expect(path).toBe("/opt/opencode-sidecar")
  })

  test("resolves dev sidecar from out/bin", () => {
    const path = resolveGoSidecarPath({
      env: {},
      platform: "darwin",
      moduleUrl,
      resourcesPath: "/resources",
      exists: (candidate) => candidate === "/app/out/bin/opencode-sidecar",
    })

    expect(path).toBe("/app/out/bin/opencode-sidecar")
  })

  test("resolves packaged sidecar from resources bin", () => {
    const path = resolveGoSidecarPath({
      env: {},
      platform: "win32",
      moduleUrl,
      resourcesPath: "C:\\OpenCode\\resources",
      exists: (candidate) => candidate === "C:\\OpenCode\\resources\\bin\\opencode-sidecar.exe",
    })

    expect(path).toBe("C:\\OpenCode\\resources\\bin\\opencode-sidecar.exe")
  })

  test("returns null when the Go sidecar is missing", () => {
    const path = resolveGoSidecarPath({
      env: {},
      moduleUrl,
      resourcesPath: "/resources",
      exists: () => false,
    })

    expect(path).toBe(null)
  })
})
