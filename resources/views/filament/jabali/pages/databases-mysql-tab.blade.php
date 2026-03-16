<div class="space-y-6">
    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="danger"
    >
        <x-slot name="heading">
            {{ __('Important') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Deleting a database will permanently remove all its data including tables, records, and associated user accounts. Always create a backup before making changes. This action cannot be undone.') }}
        </x-slot>
    </x-filament::section>

    {{-- Databases Section --}}
    <x-filament::section icon="heroicon-o-circle-stack">
        <x-slot name="heading">
            {{ __('MySQL Databases') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Manage your MySQL databases. Use phpMyAdmin to browse and manage database content.') }}
        </x-slot>
        <x-slot name="headerEnd">
            <x-filament::badge color="info">{{ count($this->databases) }}</x-filament::badge>
        </x-slot>

        {{ $this->table }}
    </x-filament::section>

    {{-- Users Section --}}
    <x-filament::section icon="heroicon-o-users">
        <x-slot name="heading">
            {{ __('MySQL Users & Privileges') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Manage database users and their access permissions to your databases.') }}
        </x-slot>

        @livewire('database-users-table')
    </x-filament::section>
</div>
