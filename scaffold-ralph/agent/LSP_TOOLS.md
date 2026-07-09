# LSP & Semantic Code Tools

Semantic code intelligence should be preferred over grep/read for any
operation involving symbol relationships, cross-file impact, or refactoring.

## 1) Built-in LSP tool

Claude Code's native `LSP` tool talks to language servers on PATH. It
provides navigation-oriented operations:

| Operation              | What it does                                      |
|------------------------|---------------------------------------------------|
| `goToDefinition`       | Jump to a symbol's declaration                    |
| `findReferences`       | All usages of a symbol across the codebase        |
| `goToImplementation`   | Find all implementations of an interface          |
| `hover`                | Type info and documentation for a symbol          |
| `documentSymbol`       | All symbols in a file                             |
| `workspaceSymbol`      | Search symbols across the workspace               |
| `prepareCallHierarchy` | Get call hierarchy item at a position             |
| `incomingCalls`        | All functions/methods that call a given function  |
| `outgoingCalls`        | All functions/methods called by a given function  |

**Supported languages:** any language whose server is installed on PATH and
registered with Claude Code. In the sandbox, install servers via the child
Dockerfile and run `setup-lsp-plugins` to register them. If `LSP` returns
"No LSP server available for file type", the server for that language isn't
installed — fall back to grep/read.

## 2) Language-specific semantic MCP servers (optional)

Some language toolchains ship a richer MCP server that exposes higher-level
operations beyond the built-in LSP tool (workspace layout, package APIs,
fuzzy symbol search, compiler diagnostics, dependency/vuln checks). For
example, Go's `gopls mcp` exposes `mcp__gopls__*` tools. When such a server
is available for your project's language, prefer it for the operations it
covers. These are configured as MCP servers in `.mcp.json`.

## 3) When to use which

| Task                          | Preferred tool                       |
|-------------------------------|--------------------------------------|
| Navigate to a definition      | `LSP(goToDefinition)`                |
| Find all usages of a symbol   | `LSP(findReferences)`                |
| Find interface implementors   | `LSP(goToImplementation)`            |
| Understand call flow          | `LSP(incomingCalls)` / `LSP(outgoingCalls)` |
| Search for a symbol by name   | `LSP(workspaceSymbol)`               |

If a language-specific MCP server (§2) offers an equivalent or higher-level
operation (e.g. package-level diagnostics or API summaries), prefer it.

## 4) Mandatory usage

1. **Before modifying an interface or widely-used symbol** — run
   `LSP(findReferences)` on every method/symbol being changed to know all
   call/implementation sites before editing.

2. **After editing code** — run diagnostics (via a language-specific MCP
   server if available) on the edited files to catch breaks before the full
   `make test` run.

3. **Exploring unfamiliar code** — use `LSP(workspaceSymbol)` to find a
   symbol, then `LSP(findReferences)` to understand its usage, rather than
   reading files top-down.

4. **Adding a method to an interface** — use `LSP(goToImplementation)` on the
   interface type to find all implementors and mock files that need updating.

## 5) Limitations

- LSP tools require the code to be parseable/compilable. If generated code is
  stale (after a codegen step), regenerate it before relying on LSP results.
- Generated code and vendored dependencies should never be hand-edited.
- Some language servers require plugin/loader support and may not be available
  in every environment — fall back to grep/read when a server is missing.
