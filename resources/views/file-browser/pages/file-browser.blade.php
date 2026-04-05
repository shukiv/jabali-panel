<x-filament-panels::page>
    {{-- Warning Banner --}}
    @if($this->getWarningMessage())
        <x-filament::section
            icon="heroicon-o-exclamation-triangle"
            icon-color="warning"
        >
            <x-slot name="heading">
                {{ __('Warning') }}
            </x-slot>
            <x-slot name="description">
                {{ $this->getWarningMessage() }}
                @if($this->getWarningFolders())
                    {{ __('Avoid editing files in the') }}
                    @foreach($this->getWarningFolders() as $folder)
                        <code wire:key="warn-folder-{{ $loop->index }}" class="px-1.5 py-0.5 rounded bg-warning-100 dark:bg-warning-900/50 fi-color-warning fi-text-color-700 dark:fi-text-color-300 font-mono fi-section-header-description">{{ $folder }}</code>
                        @if(!$loop->last){{ __('and') }}@endif
                    @endforeach
                    {{ __('folders unless you know what you are doing.') }}
                @endif
            </x-slot>
        </x-filament::section>
    @endif

    {{-- Breadcrumbs --}}
    <nav class="fi-breadcrumbs mb-4">
        <ol class="flex flex-wrap items-center gap-x-2">
            @foreach($this->getPathBreadcrumbs() as $crumb)
                <li wire:key="crumb-{{ $loop->index }}" class="flex items-center gap-x-2">
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
                            x-on:click="$wire.navigateTo({{ \Illuminate\Support\Js::from($crumb['path']) }})"
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
        // Permission toggle ↔ mode sync (event delegation, no Livewire roundtrip)
        function permSyncTogglesFromMode(modal, mode) {
            if (!/^[0-7]{3,4}$/.test(mode)) return;
            const digits = mode.slice(-3).split('').map(Number);
            modal.querySelectorAll('[data-perm-bit]').forEach(btn => {
                const [group, bit] = btn.dataset.permBit.split('-').map(Number);
                const shouldBeOn = (digits[group] & bit) !== 0;
                const isOn = btn.getAttribute('aria-checked') === 'true';
                if (shouldBeOn !== isOn) btn.click();
            });
        }

        function permSyncModeFromToggles(modal) {
            const digits = [0, 0, 0];
            modal.querySelectorAll('[data-perm-bit]').forEach(btn => {
                const [group, bit] = btn.dataset.permBit.split('-').map(Number);
                if (btn.getAttribute('aria-checked') === 'true') digits[group] += bit;
            });
            const input = modal.querySelector('[data-perm-mode]');
            if (input) {
                input.value = digits.join('');
                input.dispatchEvent(new Event('input', { bubbles: true }));
            }
        }

        // Toggle → mode: click on any toggle button recalculates the mode
        document.addEventListener('click', (e) => {
            const btn = e.target.closest('[data-perm-bit]');
            if (!btn) return;
            const modal = btn.closest('.fi-modal');
            if (!modal) return;
            // Wait for Alpine to update aria-checked
            setTimeout(() => permSyncModeFromToggles(modal), 50);
        });

        // Mode → toggles: typing in the mode input updates the toggles
        document.addEventListener('change', (e) => {
            if (!e.target.matches('[data-perm-mode]')) return;
            const modal = e.target.closest('.fi-modal');
            if (!modal) return;
            permSyncTogglesFromMode(modal, e.target.value);
        });

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
