@php
    $jobs = $this->getFilteredLogJobs();
@endphp

<div class="fi-ta">
    <div class="fi-ta-content divide-y divide-gray-200 dark:divide-white/5">
        <table class="fi-ta-table w-full table-auto divide-y divide-gray-200 text-start dark:divide-white/5">
            <thead class="divide-y divide-gray-200 dark:divide-white/5">
                <tr>
                    <th class="fi-ta-header-cell px-3 py-3.5 sm:first-of-type:ps-6 sm:last-of-type:pe-6 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Type') }}</th>
                    <th class="fi-ta-header-cell px-3 py-3.5 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Accounts') }}</th>
                    <th class="fi-ta-header-cell px-3 py-3.5 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Status') }}</th>
                    <th class="fi-ta-header-cell px-3 py-3.5 text-start text-sm font-semibold text-gray-950 dark:text-white">{{ __('Date') }}</th>
                    <th class="fi-ta-header-cell px-3 py-3.5 text-end text-sm font-semibold text-gray-950 dark:text-white">{{ __('Duration') }}</th>
                    <th class="fi-ta-header-cell px-3 py-3.5 sm:last-of-type:pe-6"></th>
                </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 dark:divide-white/5">
                @forelse($jobs as $idx => $job)
                <tr class="fi-ta-row transition duration-75 hover:bg-gray-50 dark:hover:bg-white/5">
                    <td class="fi-ta-cell px-3 py-4 sm:first-of-type:ps-6 sm:last-of-type:pe-6">
                        <x-filament::badge
                            :color="match($job['type'] ?? 'backup') { 'restore', 'file-restore' => 'info', default => 'primary' }"
                            :icon="match($job['type'] ?? 'backup') { 'restore' => 'heroicon-o-arrow-path', 'file-restore' => 'heroicon-o-document-arrow-down', default => 'heroicon-o-cloud-arrow-up' }"
                            size="sm"
                        >{{ ucfirst($job['type'] ?? 'backup') }}</x-filament::badge>
                    </td>
                    <td class="fi-ta-cell px-3 py-4 text-sm text-gray-950 dark:text-white">{{ implode(', ', $job['accounts'] ?? []) ?: '-' }}</td>
                    <td class="fi-ta-cell px-3 py-4">
                        <x-filament::badge
                            :color="match($job['status'] ?? '') { 'success' => 'success', 'partial' => 'warning', 'failed' => 'danger', default => 'gray' }"
                            size="sm"
                        >{{ ucfirst($job['status'] ?? 'running') }}</x-filament::badge>
                    </td>
                    <td class="fi-ta-cell px-3 py-4 text-sm text-gray-950 dark:text-white">{{ $job['started_at'] ?? '' }}</td>
                    <td class="fi-ta-cell px-3 py-4 text-sm text-gray-950 dark:text-white text-end">{{ isset($job['duration_seconds']) ? $job['duration_seconds'] . 's' : '-' }}</td>
                    <td class="fi-ta-cell px-3 py-4 sm:last-of-type:pe-6">
                        <x-filament::button size="sm" color="gray" icon="heroicon-o-eye" wire:click="viewLogJob({{ $idx }})">{{ __('View Log') }}</x-filament::button>
                    </td>
                </tr>
                @empty
                <tr>
                    <td colspan="6" class="px-3 py-8 text-center text-sm text-gray-500">{{ __('No jobs found.') }}</td>
                </tr>
                @endforelse
            </tbody>
        </table>
    </div>
</div>
