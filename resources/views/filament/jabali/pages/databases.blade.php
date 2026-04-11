<x-filament-panels::page>
    {{-- Download handler --}}
    @script
    <script>
        $wire.on('open-phpmyadmin', ({ url }) => {
            window.open(url, '_blank');
        });

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

    {{ $this->databasesForm }}

    <x-filament-actions::modals />
</x-filament-panels::page>
