<x-filament-panels::page>
    {{ $this->imageForm }}

    <div class="mt-6">
        <x-filament::button color="primary" wire:click="runOptimization" icon="heroicon-o-bolt">
            {{ __('Run Optimization') }}
        </x-filament::button>
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
