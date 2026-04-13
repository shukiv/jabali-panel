<div
    x-data="{ items: $wire.entangle('restoreFileList') }"
    class="flex flex-wrap gap-1"
>
    <template x-for="(path, idx) in items.filter(p => p && p.length > 0)" :key="path">
        <span class="fi-badge fi-badge-size-sm inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs font-medium bg-primary-50 text-primary-700 dark:bg-primary-400/10 dark:text-primary-400">
            <span x-text="path"></span>
            <button
                type="button"
                x-on:click.stop="items = items.filter(p => p !== path)"
                class="ml-1 text-primary-500 hover:text-danger-500"
            >&times;</button>
        </span>
    </template>
    <span x-show="items.filter(p => p && p.length > 0).length === 0" class="text-sm text-gray-500 italic">
        {{ __('No files selected. Select files below and click "Add Selected to Restore".') }}
    </span>
</div>
