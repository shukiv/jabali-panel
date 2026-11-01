// MiniMarkdown — Codeberg #7. Render the small subset of Markdown that appears
// in our OpenAPI spec descriptions (bold, inline code, bullet lists, headings,
// paragraphs) instead of showing raw `**` / backtick markers as literal text.
//
// Deliberately NOT a full Markdown engine and NOT react-markdown (a bundle add
// for a docs page nobody loads in production, mirroring APIDocsPage's own
// reasoning). Scoped to our own controlled spec text. Builds React nodes only —
// no dangerouslySetInnerHTML — so there is no HTML-injection surface.
import type { ReactNode, JSX } from "react";
import { Typography } from "antd";

// renderInline handles **bold** and `code` within a line of text.
function renderInline(text: string): ReactNode[] {
  const parts = text.split(/(\*\*[^*]+\*\*|`[^`]+`)/g).filter((p) => p !== "");
  return parts.map((p, i) => {
    if (/^\*\*[^*]+\*\*$/.test(p)) return <strong key={i}>{p.slice(2, -2)}</strong>;
    if (/^`[^`]+`$/.test(p))
      return (
        <Typography.Text code key={i}>
          {p.slice(1, -1)}
        </Typography.Text>
      );
    return <span key={i}>{p}</span>;
  });
}

export function MiniMarkdown({ text }: { text: string }): JSX.Element {
  const blocks = text.trim().split(/\n\s*\n/); // paragraphs split on blank lines

  return (
    <>
      {blocks.map((block, bi) => {
        const lines = block.split("\n");

        // Bullet list: every line starts with - or *
        if (lines.every((l) => /^\s*[-*]\s+/.test(l))) {
          return (
            <ul key={bi} style={{ margin: "0 0 8px", paddingLeft: 20 }}>
              {lines.map((l, li) => (
                <li key={li}>{renderInline(l.replace(/^\s*[-*]\s+/, ""))}</li>
              ))}
            </ul>
          );
        }

        // Heading: a single line of #..#### text
        const h = lines.length === 1 ? block.match(/^(#{1,4})\s+(.*)$/) : null;
        if (h) {
          const level = Math.min(5, h[1].length + 1) as 2 | 3 | 4 | 5;
          return (
            <Typography.Title key={bi} level={level} style={{ marginTop: bi ? 12 : 0 }}>
              {renderInline(h[2])}
            </Typography.Title>
          );
        }

        // Paragraph: join soft-wrapped lines with spaces.
        return (
          <Typography.Paragraph key={bi} style={{ marginBottom: 8 }}>
            {renderInline(lines.join(" "))}
          </Typography.Paragraph>
        );
      })}
    </>
  );
}
