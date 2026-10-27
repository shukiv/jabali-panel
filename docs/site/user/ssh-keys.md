# SSH Keys

`/jabali-panel/ssh-keys`. Manage the SSH public keys used for SFTP access to your account.

## Why SSH keys

SFTP is the file-transfer protocol the panel exposes (port 22). Password authentication is disabled by default for panel users — only key-based authentication. SSH keys are individually attributable (one key per device or per user), revocable without affecting other keys, and not vulnerable to credential-stuffing.

## Adding a key

1. Generate a key pair on your local machine if you do not have one:

   ```bash
   ssh-keygen -t ed25519 -C "alice@laptop"
   ```

   This creates `~/.ssh/id_ed25519` (private; do not share) and `~/.ssh/id_ed25519.pub` (public; safe to share).

2. Open `~/.ssh/id_ed25519.pub` in a text editor and copy the full content. It starts with `ssh-ed25519 ...` and ends with a label such as `alice@laptop`.

3. In the panel: click **Add SSH key**, paste the public key, name it ("Laptop", "Work desktop", "CI"). Save.

The agent appends the key to `~/.ssh/authorized_keys` with the correct permissions. The change is effective for the next SFTP connection.

## Revoking a key

Click **Revoke** on the row. The agent removes the line from `~/.ssh/authorized_keys`. Any active SFTP sessions using the revoked key remain connected until they disconnect; new connections using the same key are rejected.

## Connecting via SFTP

```bash
sftp -i ~/.ssh/id_ed25519 <your-username>@<panel-hostname>
```

Or with a GUI client: FileZilla, Cyberduck, WinSCP — supply the host (panel hostname), port 22, your panel username, and point at the private key file.

## SFTP restrictions

- You are chrooted to your home directory.
- Interactive shell is disabled (`ForceCommand internal-sftp`). You cannot run shell commands over SSH.
- Port forwarding is disabled (`AllowTcpForwarding no`, `X11Forwarding no`).

## Key types accepted

- Ed25519 (recommended; fastest, shortest, modern).
- RSA — minimum 2048 bits; 4096 bits preferred.
- ECDSA — accepted but not recommended (NIST-curve concerns); prefer Ed25519.

DSA keys are rejected.

## What about password authentication

Disabled by default. The administrator may enable password authentication for your account on request, but key auth is the supported path. Even with password auth enabled, the password is your panel password — not a separate SFTP password.
