@if(count($items) > 0)
    <div class="flex justify-end mb-4">
        <x-filament::button
            wire:click="emptyTrash"
            wire:confirm="{{ __('Are you sure you want to permanently delete all items in trash? This cannot be undone.') }}"
            color="danger"
            size="sm"
            icon="heroicon-o-trash"
        >
            {{ __('Empty Trash') }}
        </x-filament::button>
    </div>

    <div class="fi-ta-content rounded-xl bg-white shadow-sm ring-1 ring-gray-950/5 dark:bg-gray-900 dark:ring-white/10">
        <div class="overflow-x-auto">
            <table class="fi-ta-table w-full table-auto divide-y divide-gray-200 text-start dark:divide-white/5">
                <thead class="divide-y divide-gray-200 dark:divide-white/5">
                    <tr class="bg-gray-50 dark:bg-white/5">
                        <th class="fi-ta-header-cell px-3 py-3.5 sm:first-of-type:ps-6 sm:last-of-type:pe-6 fi-table-header-cell-name text-start">
                            <span class="fi-section-header-heading">{{ __('Name') }}</span>
                        </th>
                        <th class="fi-ta-header-cell px-3 py-3.5 fi-table-header-cell-deleted text-start hidden sm:table-cell">
                            <span class="fi-section-header-heading">{{ __('Deleted') }}</span>
                        </th>
                        <th class="fi-ta-header-cell px-3 py-3.5 sm:first-of-type:ps-6 sm:last-of-type:pe-6 text-end">
                            <span class="fi-section-header-heading">{{ __('Actions') }}</span>
                        </th>
                    </tr>
                </thead>
                <tbody class="divide-y divide-gray-200 dark:divide-white/5">
                    @foreach($items as $item)
                        <tr class="fi-ta-row hover:bg-gray-50 dark:hover:bg-white/5 transition">
                            <td class="fi-ta-cell px-3 py-4 sm:first-of-type:ps-6 sm:last-of-type:pe-6">
                                <div class="flex items-center gap-3">
                                    <x-filament::icon
                                        :icon="$item['is_dir'] ? 'heroicon-o-folder' : 'heroicon-o-document'"
                                        :size="\Filament\Support\Enums\IconSize::Medium"
                                        @class([
                                            'shrink-0',
                                            'fi-color-warning fi-text-color-500' => $item['is_dir'],
                                            'fi-color-primary fi-text-color-500' => !$item['is_dir'],
                                        ])
                                    />
                                    <div class="min-w-0">
                                        <span class="fi-section-header-heading">{{ $item['name'] }}</span>
                                        <span class="block fi-section-header-description sm:hidden">
                                            {{ date('M d, Y H:i', $item['trashed_at']) }}
                                        </span>
                                    </div>
                                </div>
                            </td>
                            <td class="fi-ta-cell px-3 py-4 fi-section-header-description hidden sm:table-cell">
                                {{ date('M d, Y H:i', $item['trashed_at']) }}
                            </td>
                            <td class="fi-ta-cell px-3 py-4 sm:first-of-type:ps-6 sm:last-of-type:pe-6">
                                <div class="flex items-center justify-end gap-1">
                                    <x-filament::icon-button
                                        wire:click="restoreFromTrash('{{ $item['trash_name'] }}')"
                                        color="success"
                                        icon="heroicon-o-arrow-uturn-left"
                                        size="sm"
                                        :tooltip="__('Restore')"
                                    />
                                    <x-filament::icon-button
                                        wire:click="deleteFromTrash('{{ $item['trash_name'] }}')"
                                        wire:confirm="{{ __('Permanently delete this item? This cannot be undone.') }}"
                                        color="danger"
                                        icon="heroicon-o-x-mark"
                                        size="sm"
                                        :tooltip="__('Delete')"
                                    />
                                </div>
                            </td>
                        </tr>
                    @endforeach
                </tbody>
            </table>
        </div>
    </div>
@else
    <div class="flex flex-col items-center justify-center rounded-xl bg-white p-12 shadow-sm ring-1 ring-gray-950/5 dark:bg-gray-900 dark:ring-white/10">
        <x-filament::icon icon="heroicon-o-trash" :size="\Filament\Support\Enums\IconSize::TwoExtraLarge" class="mb-4 fi-color-gray fi-text-color-400" />
        <p class="fi-section-header-heading">{{ __('Trash is empty') }}</p>
        <p class="fi-section-header-description mt-1">{{ __('Deleted items will appear here') }}</p>
    </div>
@endif
