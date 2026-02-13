<div class="space-y-4">
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <x-filament::section compact>
            <x-slot name="heading">{{ __('Process ID') }}</x-slot>
            <p class="font-mono fi-section-header-heading">{{ $process->pid }}</p>
        </x-filament::section>

        <x-filament::section compact>
            <x-slot name="heading">{{ __('User') }}</x-slot>
            <x-filament::badge :color="match($process->user) {
                'root' => 'danger',
                'www-data', 'nginx', 'apache' => 'info',
                'mysql', 'postgres' => 'warning',
                default => 'gray',
            }">
                {{ $process->user }}
            </x-filament::badge>
        </x-filament::section>
    </div>

    <x-filament::section compact>
        <x-slot name="heading">{{ __('Command') }}</x-slot>
        <p class="font-mono fi-section-header-description break-all">{{ $process->command }}</p>
    </x-filament::section>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <x-filament::section compact>
            <x-slot name="heading">{{ __('CPU Usage') }}</x-slot>
            <div class="flex items-center gap-2">
                <x-filament::badge :color="$process->cpu > 50 ? 'danger' : ($process->cpu > 20 ? 'warning' : 'success')" size="lg">
                    {{ number_format($process->cpu, 2) }}%
                </x-filament::badge>
            </div>
        </x-filament::section>

        <x-filament::section compact>
            <x-slot name="heading">{{ __('Memory Usage') }}</x-slot>
            <div class="flex items-center gap-2">
                <x-filament::badge :color="$process->memory > 50 ? 'danger' : ($process->memory > 20 ? 'warning' : 'success')" size="lg">
                    {{ number_format($process->memory, 2) }}%
                </x-filament::badge>
            </div>
        </x-filament::section>
    </div>

    <x-filament::section compact>
        <x-slot name="heading">{{ __('Captured At') }}</x-slot>
        <p class="fi-section-header-description">{{ $process->captured_at->format('Y-m-d H:i:s') }}</p>
    </x-filament::section>
</div>
