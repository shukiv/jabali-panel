<?php

return array_values(array_filter([
    App\Providers\AppServiceProvider::class,
    App\Providers\Filament\AdminPanelProvider::class,
    App\Providers\Filament\JabaliPanelProvider::class,
    App\FileBrowser\FileBrowserServiceProvider::class,
    // Addons: registered only when their files are present on disk so that
    // running `composer install` / `php artisan optimize` on a panel without
    // the addon installed does not fail.
    class_exists(\App\JabaliTerminal\JabaliTerminalServiceProvider::class)
        ? \App\JabaliTerminal\JabaliTerminalServiceProvider::class
        : null,
]));
