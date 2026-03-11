<x-filament-panels::page>
    @if($this->isProcessing)
        <div wire:poll.5s="pollSyncLog"></div>
    @endif

    {{ $this->migrationForm }}

    <x-filament-actions::modals />
</x-filament-panels::page>
