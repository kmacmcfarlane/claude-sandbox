// Package assets embeds the repo files the claude-sandbox binary needs at
// runtime, so init/launch work from a bare binary without the repo checkout.
package assets

import "embed"

// Scaffold is the base bootstrap seed for `init` (config.yaml, env.example,
// Dockerfile.example).
//
//go:embed scaffold
var Scaffold embed.FS

// ScaffoldRalph is the additional seed for `init-ralph` (agent/ + scripts/).
// `all:` admits `.`- and `_`-prefixed entries, but the seeding walk
// (internal/scaffold.skipEntry, CS-INITR-007) drops every dot-prefixed entry,
// __pycache__ and Python bytecode — so a future `.gitignore`/`.env.example`
// seed under scaffold-ralph/ will NOT be seeded until that filter is taught
// to allow it.
//
//go:embed all:scaffold-ralph
var ScaffoldRalph embed.FS

// ContainerContext is merged with the host's ~/.claude/CLAUDE.md and shadowed
// over it in the container.
//
//go:embed container-context.md
var ContainerContext []byte

// MCPServers is the .mcp.json fragment merged into the host file and shadowed
// in the container.
//
//go:embed mcp-servers.json
var MCPServers []byte

// PromptRalph is the base ralph prompt prepended to every loop iteration.
//
//go:embed PROMPT_RALPH.md
var PromptRalph []byte
