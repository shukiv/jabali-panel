<?php

declare(strict_types=1);

namespace App\Services\BackgroundTasks;

use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use InvalidArgumentException;
use RuntimeException;

/**
 * Facade for install/uninstall that routes through the background-task
 * runner when the feature flag is on, or throws for callers that should
 * keep using the legacy agent.send('addon.install') path.
 *
 * The Filament ServerSettings page delegates to this service when
 * jabali.agent_v2.addons_enabled is true. Legacy callers are unchanged.
 *
 * @see docs/adr/0007-agent-v2-control-plane.md
 */
final class AddonInstaller
{
    public function __construct(private readonly BackgroundTaskDispatcher $dispatcher) {}

    public function install(string $addonId): ?BackgroundTask
    {
        $addon = $this->lookupAddon($addonId);

        if (file_exists($addon['binary'])) {
            throw new RuntimeException($addon['name'].' is already installed');
        }

        return $this->dispatcher->dispatch(
            type: BackgroundTaskType::AddonInstall,
            argv: [
                '/usr/bin/php',
                base_path('artisan'),
                'jabali:tasks:run-addon-install',
                '--addon-id='.$addonId,
            ],
            payload: ['addon' => $addonId, 'name' => $addon['name']],
            dedupeKey: 'addon:install:'.$addonId,
            limits: ['cpu' => 50, 'memory' => '512M', 'io' => 100],
        );
    }

    public function uninstall(string $addonId): ?BackgroundTask
    {
        $addon = $this->lookupAddon($addonId);

        if (! file_exists($addon['binary'])) {
            throw new RuntimeException($addon['name'].' is not installed');
        }
        if (empty($addon['uninstall_command'])) {
            throw new RuntimeException($addon['name'].' does not support uninstall');
        }

        return $this->dispatcher->dispatch(
            type: BackgroundTaskType::AddonUninstall,
            argv: [
                '/usr/bin/php',
                base_path('artisan'),
                'jabali:tasks:run-addon-uninstall',
                '--addon-id='.$addonId,
            ],
            payload: ['addon' => $addonId, 'name' => $addon['name']],
            dedupeKey: 'addon:uninstall:'.$addonId,
            limits: ['cpu' => 50, 'memory' => '256M', 'io' => 100],
        );
    }

    /**
     * @return array{name: string, binary: string, install_url: string, uninstall_command: string}
     */
    private function lookupAddon(string $addonId): array
    {
        $addons = config('jabali-addons', []);
        if (! isset($addons[$addonId])) {
            throw new InvalidArgumentException("Unknown addon: {$addonId}");
        }

        return $addons[$addonId];
    }
}
