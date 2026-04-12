<?php

declare(strict_types=1);

namespace App\Agent;

/**
 * Append-only audit log for every privileged agent method call.
 *
 * Format: one JSON object per line. Written atomically with LOCK_EX flock
 * so concurrent agent method handlers don't interleave.
 *
 * Sensitive fields (usernames, paths, secrets) are NOT logged raw — only
 * a SHA-256 hash of the canonical args. This keeps the log useful for
 * forensics (matching repeated calls, correlating incident timelines)
 * without turning it into a credential dump.
 *
 * See docs/adr/0007-agent-v2-control-plane.md.
 */
final class AuditLog
{
    public function __construct(private readonly string $path) {}

    /**
     * @param  array<string, mixed>  $argsSummary  Canonical args; will be JSON-encoded and hashed.
     */
    public function append(
        string $method,
        array $argsSummary,
        int $callerUid,
        string $result,
        int $durationMs,
        ?string $error = null,
    ): void {
        $canonical = $this->canonicalize($argsSummary);
        $hash = hash('sha256', $canonical);

        $entry = [
            'ts' => gmdate('Y-m-d\TH:i:s\Z'),
            'method' => $method,
            'args_hash' => $hash,
            'caller_uid' => $callerUid,
            'result' => $result,
            'duration_ms' => $durationMs,
        ];

        if ($error !== null) {
            $entry['error'] = substr($error, 0, 512);
        }

        $line = json_encode($entry, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)."\n";

        $this->ensureFile();

        $fp = fopen($this->path, 'ab');
        if ($fp === false) {
            return; // Best-effort. Don't block the real operation on audit failure.
        }

        try {
            if (flock($fp, LOCK_EX)) {
                fwrite($fp, $line);
                fflush($fp);
                flock($fp, LOCK_UN);
            }
        } finally {
            fclose($fp);
        }
    }

    /**
     * @param  array<string, mixed>  $args
     */
    private function canonicalize(array $args): string
    {
        // Sort keys so that {a:1,b:2} and {b:2,a:1} hash identically.
        ksort($args);

        return (string) json_encode($args, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    }

    private function ensureFile(): void
    {
        if (file_exists($this->path)) {
            return;
        }

        $dir = dirname($this->path);
        if (! is_dir($dir)) {
            @mkdir($dir, 0750, recursive: true);
        }

        // Create with 0600 (owner read/write only). Even reading is restricted.
        $fp = @fopen($this->path, 'ab');
        if ($fp !== false) {
            fclose($fp);
            @chmod($this->path, 0600);
        }
    }
}
