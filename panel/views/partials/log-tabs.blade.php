<x-filament::tabs>
    <x-filament::tabs.item :active="$this->logFilter === 'all'" wire:click="$set('logFilter', 'all')" icon="heroicon-o-queue-list">{{ __('All') }}</x-filament::tabs.item>
    <x-filament::tabs.item :active="$this->logFilter === 'backup'" wire:click="$set('logFilter', 'backup')" icon="heroicon-o-cloud-arrow-up">{{ __('Backup Logs') }}</x-filament::tabs.item>
    <x-filament::tabs.item :active="$this->logFilter === 'restore'" wire:click="$set('logFilter', 'restore')" icon="heroicon-o-arrow-path">{{ __('Restore Logs') }}</x-filament::tabs.item>
</x-filament::tabs>
