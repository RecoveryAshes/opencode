#!/usr/bin/env bun
import { $ } from "bun"

import { buildGoSidecar, resolveChannel } from "./utils"

const channel = resolveChannel()
await $`bun ./scripts/copy-icons.ts ${channel}`
await $`bun ./scripts/copy-metainfo.ts ${channel}`
await buildGoSidecar()
