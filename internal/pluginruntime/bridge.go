package pluginruntime

const bridgeScript = `
const originalConsoleError = console.error.bind(console)
for (const name of ["log", "info", "warn", "debug"]) {
  console[name] = (...args) => originalConsoleError(...args)
}

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value)
}

function getServerPlugin(value) {
  if (typeof value === "function") return value
  if (!isRecord(value)) return undefined
  if (typeof value.server === "function") return value.server
  return undefined
}

function getLegacyPlugins(mod) {
  const seen = new Set()
  const result = []
  for (const value of Object.values(mod)) {
    if (seen.has(value)) continue
    seen.add(value)
    const server = getServerPlugin(value)
    if (!server) throw new TypeError("Plugin export is not a function")
    result.push(server)
  }
  return result
}

function getV1Server(mod) {
  const value = mod.default
  if (!isRecord(value)) return undefined
  if (!("id" in value) && !("server" in value) && !("tui" in value)) return undefined
  if (value.server === undefined) return undefined
  if (typeof value.server !== "function") throw new TypeError("Plugin has invalid server export")
  return value.server
}

function pluginInput(req) {
  return {
    client: {},
    project: req.project ?? { id: "", name: "", directory: req.directory },
    directory: req.directory,
    worktree: req.worktree,
    experimental_workspace: {
      register() {},
    },
    serverUrl: new URL(req.serverUrl || "http://localhost:4096"),
    $: Bun.$,
  }
}

async function serverHooks(plugin, req) {
  const mod = await import(plugin.entry)
  const input = pluginInput(req)
  const hooks = []
  const v1 = getV1Server(mod)
  if (v1) {
    hooks.push(await v1(input, plugin.options || undefined))
  } else {
    for (const server of getLegacyPlugins(mod)) {
      hooks.push(await server(input, plugin.options || undefined))
    }
  }
  return hooks.filter(Boolean)
}

function publicAuthMethods(auth) {
  if (!auth || !auth.provider || !Array.isArray(auth.methods)) return undefined
  return {
    provider: auth.provider,
    methods: auth.methods.map((method) => ({
      type: method.type,
      label: method.label,
      ...(Array.isArray(method.prompts)
        ? {
            prompts: method.prompts.map((prompt) => {
              if (prompt.type === "select") {
                return {
                  type: "select",
                  key: prompt.key,
                  message: prompt.message,
                  options: prompt.options ?? [],
                  ...(prompt.when ? { when: prompt.when } : {}),
                }
              }
              return {
                type: "text",
                key: prompt.key,
                message: prompt.message,
                ...(prompt.placeholder ? { placeholder: prompt.placeholder } : {}),
                ...(prompt.when ? { when: prompt.when } : {}),
              }
            }),
          }
        : {}),
    })),
  }
}

async function main() {
  const raw = await new Response(Bun.stdin.stream()).text()
  const req = JSON.parse(raw || "{}")
  let output = req.output ?? {}
  for (const plugin of req.plugins ?? []) {
    let loaded
    try {
      loaded = await serverHooks(plugin, req)
    } catch (err) {
      console.error("Skipping plugin " + plugin.spec + ": " + (err && err.stack ? err.stack : String(err)))
      continue
    }
    for (const hooks of loaded) {
      try {
        const configTarget = req.hook === "config.apply" && output.config ? output.config : req.config
        if (configTarget && typeof hooks.config === "function") {
          await hooks.config(configTarget)
        }
        if (req.hook === "config.apply") {
          continue
        }
        if (req.hook === "auth.methods") {
          const auth = publicAuthMethods(hooks.auth)
          if (auth) {
            output.methods ??= {}
            output.methods[auth.provider] = auth.methods
          }
          continue
        }
        if (req.hook === "auth.authorize") {
          const auth = hooks.auth
          if (!auth || auth.provider !== req.input.providerID) continue
          const method = auth.methods?.[req.input.method]
          if (!method) continue
          if (method.type === "oauth") {
            const result = await method.authorize(req.input.inputs)
            output.authorization = result
              ? { url: result.url, method: result.method, instructions: result.instructions }
              : null
            continue
          }
          if (method.type === "api") {
            output.authorization = method.authorize ? await method.authorize(req.input.inputs) : null
            continue
          }
        }
        const fn = hooks[req.hook]
        if (typeof fn !== "function") continue
        await fn(req.input ?? {}, output)
      } catch (err) {
        console.error("Plugin " + plugin.spec + " hook " + req.hook + " failed: " + (err && err.stack ? err.stack : String(err)))
      }
    }
  }
  process.stdout.write(JSON.stringify(output))
}

main().catch((err) => {
  console.error(err && err.stack ? err.stack : String(err))
  process.exit(1)
})
`
