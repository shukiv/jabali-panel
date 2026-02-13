#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListResourcesRequestSchema,
  ListToolsRequestSchema,
  ReadResourceRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import * as fs from "fs";
import * as path from "path";

const PROJECT_ROOT = process.env.JABALI_ROOT || "/var/www/jabali";

// Documentation files to expose
const DOC_FILES: Record<string, { path: string; description: string }> = {
  "claude-md": {
    path: "CLAUDE.md",
    description: "Project instructions and architecture documentation",
  },
  "readme": {
    path: "README.md",
    description: "Project README and setup instructions",
  },
  "changelog": {
    path: "CHANGELOG.md",
    description: "Version history and changes",
  },
};

// Documentation sections extracted from CLAUDE.md
interface DocSection {
  title: string;
  content: string;
  level: number;
}

function parseMarkdownSections(content: string): DocSection[] {
  const sections: DocSection[] = [];
  const lines = content.split("\n");
  let currentSection: DocSection | null = null;
  let currentContent: string[] = [];

  for (const line of lines) {
    const headerMatch = line.match(/^(#{1,6})\s+(.+)$/);
    if (headerMatch) {
      if (currentSection) {
        currentSection.content = currentContent.join("\n").trim();
        sections.push(currentSection);
      }
      currentSection = {
        title: headerMatch[2],
        content: "",
        level: headerMatch[1].length,
      };
      currentContent = [];
    } else if (currentSection) {
      currentContent.push(line);
    }
  }

  if (currentSection) {
    currentSection.content = currentContent.join("\n").trim();
    sections.push(currentSection);
  }

  return sections;
}

function searchDocs(query: string): Array<{ file: string; section: string; snippet: string }> {
  const results: Array<{ file: string; section: string; snippet: string }> = [];
  const queryLower = query.toLowerCase();

  for (const [name, info] of Object.entries(DOC_FILES)) {
    const filePath = path.join(PROJECT_ROOT, info.path);
    if (!fs.existsSync(filePath)) continue;

    const content = fs.readFileSync(filePath, "utf-8");
    const sections = parseMarkdownSections(content);

    for (const section of sections) {
      if (
        section.title.toLowerCase().includes(queryLower) ||
        section.content.toLowerCase().includes(queryLower)
      ) {
        // Extract snippet around the match
        const contentLower = section.content.toLowerCase();
        const matchIndex = contentLower.indexOf(queryLower);
        let snippet = section.content;

        if (matchIndex !== -1 && section.content.length > 200) {
          const start = Math.max(0, matchIndex - 100);
          const end = Math.min(section.content.length, matchIndex + query.length + 100);
          snippet = (start > 0 ? "..." : "") + section.content.slice(start, end) + (end < section.content.length ? "..." : "");
        } else if (section.content.length > 200) {
          snippet = section.content.slice(0, 200) + "...";
        }

        results.push({
          file: name,
          section: section.title,
          snippet,
        });
      }
    }
  }

  return results;
}

function listSections(): Array<{ file: string; title: string; level: number }> {
  const allSections: Array<{ file: string; title: string; level: number }> = [];

  for (const [name, info] of Object.entries(DOC_FILES)) {
    const filePath = path.join(PROJECT_ROOT, info.path);
    if (!fs.existsSync(filePath)) continue;

    const content = fs.readFileSync(filePath, "utf-8");
    const sections = parseMarkdownSections(content);

    for (const section of sections) {
      allSections.push({
        file: name,
        title: section.title,
        level: section.level,
      });
    }
  }

  return allSections;
}

function getSection(file: string, sectionTitle: string): string | null {
  const info = DOC_FILES[file];
  if (!info) return null;

  const filePath = path.join(PROJECT_ROOT, info.path);
  if (!fs.existsSync(filePath)) return null;

  const content = fs.readFileSync(filePath, "utf-8");
  const sections = parseMarkdownSections(content);

  const section = sections.find(
    (s) => s.title.toLowerCase() === sectionTitle.toLowerCase()
  );

  return section ? `# ${section.title}\n\n${section.content}` : null;
}

// Create server
const server = new Server(
  {
    name: "jabali-docs",
    version: "1.0.0",
  },
  {
    capabilities: {
      resources: {},
      tools: {},
    },
  }
);

// List available resources
server.setRequestHandler(ListResourcesRequestSchema, async () => {
  const resources = [];

  for (const [name, info] of Object.entries(DOC_FILES)) {
    const filePath = path.join(PROJECT_ROOT, info.path);
    if (fs.existsSync(filePath)) {
      resources.push({
        uri: `jabali://docs/${name}`,
        name: info.path,
        description: info.description,
        mimeType: "text/markdown",
      });
    }
  }

  return { resources };
});

// Read resource content
server.setRequestHandler(ReadResourceRequestSchema, async (request) => {
  const uri = request.params.uri;
  const match = uri.match(/^jabali:\/\/docs\/(.+)$/);

  if (!match) {
    throw new Error(`Unknown resource: ${uri}`);
  }

  const docName = match[1];
  const info = DOC_FILES[docName];

  if (!info) {
    throw new Error(`Unknown document: ${docName}`);
  }

  const filePath = path.join(PROJECT_ROOT, info.path);

  if (!fs.existsSync(filePath)) {
    throw new Error(`File not found: ${info.path}`);
  }

  const content = fs.readFileSync(filePath, "utf-8");

  return {
    contents: [
      {
        uri,
        mimeType: "text/markdown",
        text: content,
      },
    ],
  };
});

// List available tools
server.setRequestHandler(ListToolsRequestSchema, async () => {
  return {
    tools: [
      {
        name: "search_docs",
        description: "Search through Jabali documentation for a query string",
        inputSchema: {
          type: "object" as const,
          properties: {
            query: {
              type: "string",
              description: "The search query",
            },
          },
          required: ["query"],
        },
      },
      {
        name: "list_sections",
        description: "List all sections in the documentation",
        inputSchema: {
          type: "object" as const,
          properties: {},
        },
      },
      {
        name: "get_section",
        description: "Get a specific section from the documentation",
        inputSchema: {
          type: "object" as const,
          properties: {
            file: {
              type: "string",
              description: "The documentation file (claude-md, readme, changelog)",
            },
            section: {
              type: "string",
              description: "The section title to retrieve",
            },
          },
          required: ["file", "section"],
        },
      },
    ],
  };
});

// Handle tool calls
server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;

  switch (name) {
    case "search_docs": {
      const query = (args as { query: string }).query;
      const results = searchDocs(query);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify(results, null, 2),
          },
        ],
      };
    }

    case "list_sections": {
      const sections = listSections();
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify(sections, null, 2),
          },
        ],
      };
    }

    case "get_section": {
      const { file, section } = args as { file: string; section: string };
      const content = getSection(file, section);
      if (!content) {
        return {
          content: [
            {
              type: "text" as const,
              text: `Section "${section}" not found in ${file}`,
            },
          ],
          isError: true,
        };
      }
      return {
        content: [
          {
            type: "text" as const,
            text: content,
          },
        ],
      };
    }

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
});

// Start server
async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Jabali Docs MCP server running on stdio");
}

main().catch(console.error);
