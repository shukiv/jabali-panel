// logStream.ts — the pure, audience-neutral pieces of the domain log-stream
// viewer (JAB-296). Extracted out of the LogStreamModal component so the
// WebSocket newline handling, the ring-buffer cap, and the GoAccess URL
// derivation can be unit-tested on their own — they used to be untestable
// module-locals inside a .tsx.

// The stream is "recent lines", not an archive: without a ceiling every frame
// appended to React state and each render re-joined the WHOLE array, so cost
// grew quadratically over the stream's lifetime. On a moderately busy domain
// (~10 lines/s) a 15-minute stream reaches ~9k lines and the tab janks within
// minutes while memory keeps climbing. Keep the newest MAX_LOG_LINES, which is
// far more than fits on screen and matches what the feature advertises.
export const MAX_LOG_LINES = 2000;

// capLogLines keeps only the newest MAX_LOG_LINES entries.
export function capLogLines(lines: string[]): string[] {
  return lines.length > MAX_LOG_LINES ? lines.slice(-MAX_LOG_LINES) : lines;
}

// stripTrailingNewline trims a single trailing CR/LF (or both) — accommodates
// servers that send "line\n", "line\r\n", or "line" interchangeably. Keeps
// inner newlines untouched in case a single frame carries a multi-line block.
export const stripTrailingNewline = (s: string): string => {
  if (typeof s !== "string") return s;
  if (s.endsWith("\r\n")) return s.slice(0, -2);
  if (s.endsWith("\n") || s.endsWith("\r")) return s.slice(0, -1);
  return s;
};

// buildGoAccessHttpUrl converts the WebSocket stream URL
// (ws[s]://host/api/v1/logs/stream/<key>) into the HTTP GoAccess render URL.
// The HTTP route serves the GoAccess HTML snapshot with its own relaxed CSP
// (script-src 'self' 'unsafe-inline' 'unsafe-eval'), which the previous
// srcdoc-via-WS path could not — srcdoc inherits the panel's strict parent CSP
// and meta CSP can only tighten.
//
// Returns null if streamUrl can't be parsed into the expected shape (caller
// then shows a "no stream" placeholder instead of crashing).
export const buildGoAccessHttpUrl = (streamUrl: string): string | null => {
  try {
    const u = new URL(streamUrl);
    if (u.protocol === "ws:") u.protocol = "http:";
    else if (u.protocol === "wss:") u.protocol = "https:";
    // Append /goaccess.html if not already present (some callers may pre-build).
    if (!u.pathname.endsWith("/goaccess.html")) {
      u.pathname = u.pathname.replace(/\/+$/, "") + "/goaccess.html";
    }
    return u.toString();
  } catch {
    return null;
  }
};
