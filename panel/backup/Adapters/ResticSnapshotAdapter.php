<?php

declare(strict_types=1);

namespace App\Backup\Adapters;

use App\FileBrowser\Adapters\Archiver;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Adapters\FileOperations;
use App\FileBrowser\Adapters\PermissionManager;
use App\Services\Agent\AgentClient;
use RuntimeException;

class ResticSnapshotAdapter implements FileBrowserAdapter, FileOperations
{
    public function __construct(
        private AgentClient $agent,
        private string $snapshotId,
        private string $username,
    ) {}

    public static function fromConfig(array $config): static
    {
        return new static(
            app(AgentClient::class),
            $config['snapshot_id'],
            $config['username'],
        );
    }

    public function toArray(): array
    {
        return ['snapshot_id' => $this->snapshotId, 'username' => $this->username];
    }

    public function files(): FileOperations
    {
        return $this;
    }

    public function archiver(): ?Archiver
    {
        return null;
    }

    public function permissions(): ?PermissionManager
    {
        return null;
    }

    public function list(string $path, bool $showHidden = false): array
    {
        $result = $this->agent->send('jb.browse', [
            'username' => $this->username,
            'snapshot_id' => $this->snapshotId,
            'path' => $path,
        ]);

        if (! ($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to browse snapshot');
        }

        return ['items' => $result['items'] ?? []];
    }

    public function read(string $path): array
    {
        $result = $this->agent->send('jb.read_file', [
            'username' => $this->username,
            'snapshot_id' => $this->snapshotId,
            'path' => $path,
        ]);

        if (! ($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to read file');
        }

        return ['content' => $result['content'] ?? ''];
    }

    public function info(string $path): array
    {
        return ['info' => ['permissions' => 'r--r--r--']];
    }

    // Write operations — snapshots are read-only

    public function write(string $path, string $content): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function delete(string $path): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function mkdir(string $path): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function rename(string $oldPath, string $newPath): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function copy(string $source, string $destination): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function move(string $source, string $destination): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }

    public function upload(string $directory, string $filename, string $content): array
    {
        throw new RuntimeException(__('Snapshots are read-only'));
    }
}
