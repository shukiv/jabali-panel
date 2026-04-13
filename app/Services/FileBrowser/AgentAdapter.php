<?php

declare(strict_types=1);

namespace App\Services\FileBrowser;

use App\FileBrowser\Adapters\Archiver;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Adapters\FileOperations;
use App\FileBrowser\Adapters\PermissionManager;
use App\Services\Agent\AgentClient;

final class AgentAdapter implements FileBrowserAdapter
{
    private AgentFileOperations $fileOps;

    private AgentPermissionManager $permissionMgr;

    private AgentArchiver $archiver;

    public function __construct(
        private AgentClient $agent,
        private string $username,
    ) {
        $this->fileOps = new AgentFileOperations($agent, $username);
        $this->permissionMgr = new AgentPermissionManager($agent, $username);
        $this->archiver = new AgentArchiver($agent, $username);
    }

    /** @param array{username: string} $config */
    public static function fromConfig(array $config): static
    {
        return new self(app(AgentClient::class), $config['username']);
    }

    /** @return array{username: string} */
    public function toArray(): array
    {
        return ['username' => $this->username];
    }

    public function files(): FileOperations
    {
        return $this->fileOps;
    }

    public function archiver(): ?Archiver
    {
        return $this->archiver;
    }

    public function permissions(): ?PermissionManager
    {
        return $this->permissionMgr;
    }
}
