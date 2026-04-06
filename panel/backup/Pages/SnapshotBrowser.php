<?php

declare(strict_types=1);

namespace App\Backup\Pages;

use App\Backup\Adapters\ResticSnapshotAdapter;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Pages\FileBrowser;
use App\Services\Agent\AgentClient;
use Illuminate\Contracts\Support\Htmlable;

class SnapshotBrowser extends FileBrowser
{
    protected static ?string $slug = 'backups/browse/{snapshotId}/{username}';

    protected static bool $shouldRegisterNavigation = false;

    public ?string $snapshotId = null;

    public ?string $username = null;

    public function mount(?string $snapshotId = null, ?string $username = null): void
    {
        $this->snapshotId = $snapshotId ?? request()->route('snapshotId');
        $this->username = $username ?? request()->route('username');

        app()->bind(FileBrowserAdapter::class, fn () => new ResticSnapshotAdapter(
            app(AgentClient::class),
            $this->snapshotId,
            $this->username,
        ));

        parent::mount();
    }

    public function getTitle(): string|Htmlable
    {
        return __('Browse Snapshot :id', ['id' => $this->snapshotId ?? '']);
    }
}
