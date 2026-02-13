@php
    $record = $getRecord();
    $status = $record->status;
    $label = $record->status_label;
    $color = $record->status_color;
    $isInProgress = in_array($status, ['pending', 'running', 'uploading']);
@endphp

<x-filament::badge :color="$color" :icon="$isInProgress ? 'heroicon-o-arrow-path' : null" size="sm">
    {{ $label }}
</x-filament::badge>
