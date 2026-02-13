<x-filament-panels::page>
    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="danger"
    >
        <x-slot name="heading">
            {{ __('Warning: System Services') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Stopping or disabling critical services can make your server unstable or inaccessible. Only modify services if you understand the consequences. Services like Nginx, PHP-FPM, and MariaDB are essential for running websites.') }}
        </x-slot>
    </x-filament::section>

    {{-- Services Table --}}
    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament-panels::page>
