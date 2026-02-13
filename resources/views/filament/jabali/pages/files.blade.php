<x-filament-panels::page>
    <style>
        /* Compact spacing for File Manager rows */
        #file-dropzone .fi-ta-text:not(.fi-inline) {
            padding-top: 0.2rem !important;
            padding-bottom: 0.2rem !important;
        }

        #file-dropzone .fi-ta-text-item {
            line-height: 1.05rem !important;
        }

        #file-dropzone .fi-ta-record-content-ctn {
            gap: 0.25rem !important;
            padding-top: 0.25rem !important;
            padding-bottom: 0.25rem !important;
        }

        #file-dropzone .fi-ta-record-checkbox {
            margin-top: 0.2rem !important;
            margin-bottom: 0.2rem !important;
        }

        #file-dropzone td.fi-ta-cell.fi-ta-selection-cell,
        #file-dropzone td.fi-ta-cell.fi-ta-group-selection-cell {
            padding-top: 0.2rem !important;
            padding-bottom: 0.2rem !important;
        }

        #file-dropzone td.fi-ta-cell:has(.fi-ta-actions) {
            padding-top: 0.2rem !important;
            padding-bottom: 0.2rem !important;
        }

        #file-dropzone .fi-ta-actions {
            gap: 0.35rem !important;
        }

        #file-dropzone .fi-ta-actions .fi-btn,
        #file-dropzone .fi-ta-actions .fi-icon-btn {
            min-height: 1.65rem !important;
        }
    </style>

    {{-- Warning Banner --}}
    <x-filament::section
        icon="heroicon-o-exclamation-triangle"
        icon-color="warning"
    >
        <x-slot name="heading">
            {{ __('Warning') }}
        </x-slot>
        <x-slot name="description">
            {{ __('Deleting or modifying system files can break your website. Avoid editing files in the') }}
            <code class="px-1.5 py-0.5 rounded bg-warning-100 dark:bg-warning-900/50 fi-color-warning fi-text-color-700 dark:fi-text-color-300 font-mono fi-section-header-description">{{ __('conf') }}</code>
            {{ __('and') }}
            <code class="px-1.5 py-0.5 rounded bg-warning-100 dark:bg-warning-900/50 fi-color-warning fi-text-color-700 dark:fi-text-color-300 font-mono fi-section-header-description">{{ __('logs') }}</code>
            {{ __('and') }}
            {{ __('folders unless you know what you are doing.') }}
        </x-slot>
    </x-filament::section>

    {{-- Breadcrumbs --}}
    <nav class="fi-breadcrumbs mb-4">
        <ol class="flex flex-wrap items-center gap-x-2">
            @foreach($this->getPathBreadcrumbs() as $crumb)
                <li class="flex items-center gap-x-2">
                    @if(!$loop->first)
                        <x-filament::icon
                            icon="heroicon-m-chevron-right"
                            :size="\Filament\Support\Enums\IconSize::Small"
                            class="fi-color-gray fi-text-color-400"
                        />
                    @endif

                    @if($loop->last)
                        <span class="fi-section-header-description">
                            {{ $crumb['name'] }}
                        </span>
                    @else
                        <x-filament::link
                            tag="button"
                            wire:click="navigateTo('{{ $crumb['path'] }}')"
                            :icon="$loop->first ? 'heroicon-o-home' : null"
                            size="sm"
                        >
                            {{ $crumb['name'] }}
                        </x-filament::link>
                    @endif
                </li>
            @endforeach
        </ol>
    </nav>

    {{-- File Table --}}
    <div id="file-dropzone" class="transition-all duration-200">
        {{ $this->table }}
    </div>

    <x-filament-actions::modals />

    {{-- Handle upload errors globally --}}
    <div x-data x-on:livewire-upload-error.window="$wire.showUploadError()"></div>

    @script
    <script>
        // File download handler
        $wire.on('download-file', ({ content, filename }) => {
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

        // File dropzone for uploads
        const dropzone = document.getElementById('file-dropzone');
        if (dropzone) {
            ['dragenter','dragover','dragleave','drop'].forEach(e => dropzone.addEventListener(e, ev => { ev.preventDefault(); ev.stopPropagation(); }));
            ['dragenter','dragover'].forEach(e => dropzone.addEventListener(e, () => dropzone.classList.add('ring-2', 'ring-primary-500', 'ring-offset-2', 'dark:ring-offset-gray-900')));
            ['dragleave','drop'].forEach(e => dropzone.addEventListener(e, () => dropzone.classList.remove('ring-2', 'ring-primary-500', 'ring-offset-2', 'dark:ring-offset-gray-900')));
            dropzone.addEventListener('drop', async e => {
                const files = e.dataTransfer?.files;
                if (files?.length) {
                    for (const file of files) {
                        const reader = new FileReader();
                        reader.onload = () => {
                            const base64 = reader.result.split(',')[1];
                            $wire.uploadDroppedFile(file.name, base64);
                        };
                        reader.readAsDataURL(file);
                    }
                }
            });
        }
    </script>
    @endscript
</x-filament-panels::page>
