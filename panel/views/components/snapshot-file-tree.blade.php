@php
    $items = $getState() ?? [];
@endphp

<div x-data="{
    selected: @entangle($getStatePath()).defer ?? [],
    toggleItem(path) {
        if (this.selected.includes(path)) {
            this.selected = this.selected.filter(p => p !== path);
        } else {
            this.selected.push(path);
        }
    },
    isSelected(path) {
        return this.selected.includes(path);
    },
    selectAll() {
        this.selected = Object.keys(items);
    },
    deselectAll() {
        this.selected = [];
    }
}" x-init="selected = Object.keys(items)">
    <div class="fi-fo-field-wrp">
        <div class="flex justify-end gap-2 mb-2">
            <button type="button" @click="selectAll()" class="text-sm text-primary-600 hover:underline">{{ __('Select all') }}</button>
            <span class="text-gray-300">|</span>
            <button type="button" @click="deselectAll()" class="text-sm text-primary-600 hover:underline">{{ __('Deselect all') }}</button>
        </div>
        <div class="grid grid-cols-1 gap-0.5 max-h-80 overflow-y-auto rounded-lg border border-gray-200 dark:border-gray-700 p-2">
            @foreach($items as $path => $item)
                <label
                    class="flex items-center gap-2 px-2 py-1.5 rounded-md cursor-pointer hover:bg-gray-50 dark:hover:bg-gray-800"
                    :class="{ 'bg-primary-50 dark:bg-primary-900/20': isSelected('{{ $path }}') }"
                >
                    <input
                        type="checkbox"
                        class="fi-checkbox-input rounded border-gray-300 text-primary-600 shadow-sm focus:ring-primary-500 dark:border-gray-600 dark:bg-gray-700"
                        :checked="isSelected('{{ $path }}')"
                        @change="toggleItem('{{ $path }}')"
                    >
                    @if($item['type'] === 'dir')
                        <x-heroicon-o-folder class="w-4 h-4 text-yellow-500 shrink-0" />
                    @else
                        <x-heroicon-o-document class="w-4 h-4 text-gray-400 shrink-0" />
                    @endif
                    <span class="text-sm truncate">{{ $item['name'] }}</span>
                    @if(!empty($item['size']))
                        <span class="text-xs text-gray-400 ml-auto shrink-0">{{ $item['size'] }}</span>
                    @endif
                </label>
            @endforeach
        </div>
    </div>
</div>

<script>
    var items = @json(collect($items)->mapWithKeys(fn ($item, $path) => [$path => $item['name']])->all());
</script>
