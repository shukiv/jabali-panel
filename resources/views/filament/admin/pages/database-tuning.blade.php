<x-filament-panels::page>
    <x-filament::section icon="heroicon-o-exclamation-triangle" icon-color="warning">
        <x-slot name="heading">{{ __('Runtime Changes') }}</x-slot>
        <x-slot name="description">
            {{ __('Changes apply immediately but may reset after a database restart. Persisting configuration will be added in a later step.') }}
        </x-slot>
    </x-filament::section>

    <div class="mt-6">
        {{ $this->table }}
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
