@php
    $record = $getRecord();
    $running = \App\Models\Backup::where('schedule_id', $record->id)->running()->exists();

    if ($running) {
        $label = __('Running...');
        $color = 'warning';
    } elseif ($record->is_active) {
        $label = __('Active');
        $color = 'success';
    } else {
        $label = __('Disabled');
        $color = 'gray';
    }
@endphp

<x-filament::badge :color="$color" :icon="$running ? 'heroicon-o-arrow-path' : null" size="sm">
    {{ $label }}
</x-filament::badge>
