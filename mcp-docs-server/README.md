# Jabali Docs MCP Server

An MCP (Model Context Protocol) server that exposes Jabali project documentation.

## Features

- **Resources**: Exposes CLAUDE.md, README.md, and CHANGELOG.md as readable resources
- **Tools**:
  - `search_docs` - Search through documentation
  - `list_sections` - List all documentation sections
  - `get_section` - Get a specific section by title

## Installation

```bash
cd /var/www/jabali/mcp-docs-server
npm install
npm run build
```

## Usage with Claude Code

Add to your Claude Code MCP settings (`~/.claude/settings.json`):

```json
{
  "mcpServers": {
    "jabali-docs": {
      "command": "node",
      "args": ["/var/www/jabali/mcp-docs-server/dist/index.js"],
      "env": {
        "JABALI_ROOT": "/var/www/jabali"
      }
    }
  }
}
```

## Available Resources

| URI | Description |
|-----|-------------|
| `jabali://docs/claude-md` | Project instructions (CLAUDE.md) |
| `jabali://docs/readme` | README documentation |
| `jabali://docs/changelog` | Version history |

## Tools

### search_docs

Search through all documentation files.

```json
{
  "query": "backup"
}
```

### list_sections

List all section headings across documentation files.

### get_section

Get content of a specific section.

```json
{
  "file": "claude-md",
  "section": "Backup System"
}
```
