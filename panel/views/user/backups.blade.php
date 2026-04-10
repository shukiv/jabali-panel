<x-filament-panels::page>
    <div class="fi-sc-tabs">
        <x-filament::tabs>
            <x-filament::tabs.item :active="$activeTab === 'snapshots'" wire:click="$set('activeTab', 'snapshots')" icon="heroicon-o-archive-box">{{ __('Snapshots') }}</x-filament::tabs.item>
            @if($browseSnapshotId)
            <x-filament::tabs.item :active="$activeTab === 'browser'" wire:click="$set('activeTab', 'browser')" icon="heroicon-o-folder-open">{{ __('Browse') }}</x-filament::tabs.item>
            @endif
            @if($restoreSnapshotId)
            <x-filament::tabs.item :active="$activeTab === 'restore'" wire:click="$set('activeTab', 'restore')" icon="heroicon-o-arrow-path">{{ __('Restore') }}</x-filament::tabs.item>
            @endif
        </x-filament::tabs>

        <div wire:loading.remove wire:target="activeTab">
        <x-tab-loading-skeleton>
            @if($activeTab === 'snapshots')
            <x-filament::section :heading="__('Your Backups')" :description="trans_choice(':count snapshot|:count snapshots', count($snapshots), ['count' => count($snapshots)])">
                @if(count($snapshots) > 0)
                <table class="fi-ta-table w-full table-auto divide-y divide-gray-200 dark:divide-white/5">
                    <thead>
                        <tr>
                            <th class="fi-ta-header-cell px-3 py-3.5 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Snapshot') }}</th>
                            <th class="fi-ta-header-cell px-3 py-3.5 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Date') }}</th>
                            <th class="fi-ta-header-cell px-3 py-3.5 text-end text-sm font-semibold text-gray-950 dark:text-white"></th>
                        </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-200 dark:divide-white/5">
                        @foreach($snapshots as $snap)
                        <tr class="fi-ta-row">
                            <td class="fi-ta-cell px-3 py-4 text-sm text-gray-950 dark:text-white"><x-filament::badge color="gray">{{ $snap['id'] ?? '' }}</x-filament::badge></td>
                            <td class="fi-ta-cell px-3 py-4 text-sm text-gray-500 dark:text-gray-400">{{ $snap['date'] ?? $snap['time'] ?? '' }}</td>
                            <td class="fi-ta-cell px-3 py-4 text-end">
                                <div class="flex items-center justify-end gap-2">
                                    <x-filament::button wire:click="browseSnapshot(@json($snap['id']))" size="xs" color="gray" icon="heroicon-o-folder-open">{{ __('Browse') }}</x-filament::button>
                                    <x-filament::button wire:click="openRestore(@json($snap['id']))" size="xs" color="primary" icon="heroicon-o-arrow-path">{{ __('Restore') }}</x-filament::button>
                                </div>
                            </td>
                        </tr>
                        @endforeach
                    </tbody>
                </table>
                @else
                    <p>{{ __('No backups found.') }}</p>
                @endif
            </x-filament::section>
            @endif

            @if($activeTab === 'browser' && $browseSnapshotId)
            <x-filament::section :heading="__('File Browser')" :description="$browseSnapshotId" icon="heroicon-o-folder-open">
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
                            <x-filament::link wire:click="navigateTo(@json($item['path']))" tag="button">
                                <x-heroicon-o-folder class="inline h-5 w-5" /> {{ $item['name'] }}
                            </x-filament::link>
                        </x-filament::section>
                    @else
                        <x-filament::section compact>
                            <label>
                                <input type="checkbox" wire:click="toggleFileSelection(@json($item['path']))" @checked(in_array($item['path'], $selectedFiles))>
                                <x-heroicon-o-document class="inline h-5 w-5" /> {{ $item['name'] }}
                                @if(($item['size'] ?? 0) > 0) <small>({{ $this->formatBytes($item['size']) }})</small> @endif
                            </label>
                        </x-filament::section>
                    @endif
                @empty
                    <p>{{ __('Empty directory') }}</p>
                @endforelse

                @if(count($selectedFiles) > 0)
                    <div class="flex items-center gap-3 mt-3">
                        <x-filament::badge color="primary">{{ trans_choice(':count file selected|:count files selected', count($selectedFiles), ['count' => count($selectedFiles)]) }}</x-filament::badge>
                        <x-filament::button wire:click="restoreSelectedFiles" wire:confirm="{{ __('Restore selected files?') }}" icon="heroicon-o-arrow-path">{{ __('Restore Selected') }}</x-filament::button>
                    </div>
                @endif
            </x-filament::section>
            @endif

            @if($activeTab === 'restore' && $restoreSnapshotId)
            <x-filament::section :heading="__('Restore Snapshot')" :description="__('Snapshot :id', ['id' => $restoreSnapshotId])" icon="heroicon-o-arrow-path">
                <div class="flex flex-wrap gap-4 mb-4">
                    <label class="flex items-center gap-1.5"><input type="checkbox" wire:model.live="restoreFiles"> {{ __('Files') }}</label>
                    <label class="flex items-center gap-1.5"><input type="checkbox" wire:model.live="restoreDatabases"> {{ __('Databases') }}</label>
                    <label class="flex items-center gap-1.5"><input type="checkbox" wire:model.live="restoreMailboxes"> {{ __('Email') }}</label>
                </div>
                <label class="flex items-center gap-1.5 mb-4"><input type="checkbox" wire:model.live="restoreForce"> {{ __('Overwrite existing data') }}</label>

                @if($restoreForce)
                <x-filament::section compact icon="heroicon-o-exclamation-triangle" icon-color="danger">
                    {{ __('Warning: Existing data will be overwritten.') }}
                </x-filament::section>
                @endif

                <div class="flex items-center gap-2">
                    <x-filament::button wire:click="executeRestore" wire:confirm="{{ __('Start restore?') }}" icon="heroicon-o-arrow-path">{{ __('Start Restore') }}</x-filament::button>
                    <x-filament::button wire:click="$set('activeTab', 'snapshots')" color="gray">{{ __('Cancel') }}</x-filament::button>
                </div>
            </x-filament::section>

            @if($restoreOutput)
            <x-filament::section :heading="__('Output')" icon="heroicon-o-command-line">
                <x-filament::section compact>
                    <code><pre>{{ $restoreOutput }}</pre></code>
                </x-filament::section>
            </x-filament::section>
            @endif
            @endif
        </x-tab-loading-skeleton>
        </div>
    </div>

    <x-filament-actions::modals />
</x-filament-panels::page>
