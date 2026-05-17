import { $ } from "bun"

import { buildGoSidecar } from "./utils"

await $`bun ./scripts/copy-icons.ts ${process.env.OPENCODE_CHANNEL ?? "dev"}`
await buildGoSidecar()
