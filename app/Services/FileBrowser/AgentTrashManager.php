<?php

declare(strict_types=1);

namespace App\Services\FileBrowser;

use App\FileBrowser\Services\TrashResult;
use App\Services\Agent\AgentClient;
use RuntimeException;

/**
 * TrashManager that delegates directly to the Jabali agent daemon.
 *
 * Replaces the package's TrashManager entirely because the agent handles
 * trash natively (file.trash, file.restore, file.list_trash, file.empty_trash)
 * as root. The package's default TrashManager uses adapter primitives (move,
 * write, read) which don't work when PHP can't access user directories.
 */
class AgentTrashManager
{
    public function __construct(
        private readonly AgentClient $agent,
        private readonly string $username,
    ) {}

    public function trash(string $path): TrashResult
    {
        $result = $this->agent->fileTrash($this->username, $path);

        if (! ($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to move to trash');
        }

        return new TrashResult;
    }

    /**
     * @return list<array{trash_name: string, name: string, original_path: string, trashed_at: int, is_dir: bool, size: int|null}>
     */
    public function items(): array
    {
        $result = $this->agent->fileListTrash($this->username);

        return $result['items'] ?? [];
    }

    public function restore(string $trashName): TrashResult
    {
        $trashName = basename($trashName);
        $result = $this->agent->fileRestore($this->username, $trashName);

        if (! ($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to restore from trash');
        }

        return new TrashResult(['restored_path' => $result['restored_path'] ?? '']);
    }

    public function deletePermanently(string $trashName): TrashResult
    {
        $trashName = basename($trashName);
        $trashPath = ".trash/$trashName";
        $this->agent->fileDelete($this->username, $trashPath);

        return new TrashResult;
    }

    public function empty(): TrashResult
    {
        $result = $this->agent->fileEmptyTrash($this->username);

        return new TrashResult(['deleted' => $result['deleted'] ?? 0]);
    }
}
