// sshKeyFile.ts — read an SSH public key from a file the user picks on the
// Add SSH key form (GH #2425). The file is read in the browser; only the key
// line goes into the form, and a private key is refused before anything is
// put there.

export const SSH_KEY_PREFIXES = ["ssh-rsa ", "ssh-ed25519 ", "ecdsa-sha2-"];

// A public key file is one line well under 1 KB (a 16384-bit RSA key is about
// 2.8 KB); anything bigger is not one.
export const MAX_KEY_FILE_BYTES = 16 * 1024;

export type KeyFileResult =
  | { ok: true; publicKey: string; suggestedName: string }
  | { ok: false; error: string };

export function parsePublicKeyFile(text: string, fileName: string): KeyFileResult {
  if (text.includes("PRIVATE KEY")) {
    return {
      ok: false,
      error: "That file is a private key. Upload the matching .pub file; the private key stays on your computer.",
    };
  }
  const lines = text
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"));
  if (lines.length === 0) {
    return { ok: false, error: "That file is empty." };
  }
  if (lines.length > 1) {
    return { ok: false, error: `That file holds ${lines.length} lines. Upload one public key at a time.` };
  }
  const key = lines[0];
  if (!SSH_KEY_PREFIXES.some((p) => key.startsWith(p))) {
    return {
      ok: false,
      error: "That file is not an SSH public key: it must start with ssh-rsa, ssh-ed25519, or ecdsa-sha2-.",
    };
  }
  const comment = key.split(/\s+/).slice(2).join(" ");
  const base = fileName.replace(/\.pub$/i, "");
  return { ok: true, publicKey: key, suggestedName: (comment || base).slice(0, 128) };
}
