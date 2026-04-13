<?php

// Addons: registered only when their files are present on disk. We check
// file_exists() rather than class_exists() because the latter triggers
// composer's autoloader, which then tries to include() a stale classmap
// entry left over from a previous install. If the operator uninstalls
// the addon before composer dump-autoload runs, that include() fatals
// and breaks the panel boot. file_exists() is a pure syscall with no
// autoload side-effects.
$addonProviders = [];
if (file_exists(__DIR__.'/../app/Backup/BackupServiceProvider.php')) {
    $addonProviders[] = \App\Backup\BackupServiceProvider::class;
}
if (file_exists(__DIR__.'/../app/JabaliTerminal/JabaliTerminalServiceProvider.php')) {
    $addonProviders[] = \App\JabaliTerminal\JabaliTerminalServiceProvider::class;
}

return array_values(array_merge([
    App\Providers\AppServiceProvider::class,
    App\Providers\Filament\AdminPanelProvider::class,
    App\Providers\Filament\JabaliPanelProvider::class,
    App\FileBrowser\FileBrowserServiceProvider::class,
], $addonProviders));
