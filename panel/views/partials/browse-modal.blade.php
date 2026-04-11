<div class="space-y-3">
    {{-- Breadcrumbs --}}
    <div class="flex items-center gap-1 text-sm">
        @foreach($breadcrumbs as $i => $crumb)
            @if($i > 0)
                <x-heroicon-o-chevron-right class="h-3 w-3 text-gray-400" />
            @endif
            @if($loop->last)
                <span class="font-medium text-gray-900 dark:text-white">{{ $crumb['label'] === '__server__' ? __('Server') : $crumb['label'] }}</span>
            @else
                <button wire:click="navigateTo(@json($crumb['path']))" class="text-primary-600 hover:underline dark:text-primary-400">
                    {{ $crumb['label'] === '__server__' ? __('Server') : $crumb['label'] }}
                </button>
            @endif
        @endforeach
    </div>

    {{-- Back button --}}
    @if($browsePath !== '')
        <div>
            <x-filament::button wire:click="navigateUp" size="sm" color="gray" icon="heroicon-o-arrow-uturn-left">
                {{ __('Back') }}
            </x-filament::button>
        </div>
    @endif

    {{-- File list --}}
    <div class="divide-y divide-gray-200 dark:divide-white/10 rounded-lg border border-gray-200 dark:border-white/10">
        @forelse($items as $item)
            <div class="flex items-center gap-3 px-3 py-2 {{ ($item['is_dir'] ?? false) ? 'cursor-pointer hover:bg-gray-50 dark:hover:bg-white/5' : '' }}"
                @if($item['is_dir'] ?? false) wire:click="navigateTo(@json($item['path']))" @endif>
                @if($item['is_dir'] ?? false)
                    <x-heroicon-o-folder class="h-5 w-5 shrink-0 text-warning-500" />
                @else
                    @if($browseUser !== '__server__')
                        <input type="checkbox"
                            wire:click="toggleFileSelection(@json($item['path']))"
                            @checked(in_array($item['path'], $selectedFiles))
                            class="fi-checkbox-input rounded border-gray-300 text-primary-600 shadow-sm focus:ring-primary-600 dark:border-white/10 dark:bg-white/5">
                    @else
                        <x-heroicon-o-document class="h-5 w-5 shrink-0 text-gray-400" />
                    @endif
                @endif
                <span class="flex-1 text-sm {{ ($item['is_dir'] ?? false) ? 'font-medium' : '' }}">{{ $item['name'] }}</span>
                @if(! ($item['is_dir'] ?? false) && ($item['size'] ?? 0) > 0)
                    <span class="text-xs text-gray-500">{{ $this->formatBytes($item['size']) }}</span>
                @endif
                @if(! empty($item['permissions']))
                    <span class="text-xs text-gray-400 font-mono">{{ $item['permissions'] }}</span>
                @endif
            </div>
        @empty
            <div class="px-3 py-6 text-center text-sm text-gray-500">
                {{ __('Empty directory') }}
            </div>
        @endforelse
    </div>

    {{-- Restore selected (for user snapshots only) --}}
    @if($browseUser !== '__server__' && count($selectedFiles) > 0)
        <div class="flex items-center gap-3">
            <x-filament::badge color="primary">
                {{ trans_choice(':count file selected|:count files selected', count($selectedFiles), ['count' => count($selectedFiles)]) }}
            </x-filament::badge>
            <x-filament::button
                wire:click="restoreSelectedFiles"
                wire:confirm="{{ __('Restore selected files?') }}"
                size="sm"
                icon="heroicon-o-arrow-path">
                {{ __('Restore Selected') }}
            </x-filament::button>
        </div>
    @endif
</div>
