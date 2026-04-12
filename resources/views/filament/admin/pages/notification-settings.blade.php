<x-filament-panels::page>
    @php
        /** @var \App\Filament\Admin\Pages\NotificationSettings $this */
        $channels = $this->getChannels();
        $severityLabels = $this->getSeverityLabels();
    @endphp

    @if (! config('notifications.v2_enabled'))
        <x-filament::section>
            <div class="flex items-center gap-3">
                <x-heroicon-o-exclamation-triangle class="w-5 h-5 text-warning-500" />
                <div>
                    <strong>v2 notifications are disabled.</strong>
                    Set <code>NOTIFICATIONS_V2_ENABLED=true</code> in your .env and restart the panel
                    to activate channel-based routing. Until then, notifications still send via the
                    legacy email-only path.
                </div>
            </div>
        </x-filament::section>
    @endif

    <x-filament::section>
        <x-slot name="heading">Severity routing</x-slot>
        <x-slot name="description">
            For each severity level, pick which channels should fire. A notification
            is counted as delivered if <em>any</em> of the selected channels succeeds.
        </x-slot>

        @if ($channels->isEmpty())
            <div class="py-6 text-center text-sm text-gray-500">
                <p class="mb-3">No channels configured yet.</p>
                <a href="{{ route('filament.admin.resources.notification-channels.create') }}"
                   class="text-primary-600 hover:underline">
                    Add your first channel →
                </a>
            </div>
        @else
            <div class="overflow-x-auto">
                <table class="w-full text-sm">
                    <thead>
                        <tr class="border-b border-gray-200 dark:border-gray-700">
                            <th class="py-2 text-left">Severity</th>
                            @foreach ($channels as $ch)
                                <th class="py-2 px-3 text-center">
                                    <div class="font-medium">{{ $ch->name }}</div>
                                    <div class="text-xs text-gray-500">{{ $ch->type }}</div>
                                    @if (! $ch->enabled)
                                        <div class="text-xs text-danger-500">(disabled)</div>
                                    @endif
                                </th>
                            @endforeach
                        </tr>
                    </thead>
                    <tbody>
                        @foreach ($severityLabels as $severity => $label)
                            <tr class="border-b border-gray-100 dark:border-gray-800">
                                <td class="py-3 pr-4">
                                    <div class="font-medium">{{ ucfirst($severity) }}</div>
                                    <div class="text-xs text-gray-500">{{ $label }}</div>
                                </td>
                                @foreach ($channels as $ch)
                                    <td class="py-3 px-3 text-center">
                                        <input type="checkbox"
                                               wire:model="routing.{{ $severity }}"
                                               value="{{ $ch->id }}"
                                               class="rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                                    </td>
                                @endforeach
                            </tr>
                        @endforeach
                    </tbody>
                </table>
            </div>
        @endif
    </x-filament::section>

    <x-filament::section>
        <x-slot name="heading">Configured channels</x-slot>
        <x-slot name="description">
            Add, edit, or delete channels in the dedicated resource page. Each row shows the
            type, enable state, and a "Send test" button to verify delivery before wiring it
            into the routing matrix above.
        </x-slot>

        <a href="{{ route('filament.admin.resources.notification-channels.index') }}"
           class="inline-flex items-center gap-2 text-primary-600 hover:underline">
            <x-heroicon-o-queue-list class="w-4 h-4" />
            Open channel list
        </a>
    </x-filament::section>
</x-filament-panels::page>
