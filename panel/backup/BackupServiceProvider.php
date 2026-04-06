<?php

declare(strict_types=1);

namespace App\Backup;

use App\Filament\Admin\Pages\SnapshotBrowser;
use Filament\Facades\Filament;
use Illuminate\Support\ServiceProvider;

class BackupServiceProvider extends ServiceProvider
{
    public function boot(): void
    {
        Filament::serving(function (): void {
            $panel = Filament::getCurrentPanel();

            if ($panel && $panel->getId() === 'admin') {
                $panel->pages([
                    SnapshotBrowser::class,
                ]);
            }
        });
    }
}
