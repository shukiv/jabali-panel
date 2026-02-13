<x-filament-panels::page>
    <x-filament::section>
        <x-slot name="heading">{{ __('Mailing Lists') }}</x-slot>
        <x-slot name="description">{{ __('Connect a mailing list provider or store settings for your administrator to configure.') }}</x-slot>

        {{ $this->mailingForm }}

        <div class="mt-6">
            <x-filament::button wire:click="saveSettings" icon="heroicon-o-check" color="primary">
                {{ __('Save Settings') }}
            </x-filament::button>
        </div>
    </x-filament::section>

    <x-filament-actions::modals />
</x-filament-panels::page>
