<?php

declare(strict_types=1);

namespace App\Agent\Jobs;

use PDO;

/**
 * Agent-local SQLite store for background tasks. Mirror of panel's
 * background_tasks table, owned by root, used as the authoritative view of
 * what the agent is currently tracking.
 *
 * WAL mode is enabled so concurrent method handlers can read while a writer
 * holds a transaction.
 */
final class JobStore
{
    private readonly PDO $pdo;

    public function __construct(string $path)
    {
        $dir = dirname($path);
        if (! is_dir($dir)) {
            @mkdir($dir, 0700, recursive: true);
        }

        $this->pdo = new PDO("sqlite:{$path}", options: [
            PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
            PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
        ]);

        $this->pdo->exec('PRAGMA journal_mode = WAL');
        $this->pdo->exec('PRAGMA synchronous = NORMAL');
        $this->pdo->exec('PRAGMA busy_timeout = 5000');

        $this->pdo->exec(<<<'SQL'
            CREATE TABLE IF NOT EXISTS jobs (
                id          TEXT PRIMARY KEY,
                type        TEXT NOT NULL,
                status      TEXT NOT NULL,
                unit        TEXT NOT NULL,
                pid         INTEGER,
                log_path    TEXT NOT NULL,
                payload     TEXT,
                exit_code   INTEGER,
                error       TEXT,
                started_at  TEXT,
                finished_at TEXT,
                created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
                updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
            )
        SQL);

        $this->pdo->exec('CREATE INDEX IF NOT EXISTS idx_jobs_status_type ON jobs(status, type, created_at)');
    }

    /**
     * @param  array<string, mixed>  $attrs  Must include: id, type, status, unit, log_path. Optional: pid, payload.
     */
    public function insert(array $attrs): void
    {
        $stmt = $this->pdo->prepare(<<<'SQL'
            INSERT INTO jobs (id, type, status, unit, pid, log_path, payload)
            VALUES (:id, :type, :status, :unit, :pid, :log_path, :payload)
        SQL);

        $stmt->execute([
            ':id' => $attrs['id'],
            ':type' => $attrs['type'],
            ':status' => $attrs['status'],
            ':unit' => $attrs['unit'],
            ':pid' => $attrs['pid'] ?? null,
            ':log_path' => $attrs['log_path'],
            ':payload' => isset($attrs['payload']) ? json_encode($attrs['payload']) : null,
        ]);
    }

    /**
     * @return array<string, mixed>|null
     */
    public function get(string $id): ?array
    {
        $stmt = $this->pdo->prepare('SELECT * FROM jobs WHERE id = :id');
        $stmt->execute([':id' => $id]);
        $row = $stmt->fetch();

        return $row === false ? null : $this->hydrate($row);
    }

    /**
     * @param  array<string, mixed>  $extra  Additional columns to set (pid, started_at, finished_at, exit_code, error).
     */
    public function updateStatus(string $id, string $status, array $extra = []): void
    {
        $sets = ['status = :status', "updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"];
        $params = [':id' => $id, ':status' => $status];

        foreach (['pid', 'started_at', 'finished_at', 'exit_code', 'error'] as $col) {
            if (array_key_exists($col, $extra)) {
                $sets[] = "{$col} = :{$col}";
                $params[":{$col}"] = $extra[$col];
            }
        }

        $sql = 'UPDATE jobs SET '.implode(', ', $sets).' WHERE id = :id';
        $this->pdo->prepare($sql)->execute($params);
    }

    /**
     * @param  array{status?: string, type?: string, limit?: int}  $filters
     * @return list<array<string, mixed>>
     */
    public function list(array $filters): array
    {
        $where = [];
        $params = [];

        if (isset($filters['status'])) {
            $where[] = 'status = :status';
            $params[':status'] = $filters['status'];
        }
        if (isset($filters['type'])) {
            $where[] = 'type = :type';
            $params[':type'] = $filters['type'];
        }

        $sql = 'SELECT * FROM jobs';
        if ($where) {
            $sql .= ' WHERE '.implode(' AND ', $where);
        }
        $sql .= ' ORDER BY created_at DESC';

        $limit = $filters['limit'] ?? 100;
        $sql .= ' LIMIT '.(int) $limit;

        $stmt = $this->pdo->prepare($sql);
        $stmt->execute($params);

        return array_map(fn ($row) => $this->hydrate($row), $stmt->fetchAll());
    }

    /**
     * @return list<array<string, mixed>>
     */
    public function runningJobs(): array
    {
        return $this->list(['status' => 'running']);
    }

    /**
     * @param  array<string, mixed>  $row
     * @return array<string, mixed>
     */
    private function hydrate(array $row): array
    {
        if (isset($row['payload']) && is_string($row['payload'])) {
            $decoded = json_decode($row['payload'], true);
            $row['payload'] = is_array($decoded) ? $decoded : [];
        }

        return $row;
    }
}
