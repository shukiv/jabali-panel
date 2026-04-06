<div>
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
    {{ $this->table }}

    <x-filament-actions::modals />

    @script
    <script>
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
    </script>
    @endscript
</div>
