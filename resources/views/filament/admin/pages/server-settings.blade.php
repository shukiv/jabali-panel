<x-filament-panels::page>
    {{ $this->settingsForm }}

    @if ($activeTab === 'general')
        <x-filament::section>
            <x-slot name="heading">{{ __('Logo Upload') }}</x-slot>

            <x-filament::fieldset>
                <x-slot name="label">{{ __('Light Logo') }}</x-slot>

                @if ($currentLogo)
                    <img src="/storage/{{ $currentLogo }}" alt="{{ __('Light Logo') }}" style="max-height:64px">
                @endif

                <input type="file" wire:model="logoLightUpload" accept="image/png,image/jpeg,image/webp,image/svg+xml">

                <div wire:loading wire:target="logoLightUpload">{{ __('Uploading...') }}</div>

                @if ($logoLightUpload)
                    <x-filament::button wire:click="saveLogoLight" icon="heroicon-o-check" size="sm" color="success">
                        {{ __('Save') }}
                    </x-filament::button>
                @endif
            </x-filament::fieldset>

            <x-filament::fieldset>
                <x-slot name="label">{{ __('Dark Logo') }}</x-slot>

                @if ($currentLogoDark)
                    <img src="/storage/{{ $currentLogoDark }}" alt="{{ __('Dark Logo') }}" style="max-height:64px">
                @endif

                <input type="file" wire:model="logoDarkUpload" accept="image/png,image/jpeg,image/webp,image/svg+xml">

                <div wire:loading wire:target="logoDarkUpload">{{ __('Uploading...') }}</div>

                @if ($logoDarkUpload)
                    <x-filament::button wire:click="saveLogoDark" icon="heroicon-o-check" size="sm" color="success">
                        {{ __('Save') }}
                    </x-filament::button>
                @endif
            </x-filament::fieldset>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
