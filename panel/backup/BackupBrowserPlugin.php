<?php

declare(strict_types=1);

namespace App\Backup;

use App\Filament\Admin\Pages\SnapshotBrowser;
use Filament\Contracts\Plugin;
use Filament\Panel;

class BackupBrowserPlugin implements Plugin
{
    public static function make(): static
    {
        return app(static::class);
    }

    public static function get(): static
    {
        return filament(static::class);
    }

    public function getId(): string
    {
        return 'backup-browser';
    }

    public function register(Panel $panel): void
    {
        $panel->pages([
            SnapshotBrowser::class,
        ]);
    }

    public function boot(Panel $panel): void
    {
        //
    }
}
