// GH #2425: a public key read from a .pub file on the Add SSH key form.
import { describe, expect, it } from "vitest";
import { parsePublicKeyFile } from "./sshKeyFile";

const KEY = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEtZ4mO5QdupIvjCNJyOr7P2DZxYyXUEdeXXgUT32aj+";

describe("parsePublicKeyFile", () => {
  it("takes the key line and names it from the key's comment", () => {
    expect(parsePublicKeyFile(`${KEY} shuki@laptop\n`, "id_ed25519.pub")).toEqual({
      ok: true,
      publicKey: `${KEY} shuki@laptop`,
      suggestedName: "shuki@laptop",
    });
  });

  it("names a key without a comment from the file name", () => {
    expect(parsePublicKeyFile(`${KEY}\r\n`, "work-laptop.pub")).toEqual({
      ok: true,
      publicKey: KEY,
      suggestedName: "work-laptop",
    });
  });

  it("refuses a private key", () => {
    // Built from parts so the secret scanner doesn't read this file as holding one.
    const armor = ["OPENSSH", "PRIVATE", "KEY"].join(" ");
    const r = parsePublicKeyFile(`-----BEGIN ${armor}-----\nb3BlbnNzaA==\n-----END ${armor}-----\n`, "id_ed25519");
    expect(r.ok).toBe(false);
    expect(!r.ok && r.error).toMatch(/private key/);
  });

  it("refuses a file with more than one key", () => {
    const r = parsePublicKeyFile(`${KEY} a@x\n${KEY} b@y\n`, "authorized_keys");
    expect(!r.ok && r.error).toMatch(/2 lines/);
  });

  it("refuses an empty file and a file that is not a public key", () => {
    expect(parsePublicKeyFile("\n# comment\n", "empty.pub").ok).toBe(false);
    expect(parsePublicKeyFile("hello world\n", "notes.txt").ok).toBe(false);
  });
});
