<x-filament-panels::page>
    {{-- Download handler --}}
    @script
    <script>
        $wire.on('download-backup-file', ({ content, filename }) => {
            const blob = new Blob([Uint8Array.from(atob(content), c => c.charCodeAt(0))]);
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = filename;
            document.body.appendChild(a);
            a.click();
            document.body.removeChild(a);
            URL.revokeObjectURL(url);
        });
    </script>
    @endscript

    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="danger"
        class="mb-6"
    >
        <x-slot name="heading">
            {{ __('Important') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Deleting a database will permanently remove all its data including tables, records, and associated user accounts. Always create a backup before making changes. This action cannot be undone.') }}
        </x-slot>
    </x-filament::section>

    {{-- Databases Section --}}
    <x-filament::section icon="heroicon-o-circle-stack" class="mb-6">
        <x-slot name="heading">
            {{ __('MySQL Databases') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Manage your MySQL databases. Use phpMyAdmin to browse and manage database content.') }}
        </x-slot>
        <x-slot name="headerEnd">
            <x-filament::badge color="info">{{ count($databases) }}</x-filament::badge>
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

    <x-filament-actions::modals />
</x-filament-panels::page>
