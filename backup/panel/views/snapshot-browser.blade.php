<x-filament-panels::page>
    <x-filament::section icon="heroicon-o-folder-open"
        :heading="__('Snapshot :id — :user', ['id' => $snapshotId, 'user' => $username])">

        <x-slot name="afterHeader">
            @foreach($this->getFileBreadcrumbs() as $i => $crumb)
                @if($i > 0) / @endif
                @if($loop->last)
                    <strong>{{ $crumb['label'] }}</strong>
                @else
                    <x-filament::link wire:click="navigateTo(@json($crumb['path']))" tag="button">{{ $crumb['label'] }}</x-filament::link>
                @endif
            @endforeach
        </x-slot>

        @if($browsePath !== '')
            <x-filament::button wire:click="navigateUp" size="sm" color="gray" icon="heroicon-o-arrow-uturn-left">{{ __('Back') }}</x-filament::button>
        @endif

        @forelse($browseItems as $item)
            @if($item['is_dir'] ?? false)
                <x-filament::section compact>
                    <div wire:click="navigateTo(@json($item['path']))" class="cursor-pointer">
                        <x-heroicon-o-folder class="inline h-5 w-5 text-yellow-500" />
                        <strong>{{ $item['name'] }}</strong>
                    </div>
                </x-filament::section>
            @else
                <x-filament::section compact>
                    <label>
                        <input type="checkbox" wire:click="toggleFileSelection(@json($item['path']))" @checked(in_array($item['path'], $selectedFiles))>
                        <x-heroicon-o-document class="inline h-5 w-5 text-gray-400" />
                        {{ $item['name'] }}
                        @if(($item['size'] ?? 0) > 0)
                            <small>({{ $this->formatBytes($item['size']) }})</small>
                        @endif
                    </label>
                </x-filament::section>
            @endif
        @empty
            <p>{{ __('Empty directory') }}</p>
        @endforelse

        @if(count($selectedFiles) > 0)
            <x-filament::badge color="primary">{{ trans_choice(':count file selected|:count files selected', count($selectedFiles), ['count' => count($selectedFiles)]) }}</x-filament::badge>
            <x-filament::button wire:click="restoreSelectedFiles" wire:confirm="{{ __('Restore selected files?') }}" icon="heroicon-o-arrow-path">{{ __('Restore Selected') }}</x-filament::button>
        @endif
    </x-filament::section>

    <x-filament-actions::modals />
</x-filament-panels::page>
