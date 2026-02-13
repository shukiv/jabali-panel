@livewire(\App\Filament\Admin\Widgets\WhmMigrationStatusTable::class, [
    'selectedAccounts' => $this->selectedAccounts,
    'migrationStatus' => $this->migrationStatus,
    'isMigrating' => $this->isMigrating,
    'migrationCacheKey' => $this->getMigrationCacheKey(),
], key('whm-migration-status-' . ($this->isMigrating ? 'active' : 'idle') . '-' . count($this->migrationStatus)))
