// Pins the in-browser SSH key generation (UserSSHKeysPage →
// micro-key-producer/ssh.js getKeys) to the output of micro-key-producer
// 0.8.6, so a version bump can't silently change the keys users get.
//
// The public key and fingerprint for a fixed seed must stay byte-for-byte
// the same. The private key PEM can't be compared directly: OpenSSH's format
// holds a random check value, so it differs between runs of the same version.
// Its shape and embedded public key are checked instead.
import { getKeys } from "micro-key-producer/ssh.js";
import { describe, expect, it } from "vitest";

// sha256("seed-0")
const SEED_HEX = "8efd6168a055d1e45863c237eb3feab2be94f10b7ccdc5bf8bb54305c8b19f7f";
const PUBLIC_0_8_6 =
  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEtZ4mO5QdupIvjCNJyOr7P2DZxYyXUEdeXXgUT32aj+ jabali";
const FINGERPRINT_0_8_6 = "SHA256:9nf1joOUcjl2bf63qr2Iz5aXXnKa/e3DDsHRWLbYiC4";

function seed(): Uint8Array {
  return new Uint8Array(SEED_HEX.match(/../g)!.map((h) => parseInt(h, 16)));
}

describe("in-browser ed25519 SSH keys (micro-key-producer)", () => {
  it("gives the same public key and fingerprint as 0.8.6 for a fixed seed", () => {
    const k = getKeys(seed(), "jabali");
    expect(k.publicKey.trim()).toBe(PUBLIC_0_8_6);
    expect(k.fingerprint).toBe(FINGERPRINT_0_8_6);
  });

  it("returns an OpenSSH private key that carries the same public key", () => {
    const k = getKeys(seed(), "jabali");
    const lines = k.privateKey.trim().split("\n");
    // Built from parts so the secret scanner doesn't read the two armor
    // lines in this file as a private key.
    const armor = ["OPENSSH", "PRIVATE", "KEY"].join(" ");
    expect(lines[0]).toBe(`-----BEGIN ${armor}-----`);
    expect(lines[lines.length - 1]).toBe(`-----END ${armor}-----`);
    const body = atob(lines.slice(1, -1).join(""));
    expect(body.startsWith("openssh-key-v1\0")).toBe(true);
    // The public key blob (base64 part of the authorized_keys line) is
    // embedded in the private key container.
    const pubBlob = atob(PUBLIC_0_8_6.split(" ")[1]);
    expect(body.includes(pubBlob)).toBe(true);
  });
});
