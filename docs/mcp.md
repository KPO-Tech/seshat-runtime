# MCP Client in Seshat

Seshat includes a production-grade MCP (Model Context Protocol) client. Any MCP server is immediately usable without additional development: plug in the server, and the agent gains access to its tools, resources, and prompts.

> Full documentation: [seshat-ai.com/docs/concepts/skills-mcp](https://seshat-ai.com/docs/concepts/skills-mcp)

---

## What is MCP?

The Model Context Protocol is an open standard that defines how AI agents connect to external data sources and tools. An MCP server exposes capabilities (tools, resources, prompts) through a standard protocol. Any compliant client — including Seshat — can connect to any compliant server.

---

## Supported transports

Seshat supports all three MCP transports:

| Transport | Use case | Config field |
|---|---|---|
| `stdio` | Local process (npx, Python, binary) | `Command` + `Args` |
| `SSE` | Remote HTTP server (Server-Sent Events) | `URL` |
| `HTTP` | Remote HTTP server (streamable HTTP) | `URL` |

`Transport` itself is optional: Seshat infers it when a server config sets `Command` (stdio) or `URL` (HTTP) without it, exactly like every example on this page. Set it explicitly only to pick `SSE` or `WebSocket` over the default HTTP for a `URL`-based server - `WebSocket` is accepted for compatibility but is served over the same HTTP transport as `SSE`/`HTTP`, not a real WebSocket connection.

---

## Configuration (SDK)

```go
client, _ := sdk.NewClient(&sdk.ClientConfig{
    MCPServers: []sdk.MCPServerConfig{
        // stdio: local npx process
        {
            Name:    "github",
            Command: "npx",
            Args:    []string{"-y", "@modelcontextprotocol/server-github"},
            Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": os.Getenv("GITHUB_TOKEN")},
        },
        // stdio: local Python process
        {
            Name:    "postgres",
            Command: "npx",
            Args:    []string{"-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"},
        },
        // SSE: remote server
        {
            Name: "my-remote-server",
            URL:  "https://my-mcp-server.example.com/sse",
        },
    },
})
```

---

## Popular MCP servers

These servers work out of the box with Seshat:

| Server | What it provides |
|---|---|
| `@modelcontextprotocol/server-github` | GitHub repos, issues, PRs, code search |
| `@modelcontextprotocol/server-postgres` | Query any PostgreSQL database |
| `@modelcontextprotocol/server-slack` | Read and write Slack messages |
| `@modelcontextprotocol/server-filesystem` | Access the local filesystem beyond the default tools |
| `@modelcontextprotocol/server-notion` | Read and write Notion pages |
| `@modelcontextprotocol/server-docker` | Manage Docker containers and images |
| `@modelcontextprotocol/server-brave-search` | Web search via Brave |
| [`docling-mcp`](https://github.com/docling-project/docling-mcp) | Explicit OCR/scan/audio conversion via a running docling-serve instance - see `scripts/docling.mcp.json.example`. `read_file` already handles DOCX/PPTX/XLSX and text-layer PDFs natively without this; reach for it when a document genuinely needs OCR or transcription. |

Any MCP-compatible server works. The full registry is at [modelcontextprotocol.io](https://modelcontextprotocol.io).

---

## How it works at runtime

When a session starts with MCP servers configured:

1. Seshat spawns each `stdio` server as a subprocess (or connects to the remote URL).
2. The server's available tools are fetched and merged with Seshat's built-in tools.
3. The agent sees all tools as a flat list and can call any of them.
4. Tool calls are routed to the right server transparently.
   A tool whose definition (name, description and input schema) is over 8,000 bytes is left out, and so are the tools of a server past 64,000 bytes in total; both are logged. Set `max_tool_spec_bytes` or `max_server_spec_bytes` on the server to change a cap (a negative value removes it).
5. When the session ends, stdio subprocesses are terminated cleanly.

---

## Related docs

- [Memory and Compaction](./memory.md) - external memory servers via MCP
- [RAG System](./rag.md) - document retrieval built into the runtime
- [Skills](./skills.md) - agent skill definitions and tool access control
- [Tools](./tools.md) - full list of Seshat's built-in tools
