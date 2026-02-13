<details class="mt-4 rounded border border-gray-200 bg-white p-4 shadow-sm open:shadow-sm dark:border-gray-800 dark:bg-gray-900">
<summary class="cursor-pointer list-none fi-section-header-heading">
    {{ $refreshOutputTitle ?? __('Update Output') }}
    @if($refreshOutputAt)
        <span class="ml-2 fi-section-header-description">
            {{ __('Last refreshed at :time', ['time' => $refreshOutputAt]) }}
        </span>
    @endif
</summary>
<div class="mt-3 fi-section-header-description">
    <pre class="whitespace-pre-wrap break-words rounded bg-gray-50 p-4 fi-section-header-description">{{ implode("\n", $refreshOutput) }}</pre>
</div>
</details>
