<x-filament-widgets::widget>
    <x-filament::section>
        <div class="flex flex-wrap gap-2">
            @foreach($this->getActions() as $action)
                <x-filament::button
                    :href="$action['url']"
                    :icon="$action['icon']"
                    color="gray"
                    size="sm"
                    tag="a"
                >
                    {{ $action['label'] }}
                </x-filament::button>
            @endforeach
        </div>
    </x-filament::section>
</x-filament-widgets::widget>
