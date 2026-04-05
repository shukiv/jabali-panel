<?php

declare(strict_types=1);

namespace App\FileBrowser;

use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Adapters\LocalAdapter;
use App\FileBrowser\Adapters\SftpAdapter;
use App\FileBrowser\Adapters\SshRsyncAdapter;
use App\FileBrowser\Services\SidecarTrashStore;
use App\FileBrowser\Services\TrashManager;
use Illuminate\Support\ServiceProvider;
use Livewire\Livewire;

class FileBrowserServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->mergeConfigFrom(config_path('file-browser.php'), 'file-browser');

        $this->app->bind(FileBrowserAdapter::class, function ($app) {
            $adapter = config('file-browser.adapter', 'local');

            return match ($adapter) {
                'sftp' => new SftpAdapter(config('file-browser.sftp')),
                'ssh-rsync' => new SshRsyncAdapter(config('file-browser.ssh_rsync')),
                default => new LocalAdapter(config('file-browser.local.root', '')),
            };
        });

        $this->app->singleton(TrashManager::class, function ($app) {
            $adapter = $app->make(FileBrowserAdapter::class);

            return new TrashManager($adapter, new SidecarTrashStore($adapter));
        });
    }

    public function boot(): void
    {
        $this->loadViewsFrom(resource_path('views/file-browser'), 'file-browser');

        if (class_exists(Livewire::class)) {
            Livewire::component('file-browser-trash-table', \App\FileBrowser\Livewire\TrashTable::class);
        }

        if ($this->app->runningInConsole()) {
            $this->publishes([
                __DIR__.'/../config/file-browser.php' => config_path('file-browser.php'),
            ], 'file-browser-config');

            $this->publishes([
                __DIR__.'/../resources/views' => resource_path('views/vendor/file-browser'),
            ], 'file-browser-views');
        }
    }
}
