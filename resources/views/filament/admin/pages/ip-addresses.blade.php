<x-filament-panels::page>
    <x-filament::section
        icon="heroicon-o-signal"
        icon-color="primary"
    >
        <x-slot name="heading">
            {{ __('Server IP Addresses') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Manage IPv4 and IPv6 addresses assigned to this server.') }}
        </x-slot>

        {{ $this->table }}
    </x-filament::section>

    <x-filament::section
        icon="heroicon-o-globe-alt"
        icon-color="info"
    >
        <x-slot name="heading">
            {{ __('Domain IP Assignments') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Assign default or dedicated IP addresses per domain.') }}
        </x-slot>

        @livewire(\App\Filament\Admin\Widgets\DomainIpAssignmentsTable::class)
    </x-filament::section>

    <x-filament-actions::modals />
</x-filament-panels::page>
